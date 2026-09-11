package main

// Тесты декодирования вывода Windows-команд. Регрессия из полевого теста на
// LENOVO: русский текст из wmic/systeminfo/ipconfig приходил как "��������".

import (
	"strings"
	"testing"
)

func TestEncodingTables(t *testing.T) {
	if len(cp866Runes) != 128 {
		t.Errorf("таблица cp866: %d рун, ожидалось 128", len(cp866Runes))
	}
	if len(cp1251Runes) != 128 {
		t.Errorf("таблица cp1251: %d рун, ожидалось 128", len(cp1251Runes))
	}
	// Нижняя половина (ASCII) не трогаем.
	if got := decodeTable([]byte("C:\\Windows abc 123"), cp866Runes); got != "C:\\Windows abc 123" {
		t.Errorf("ASCII испорчен: %q", got)
	}
}

func TestCP866Decode(t *testing.T) {
	// П=0x8F р=0xE0 и=0xA8 в=0xA2 е=0xA5 т=0xE2 — проверяем именно байтами
	if got := decodeTable([]byte{0x8F, 0xE0, 0xA8, 0xA2, 0xA5, 0xE2}, cp866Runes); got != "Привет" {
		t.Errorf("cp866 Привет: получено %q", got)
	}
	// Ё=0xF0 ж=0xA6 и=0xA8 к=0xAA
	if got := decodeTable([]byte{0xF0, 0xA6, 0xA8, 0xAA}, cp866Runes); got != "Ёжик" {
		t.Errorf("cp866 Ёжик: получено %q", got)
	}
	// Строка из реального отчёта (LENOVO): русские названия ОС/служб
	for _, want := range []string{
		"Корпоративная LTSC",
		"Диспетчер печати",
		"Служба времени Windows",
		"Удаленный вызов процедур",
	} {
		if got := decodeTable(encodeCP866(want), cp866Runes); got != want {
			t.Errorf("cp866: получено %q, ожидалось %q", got, want)
		}
	}
}

func TestCP1251Decode(t *testing.T) {
	// П=0xCF р=0xF0 и=0xE8 в=0xE2 е=0xE5 т=0xF2
	if got := decodeTable([]byte{0xCF, 0xF0, 0xE8, 0xE2, 0xE5, 0xF2}, cp1251Runes); got != "Привет" {
		t.Errorf("cp1251 Привет: получено %q", got)
	}
	for _, want := range []string{"Ёжик", "Диспетчер печати", "Корпоративная LTSC"} {
		if got := decodeTable(encodeCP1251(want), cp1251Runes); got != want {
			t.Errorf("cp1251: получено %q, ожидалось %q", got, want)
		}
	}
}

func TestDecodeConsoleBytesModes(t *testing.T) {
	defer setEncodingMode("auto")
	utf8Text := []byte("Корпоративная LTSC") // корректный UTF-8
	mangled := encodeCP866("Корпоративная LTSC")

	setEncodingMode("off")
	if got := decodeConsoleBytes(mangled); got != string(mangled) {
		t.Errorf("off: ожидались байты как есть, получено %q", got)
	}
	setEncodingMode("cp866")
	if got := decodeConsoleBytes(mangled); got != "Корпоративная LTSC" {
		t.Errorf("cp866: получено %q", got)
	}
	setEncodingMode("cp1251")
	if got := decodeConsoleBytes(encodeCP1251("Диспетчер")); got != "Диспетчер" {
		t.Errorf("cp1251: получено %q", got)
	}
	// auto: валидный UTF-8 не трогаем даже в режиме auto
	setEncodingMode("auto")
	if got := decodeConsoleBytes(utf8Text); got != string(utf8Text) {
		t.Errorf("auto/utf8: получено %q", got)
	}
	// ASCII в auto всегда идентичен
	if got := decodeConsoleBytes([]byte("Active code page: 866")); got != "Active code page: 866" {
		t.Errorf("auto/ascii: получено %q", got)
	}
	// Воспроизводим именно то, что было в отчёте: cp866-байты -> читаемый текст
	if got := decodeConsoleBytes(encodeCP866("Windows 10 IoT Корпоративная LTSC")); got != "Windows 10 IoT Корпоративная LTSC" {
		t.Errorf("auto/cp866-байты: получено %q", got)
	}
}

func TestIsPowerShellCommand(t *testing.T) {
	ps := []string{
		`Get-Service | Where-Object Status -eq 'Stopped'`,
		`Get-WinEvent -FilterHashtable @{LogName='System'; Level=2} -MaxEvents 10`,
		`Stop-Service Spooler`,
		`Get-NetIPConfiguration | Format-Table InterfaceAlias, IPv4Address`,
		`Restart-Service -Name Spooler`,
	}
	native := []string{
		`systeminfo | findstr /B /C:"OS Name" /C:"OS Version"`,
		`ipconfig /all`,
		`ipconfig /flushdns`,
		`wmic os get Caption,Version,OSArchitecture /value`,
		`sc query type= service state= inactive`,
		`netsh winsock reset`,
		`ver`,
		`dir C:\Windows`,
		`findstr /i error C:\logs\app.log`,
		`schema: systeminfo`,
	}
	for _, c := range ps {
		if !isPowerShellCommand(c) {
			t.Errorf("ожидался PowerShell, но выбран cmd.exe: %q", c)
		}
	}
	for _, c := range native {
		if isPowerShellCommand(c) {
			t.Errorf("ожидался cmd.exe, но выбран PowerShell: %q", c)
		}
	}
}

func TestBuildCmdFile(t *testing.T) {
	script := buildCmdFile(`systeminfo | findstr /C:"OS Name"`, 866, 65001)
	if !strings.HasPrefix(script, "@echo off\r\nchcp 866 >nul\r\n") {
		t.Errorf("нет переключения в OEM-кодировку в начале: %q", script)
	}
	if strings.Count(script, "chcp 866 >nul") != 1 || strings.Count(script, "chcp 65001 >nul") != 1 {
		t.Errorf("ожидалось ровно одно переключение туда и обратно: %q", script)
	}
	// Код возврата надо снять ДО возврата кодировки, иначе вернём код chcp.
	iRC := strings.Index(script, "FASTAGENT_RC=%ERRORLEVEL%")
	iRestore := strings.Index(script, "chcp 65001")
	if iRC == -1 || iRestore == -1 || iRC > iRestore {
		t.Errorf("ERRORLEVEL должен сниматься до смены кодировки: %q", script)
	}
	if !strings.Contains(script, `systeminfo | findstr /C:"OS Name"`) {
		t.Errorf("команда потерялась: %q", script)
	}
	if !strings.HasSuffix(script, "exit /b %FASTAGENT_RC%\r\n") {
		t.Errorf("ожидался exit с исходным кодом: %q", script)
	}
	// Все строки — CRLF (cmd иначе может сломаться на многострочном файле).
	if lf := strings.Count(script, "\n") - strings.Count(script, "\r\n"); lf != 0 {
		t.Errorf("одиночных LF: %d", lf)
	}
}

// --- вспомогательные кодировщики для тестов (обратные таблицам) ---
func encodeWith(text string, table []rune) []byte {
	rev := make(map[rune]byte, 128)
	for i, r := range table {
		rev[r] = byte(0x80 + i)
	}
	var out []byte
	for _, r := range text {
		if r < 0x80 {
			out = append(out, byte(r))
			continue
		}
		if b, ok := rev[r]; ok {
			out = append(out, b)
		}
	}
	return out
}

func encodeCP866(text string) []byte  { return encodeWith(text, cp866Runes) }
func encodeCP1251(text string) []byte { return encodeWith(text, cp1251Runes) }

// --- Спасение чужой кодировки (найдено в четвёртом полевом прогоне) ----------
//
// wevtutil /f:text печатает в cp1251 (ANSI) даже когда консоль в cp866, поэтому
// в отчёте прогона 4 (LENOVO) было "Level: ╧ЁхфєяЁхцфхэшх" вместо
// «Level: Предупреждение». Образцы ниже — реальный фрагмент того отчёта.

func TestRescueMojibakeFromRealReport(t *testing.T) {
	plain := "Level: Предупреждение\n  Opcode: Сведения\n  Keyword: Классический\n"
	raw := encodeCP1251(plain) // байты, которые реально пришли от wevtutil

	mojib := decodeTable(raw, cp866Runes)
	if !strings.Contains(mojib, "╧ЁхфєяЁхцфхэшх") || !strings.Contains(mojib, "╩ырёёшўхёъшщ") {
		t.Fatalf("не воспроизвели мусор из отчёта: %q", mojib)
	}
	if got := rescueMojibake(raw, mojib); got != plain {
		t.Fatalf("мохнатый вывод не спасён:\n  получено  %q\n  ожидалось %q", got, plain)
	}
}

// Полный путь декодера в режиме auto: cp1251-байты должны стать читаемым текстом.
func TestDecodeConsoleBytesAutoRescuesCP1251(t *testing.T) {
	defer setEncodingMode("auto")
	setEncodingMode("auto")
	plain := "Level: Предупреждение"
	if got := decodeConsoleBytes(encodeCP1251(plain)); got != plain {
		t.Fatalf("auto не распознал cp1251: %q", got)
	}
}

// Честный cp866 (sc, ipconfig, systeminfo) и ASCII не должны переключаться.
func TestRescueKeepsGenuineCP866AndASCII(t *testing.T) {
	for _, plain := range []string{
		"Служба маршрутизатора AllJoyn",
		"Кэш сопоставителя DNS успешно очищен.",
		"SERVICE_NAME: AJRouter",
		"Type: 20  WIN32_SHARE_PROCESS  STATE: 1  STOPPED",
	} {
		raw := encodeCP866(plain)
		if got := decodeConsoleBytes(raw); got != plain {
			t.Fatalf("cp866-вывод испорчен: %q -> %q", plain, got)
		}
	}
}

// Оценка «мохнатости»: чужой текст считается по псевдографике внутри слов и по
// редким знакам; обычный русский текст и легитимная псевдографика (tree) — нет.
func TestMojibakeScore(t *testing.T) {
	if bad, cyr := mojibakeScore("Предупреждение"); bad != 0 || cyr != 14 {
		t.Fatalf("чистый текст: bad=%d cyr=%d", bad, cyr)
	}
	if bad, _ := mojibakeScore("╧ЁхфєяЁхцфхэшх"); bad == 0 {
		t.Fatal("мусор не распознан как подозрительный")
	}
	if bad, cyr := mojibakeScore("Level: 0x800f081f"); bad != 0 || cyr != 0 {
		t.Fatalf("ASCII не должен считаться мусором: bad=%d cyr=%d", bad, cyr)
	}
	// Псевдографика как таковая — не признак: ею рисует деревья `tree`.
	if bad, _ := mojibakeScore("├─── System32\n│   └── drivers\n└─── Users"); bad != 0 {
		t.Fatalf("легитимная псевдографика принята за мусор: bad=%d", bad)
	}
}

// Честный cp866-вывод с деревом каталогов не должен переключаться на cp1251.
func TestRescueKeepsGenuineTreeArt(t *testing.T) {
	tree := "│  ├─── Документы\n│  │   ├─── Отчеты\n│  └─── System32\n├─── Program Files\n└─── Users"
	raw := encodeCP866(tree)
	primary := decodeTable(raw, cp866Runes)
	if got := rescueMojibake(raw, primary); got != tree {
		t.Fatalf("дерево каталогов испорчено:\n  получено  %q\n  ожидалось %q", got, tree)
	}
}
