package main

// Тесты разбора вывода PowerShell.
//
// Повод — дефект 15 из полевого прогона 7 (LENOVO, реальный Windows). Когда
// stdout+stderr PowerShell перенаправлены в pipe, он сериализует потоки ошибок
// и прогресса в CLIXML. В отчёт и в контекст модели уходили килобайты XML:
// пачки «Preparing modules for first use.» и сообщения об ошибках с escapes
// _x000D_/_x000A_ и удвоенными обратными слэшами. Модель это терпела и даже
// объясняла оператору, что «CLIXML-обёртка — это не ошибка», но человеку
// читать такое нельзя.
//
// Образцы ниже — НАСТОЯЩИЕ, вырезаны из отчёта прогона 7: первый — вывод
// `Get-Item` (код 1, 4 progress-записи + 8 записей ошибок), второй —
// `Get-AuthenticodeSignature` с кириллическим путём.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const realCLIXMLGetItem = `#< CLIXML
<Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04"><Obj S="progress" RefId="0"><TN RefId="0"><T>System.Management.Automation.PSCustomObject</T><T>System.Object</T></TN><MS><I64 N="SourceId">1</I64><PR N="Record"><AV>Preparing modules for first use.</AV><AI>0</AI><Nil /><PI>-1</PI><PC>-1</PC><T>Completed</T><SR>-1</SR><SD> </SD></PR></MS></Obj><Obj S="progress" RefId="1"><TNRef RefId="0" /><MS><I64 N="SourceId">1</I64><PR N="Record"><AV>Preparing modules for first use.</AV><AI>0</AI><Nil /><PI>-1</PI><PC>-1</PC><T>Completed</T><SR>-1</SR><SD> </SD></PR></MS></Obj><Obj S="progress" RefId="2"><TNRef RefId="0" /><MS><I64 N="SourceId">2</I64><PR N="Record"><AV>Preparing modules for first use.</AV><AI>0</AI><Nil /><PI>-1</PI><PC>-1</PC><T>Completed</T><SR>-1</SR><SD> </SD></PR></MS></Obj><Obj S="progress" RefId="3"><TNRef RefId="0" /><MS><I64 N="SourceId">2</I64><PR N="Record"><AV>Preparing modules for first use.</AV><AI>0</AI><Nil /><PI>-1</PI><PC>-1</PC><T>Completed</T><SR>-1</SR><SD> </SD></PR></MS></Obj><S S="Error">Get-Item : Cannot bind argument to parameter 'Path' because it is null._x000D__x000A_</S><S S="Error">At line:1 char:10_x000D__x000A_</S><S S="Error">+ Get-Item (Get-Command mycustomtool -ErrorAction SilentlyContinue).Sou ..._x000D__x000A_</S><S S="Error">+          ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~_x000D__x000A_</S><S S="Error">    + CategoryInfo          : InvalidData: (:) [Get-Item], ParameterBindingValidationException_x000D__x000A_</S><S S="Error">    + FullyQualifiedErrorId : ParameterArgumentValidationErrorNullNotAllowed,Microsoft.PowerShell.Commands.GetItemComm _x000D__x000A_</S><S S="Error">   and_x000D__x000A_</S><S S="Error"> _x000D__x000A_</S></Objs>`

const realCLIXMLSignature = `#< CLIXML
<Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04"><Obj S="progress" RefId="0"><TN RefId="0"><T>System.Management.Automation.PSCustomObject</T><T>System.Object</T></TN><MS><I64 N="SourceId">1</I64><PR N="Record"><AV>Preparing modules for first use.</AV><AI>0</AI><Nil /><PI>-1</PI><PC>-1</PC><T>Completed</T><SR>-1</SR><SD> </SD></PR></MS></Obj><Obj S="progress" RefId="1"><TNRef RefId="0" /><MS><I64 N="SourceId">1</I64><PR N="Record"><AV>Preparing modules for first use.</AV><AI>0</AI><Nil /><PI>-1</PI><PC>-1</PC><T>Completed</T><SR>-1</SR><SD> </SD></PR></MS></Obj><S S="Error">Get-AuthenticodeSignature : File C:\путь\к\mycustomtool.exe was not found._x000D__x000A_</S><S S="Error">At line:1 char:1_x000D__x000A_</S><S S="Error">+ Get-AuthenticodeSignature 'C:\путь\к\mycustomtool.exe' | Format-List  ..._x000D__x000A_</S><S S="Error">+ ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~_x000D__x000A_</S><S S="Error">    + CategoryInfo          : ObjectNotFound: (:) [Get-AuthenticodeSignature], FileNotFoundException_x000D__x000A_</S><S S="Error">    + FullyQualifiedErrorId : SignatureCommandsBaseFileNotFound,Microsoft.PowerShell.Commands.GetAuthenticodeSignature _x000D__x000A_</S><S S="Error">   Command_x000D__x000A_</S><S S="Error"> _x000D__x000A_</S></Objs>`

func TestStripCLIXMLRealRun7Block(t *testing.T) {
	// В блоке 8 XML-записей ошибок, но одна из них пустая (PS добавляет
	// запись-разделитель) — восстанавливаем 7 содержательных строк.
	if n := strings.Count(realCLIXMLGetItem, `<S S=`); n != 8 {
		t.Fatalf("в образце %d XML-записей, ожидали 8", n)
	}
	got, recovered := stripCLIXML(realCLIXMLGetItem)
	if recovered != 7 {
		t.Fatalf("восстановлено записей ошибок: %d, ожидали 7", recovered)
	}
	if strings.Contains(got, "CLIXML") || strings.Contains(got, "<Objs") {
		t.Fatalf("CLIXML остался в выводе: %q", got)
	}
	if strings.Contains(got, "Preparing modules") {
		t.Fatalf("progress-записи не выброшены: %q", got)
	}
	for _, want := range []string{
		"[stderr] Get-Item : Cannot bind argument to parameter 'Path' because it is null.",
		"[stderr] At line:1 char:10",
		"[stderr] + Get-Item (Get-Command mycustomtool -ErrorAction SilentlyContinue).Sou ...",
		"+ CategoryInfo          : InvalidData: (:) [Get-Item], ParameterBindingValidationException",
		"+ FullyQualifiedErrorId : ParameterArgumentValidationErrorNullNotAllowed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("нет строки %q в разобранном выводе:\n%s", want, got)
		}
	}
	if strings.Contains(got, "_x000D_") || strings.Contains(got, "_x000A_") {
		t.Errorf("escapes CLIXML не разобраны: %q", got)
	}
}

// CLIXML удваивает обратный слэш: в отчёте путь выглядел как
// C:\\путь\\к\\mycustomtool.exe. Оператор должен видеть нормальный путь.
func TestStripCLIXMLUndoublesBackslashes(t *testing.T) {
	got, recovered := stripCLIXML(realCLIXMLSignature)
	if recovered == 0 {
		t.Fatal("ошибки не восстановлены")
	}
	if !strings.Contains(got, `C:\путь\к\mycustomtool.exe`) {
		t.Errorf("путь не восстановлен (ожидали одинарные слэши):\n%s", got)
	}
	if strings.Contains(got, `C:\\путь`) {
		t.Errorf("удвоенные слэши остались:\n%s", got)
	}
	if !strings.Contains(got, `[stderr] Get-AuthenticodeSignature : File C:\путь\к\mycustomtool.exe was not found.`) {
		t.Errorf("текст ошибки потерян:\n%s", got)
	}
}

// Блок только из progress-записей выкидывается целиком, без следов.
func TestStripCLIXMLDropsProgressOnlyBlock(t *testing.T) {
	block := "#< CLIXML\r\n<Objs Version=\"1.1.0.1\" xmlns=\"http://schemas.microsoft.com/powershell/2004/04\">" +
		"<Obj S=\"progress\" RefId=\"0\"><MS><PR N=\"Record\"><AV>Preparing modules for first use.</AV></PR></MS></Obj></Objs>\r\n"
	got, recovered := stripCLIXML(block)
	if recovered != 0 {
		t.Fatalf("из progress-блока нечего восстанавливать, получено %d", recovered)
	}
	for _, junk := range []string{"CLIXML", "Preparing modules", "<Objs", "Objs Version"} {
		if strings.Contains(got, junk) {
			t.Fatalf("progress-блок не выброшен (%q осталось): %q", junk, got)
		}
	}
}

// Обычный вывод (в том числе кириллический) не трогаем.
func TestStripCLIXMLKeepsPlainOutput(t *testing.T) {
	for _, s := range []string{
		"Active code page: 866\r\n",
		"Caption=Майкрософт Windows 10 IoT Корпоративная LTSC",
		"Name  DisplayName\n----  -----------\nBITS  Фоновая служба\n",
		"",
	} {
		got, recovered := stripCLIXML(s)
		if got != s || recovered != 0 {
			t.Errorf("вывод изменён: было %q, стало %q (восстановлено %d)", s, got, recovered)
		}
	}
}

// Сквозная проверка: то, что видит оператор, не содержит XML.
func TestFinalizeOutputStripsCLIXML(t *testing.T) {
	got := finalizeOutput([]byte(realCLIXMLGetItem))
	if strings.Contains(got, "<Objs") || strings.Contains(got, "CLIXML") {
		t.Fatalf("CLIXML дошёл до оператора: %q", got)
	}
	if !strings.Contains(got, "[stderr] Get-Item") {
		t.Fatalf("ошибка потеряна: %q", got)
	}
	// cp866-вывод (нативные утилиты) обрабатывается как раньше.
	cp866 := []byte{0x8a, 0xae, 0xe0, 0xaf, 0xae, 0xe0, 0xa0, 0xe2, 0xa8, 0xa2, 0xad, 0xa0, 0xef}
	if got := finalizeOutput(cp866); got != "Корпоративная" {
		t.Fatalf("cp866-декодирование сломалось: %q", got)
	}
}

// Проверка на всём отчёте прогона целиком (если архив прогона на месте):
// после разбора в тексте не должно остаться ни одного XML-блока CLIXML.
// Тест пропускается, если архива нет (логи полевых прогонов в .gitignore).
func TestStripCLIXMLOnWholeRun7Report(t *testing.T) {
	paths, _ := filepath.Glob(filepath.Join("..", "Data", "logs", "field-run-7-lenovo", "field-L2-blk-*.txt"))
	if len(paths) == 0 {
		t.Skip("архив прогона 7 не найден — тест читает реальный отчёт")
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatalf("не читается %s: %v", paths[0], err)
	}
	before := string(raw)
	if n := strings.Count(before, "<Objs Version="); n < 2 {
		t.Fatalf("в отчёте прогона 7 ожидали минимум 2 CLIXML-блока, нашли %d", n)
	}
	after, recovered := stripCLIXML(before)
	// Слово CLIXML само по себе искать нельзя: в отчёте есть и проза модели
	// («CLIXML-обёртка — это просто сериализация прогресс-записей»). Ищем
	// именно разметку и служебные progress-записи.
	for _, junk := range []string{"<Objs", "</Objs>", "#< CLIXML", "Preparing modules"} {
		if strings.Contains(after, junk) {
			t.Fatalf("%q остался в разобранном отчёте", junk)
		}
	}
	if !strings.Contains(after, "[stderr] Get-Item : Cannot bind argument to parameter 'Path'") {
		t.Fatalf("ошибка из CLIXML не восстановлена человеческим текстом")
	}
	if recovered == 0 {
		t.Fatal("ни одной ошибки не восстановлено — разбор бесполезен")
	}
	t.Logf("из отчёта убран CLIXML, восстановлено строк ошибок: %d", recovered)
}

// Таймаут: sfc/dism/chkdsk идут минутами, обычные команды — нет.
func TestCommandTimeout(t *testing.T) {
	long := []string{
		"sfc /scannow",
		"SFC /VERIFYONLY",
		`cmd /c "sfc /scannow"`,
		"dism /online /cleanup-image /restorehealth",
		"chkdsk C: /f",
		"chkdsk /scan",
		"vssadmin list shadows",
		"cleanmgr /sagerun:1",
	}
	for _, cmd := range long {
		if got := commandTimeout(cmd); got != 45*time.Minute {
			t.Errorf("%q: таймаут %v, ожидали 45 мин", cmd, got)
		}
	}
	short := []string{
		"whoami", "ipconfig /all", "ipconfig /flushdns", "wevtutil qe System /c:20",
		"wmic os get Caption /value", "Get-Service",
		`powershell -NoProfile -Command "Get-Service"`,
	}
	for _, cmd := range short {
		if got := commandTimeout(cmd); got != 120*time.Second {
			t.Errorf("%q: таймаут %v, ожидали 120 с", cmd, got)
		}
	}
}
