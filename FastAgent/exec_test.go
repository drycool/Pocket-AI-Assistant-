package main

// Тесты передачи скрипта PowerShell на выполнение.
//
// Повод — дефект 13 из полевого прогона 6 (LENOVO, реальный Windows). Модель
// отдала готовую команду:
//
//	powershell -NoProfile -Command "Get-Service | Where-Object {$_.Status -eq 'Stopped'} | ..."
//
// Агент оборачивал её в СВОЮ команду `powershell -Command <строка>`, и внешний
// PowerShell раскрывал `$_` в своём контексте (там это $null). Внутренний
// получал `.Status` и падал: «The term '.Status' is not recognized», код 0.
// Команда, которая в обычной консоли Windows работает, у агента ломалась.
// Хуже того, модель сделала ложный вывод («символ съелся оболочкой cmd, надо
// экранировать $») и потратила ход на обходной синтаксис.
//
// Правило: агент не имеет права менять текст команды, которую предложила модель,
// а скрипт должен доходить до PowerShell без промежуточного разбора.

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"
)

// run6Command — команда из отчёта прогона 6, дословно.
const run6Command = `powershell -NoProfile -Command "Get-Service | Where-Object {$_.Status -eq 'Stopped'} | Select-Object Name,DisplayName,StartType | Format-Table -AutoSize"`

func TestExecScriptStripsModelPowerShellWrapper(t *testing.T) {
	script, unwrapped := execScript(run6Command)
	if !unwrapped {
		t.Fatalf("обёртка powershell не снята: %q", script)
	}
	want := `Get-Service | Where-Object {$_.Status -eq 'Stopped'} | Select-Object Name,DisplayName,StartType | Format-Table -AutoSize`
	if script != want {
		t.Fatalf("снят не тот скрипт:\n получили: %s\n ожидали : %s", script, want)
	}
	if !strings.Contains(script, "$_") {
		t.Fatalf("потерян `$_` — именно это ломало вывод: %q", script)
	}
	if strings.HasPrefix(script, "powershell") {
		t.Fatalf("вложенный powershell остался — будет вторая оболочка: %q", script)
	}
}

func TestExecScriptKeepsBareCommandUnchanged(t *testing.T) {
	for _, cmd := range []string{
		`Get-WinEvent -FilterHashtable @{LogName='System';Level=2} -MaxEvents 10 | Select-Object TimeCreated,ProviderName,Id`,
		"wmic os get Caption,Version,BuildNumber /value",
		"ipconfig /flushdns",
		`wevtutil qe System /c:20 /rd:true /f:text`,
	} {
		script, unwrapped := execScript(cmd)
		if unwrapped {
			t.Errorf("снята обёртка там, где её нет: %q", cmd)
		}
		if script != cmd {
			t.Errorf("текст команды изменён:\n было : %q\n стало: %q", cmd, script)
		}
	}
}

// Опасную нагрузку, которую нельзя разобрать (-EncodedCommand), трогать нельзя:
// её текст должен уйти на выполнение как есть, а политика — запретить.
func TestExecScriptKeepsOpaquePayload(t *testing.T) {
	cmd := "powershell -EncodedCommand SQBFAFgA"
	script, unwrapped := execScript(cmd)
	if unwrapped {
		t.Fatalf("непрозрачная нагрузка разобрана как скрипт: %q", script)
	}
	if script != cmd {
		t.Fatalf("непрозрачная команда изменена: %q", script)
	}
	if v := classify(cmd, L1Read); v != VForbidden {
		t.Fatalf("-EncodedCommand на L1 должен быть запрещён, получено %v", v)
	}
	if v := classify(cmd, L3Admin); v != VDangerous {
		t.Fatalf("-EncodedCommand на L3 должен требовать двойного подтверждения, получено %v", v)
	}
}

func TestUnwrapShellIsCaseInsensitive(t *testing.T) {
	cases := map[string]string{
		`powershell -NoProfile -Command "Get-Service"`: `Get-Service`,
		`PowerShell -Command "Get-Service"`:            `Get-Service`,
		`powershell.exe -c "Get-Service"`:              `Get-Service`,
		`pwsh -Command "Get-Service"`:                  `Get-Service`,
		`powershell -COMMAND "Get-Service"`:            `Get-Service`,
	}
	for cmd, want := range cases {
		inner, opaque := unwrapShell(cmd)
		if opaque {
			t.Errorf("%q ошибочно признана непрозрачной", cmd)
			continue
		}
		if inner != want {
			t.Errorf("%q: получено %q, ожидали %q", cmd, inner, want)
		}
	}
}

func TestUnwrapShellOpaqueFlags(t *testing.T) {
	opaque := []string{
		"powershell -EncodedCommand SQBFAFgA",
		"powershell -e SQBFAFgA",
		"powershell -enc SQBFAFgA",
		`powershell -NoProfile -EncodedCommand SQBFAFgA`,
	}
	for _, cmd := range opaque {
		if _, ok := unwrapShell(cmd); !ok {
			t.Errorf("%q должна считаться непрозрачной", cmd)
		}
	}
	// `-e` внутри скрипта (после -Command) — не признак непрозрачности.
	transparent := []string{
		`powershell -Command "grep -e foo /etc/hostname"`,
		`powershell -NoProfile -Command "Get-Service -Name spooler"`,
	}
	for _, cmd := range transparent {
		if _, ok := unwrapShell(cmd); ok {
			t.Errorf("%q ошибочно признана непрозрачной", cmd)
		}
	}
}

// decodePowerShellScript — обратная операция к encodePowerShellScript
// (Base64 → UTF-16LE → строка). Нужна только тестам: проверяем, что до
// PowerShell доедет ровно тот текст, который предложила модель.
func decodePowerShellScript(encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	if len(raw)%2 != 0 {
		return "", err
	}
	u16 := make([]uint16, 0, len(raw)/2)
	for i := 0; i < len(raw); i += 2 {
		u16 = append(u16, uint16(raw[i])|uint16(raw[i+1])<<8)
	}
	return string(utf16.Decode(u16)), nil
}

func TestEncodePowerShellScriptRoundTrip(t *testing.T) {
	for _, script := range []string{
		run6Command,
		`Get-Service | Where-Object {$_.Status -eq 'Stopped'}`,
		`Get-ChildItem "C:\Program Files" | Select-Object Name`,
		`Get-CimInstance Win32_OperatingSystem | Select-Object Caption,Version`,
		`Write-Output "Кэш сопоставителя DNS успешно очищен"`,
		`$s = Get-Service; $s | Where-Object {$_.Status -eq 'Running'} | Measure-Object`,
	} {
		got, err := decodePowerShellScript(encodePowerShellScript(script))
		if err != nil {
			t.Fatalf("декодирование сломалось на %q: %v", script, err)
		}
		if got != script {
			t.Errorf("скрипт дошёл изменённым:\n было : %q\n стало: %q", script, got)
		}
	}
}

// PowerShell ждёт Base64 от UTF-16LE — перепутанный порядок байт даст мусор
// вместо команды. Проверяем именно формат, а не только round-trip.
func TestEncodePowerShellScriptIsUTF16LE(t *testing.T) {
	enc := encodePowerShellScript("AB")
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("не Base64: %v", err)
	}
	want := []byte{0x41, 0x00, 0x42, 0x00} // 'A',0,'B',0
	if string(raw) != string(want) {
		t.Fatalf("ожидали UTF-16LE %v, получили %v", want, raw)
	}
}

// Сквозная проверка пути выполнения: снимаем обёртку → кодируем → декодируем.
// Именно эта цепочка ломала `$_` в прогоне 6.
func TestExecutionPathPreservesDollarUnderscore(t *testing.T) {
	script, _ := execScript(run6Command)
	back, err := decodePowerShellScript(encodePowerShellScript(script))
	if err != nil {
		t.Fatalf("декодирование: %v", err)
	}
	if !strings.Contains(back, "$_.Status") {
		t.Fatalf("`$_.Status` не дожил до PowerShell: %q", back)
	}
	if strings.Contains(back, "{.Status") {
		t.Fatalf("вернулся тот самый баг прогона 6: %q", back)
	}
}
