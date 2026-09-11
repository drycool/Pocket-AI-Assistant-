package main

// Тесты по итогам полевого прогона 9 (LENOVO, реальный Windows, штамп 193316).
//
// Прогон 9 подтвердил, что оба исправления прогона 8 работают: вывод sfc
// читается по-русски («Начато сканирование системы…»), его итог виден
// («Проверка 100% завершена. Защита ресурсов Windows не обнаружила нарушений
// целостности.»), 197 строк прогресса свёрнуты в одну пометку, итог DISM
// («Восстановление выполнено успешно. Операция успешно завершена.») тоже
// доехал до модели. Заодно нашлись два новых дефекта — 19 и 20, и образцы
// ниже вырезаны из настоящего отчёта этого прогона.
//
//	Дефект 19: маркер «#< CLIXML» оставался в отчёте отдельной строкой —
//	           семь раз за прогон. Причина: маркер отделён от XML пустой
//	           строкой, а шаблон допускал ровно один перевод строки; плюс на
//	           CRLF-выводе (?m)$ стоит перед \n, а перед ним остаётся \r.
//	Дефект 20: полосы прогресса DISM не сворачивались вообще (4023 символа
//	           полос выдавили из вывода всё остальное). Причина: DISM печатает
//	           после каждой полосы пустую строку, поэтому серия распадалась на
//	           серии по одной полосе, а «серия из одной строки» не сворачивалась.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realDISMProgress — настоящие полосы прогресса DISM из отчёта прогона 9:
// каждая полоса отделена пустой строкой (именно поэтому они не сворачивались).
const realDISMProgress = "\n\n[==                         3.8%                           ] \n" +
	"\n[==                         4.5%                           ] \n" +
	"\n[==                         5.1%                           ] \n" +
	"\n[===                        6.0%                           ] \n" +
	"\n[=====                      10.1%                          ] \n" +
	"\n[=======                    12.8%                          ] \n" +
	"\n[=========                  17.1%                          ] \n" +
	"\n[===========================55.0%                          ] \n" +
	"\n[===========================59.8%==                        ] \n" +
	"\n[==========================100.0%==========================] \n"

// realDISMOutput — фрагмент вывода DISM с полосами и ИТОГОМ в конце.
const realDISMOutput = "Cистема DISM\nВерсия: 10.0.19041.3636\n\nВерсия образа: 10.0.19044.7548\n" +
	realDISMProgress +
	"Восстановление выполнено успешно.\nОперация успешно завершена.\n"

func TestCollapseProgressDISMBarsSeparatedByBlankLines(t *testing.T) {
	got := collapseProgress(realDISMProgress)
	if n := strings.Count(got, "%"); n != 1 {
		t.Fatalf("после сворачивания осталось %d строк с процентом, ожидали 1:\n%s", n, got)
	}
	if !strings.Contains(got, "пропущено 9 строк прогресса") {
		t.Fatalf("нет пометки о пропущенных строках прогресса:\n%s", got)
	}
	if !strings.Contains(got, "100.0%") {
		t.Fatalf("потеряна последняя (свежая) строка прогресса:\n%s", got)
	}
}

func TestCollapseProgressKeepsDISMVerdictAfterBars(t *testing.T) {
	got := collapseProgress(realDISMOutput)
	if !strings.Contains(got, "Восстановление выполнено успешно.") {
		t.Fatalf("итог DISM потерян:\n%s", got)
	}
	if !strings.Contains(got, "Версия образа: 10.0.19044.7548") {
		t.Fatalf("шапка DISM потеряна:\n%s", got)
	}
	// Выигрыш по объёму — ради него всё и делалось: десять полос (сотни
	// символов) сворачиваются в одну пометку плюс одну строку.
	if len(got)*10 > len(realDISMOutput)*6 {
		t.Fatalf("сворачивание почти ничего не дало: %d из %d символов", len(got), len(realDISMOutput))
	}
}

// realCLIXMLMarkedOutput — маркер CLIXML, ОТДЕЛЁННЫЙ ОТ XML ПУСТОЙ СТРОКОЙ,
// и обычный текстовый вывод в том же потоке — так это выглядит в отчёте
// прогона 9 (дефект 19): XML вырезался, а маркер оставался.
const realCLIXMLMarkedOutput = "#< CLIXML\n\n" +
	"<Objs Version=\"1.1.0.1\" xmlns=\"http://schemas.microsoft.com/powershell/2004/04\">" +
	"<S S=\"Error\">Get-Item : Cannot bind argument to parameter 'Path' because it is null._x000D__x000A_</S>" +
	"</Objs>\n" +
	"Name                       DisplayName                 Status StartType\n" +
	"----                       -----------                 ------ ---------\n" +
	"Chemtable Star             Химик                        Running Automatic\n"

func TestStripCLIXMLLeavesNoMarkerWhenBlankLineBeforeObjs(t *testing.T) {
	if !strings.Contains(realCLIXMLMarkedOutput, "#< CLIXML\n\n<Objs") {
		t.Fatal("образец не воспроизводит дефект 19 (маркер должен отделяться пустой строкой)")
	}
	got, recovered := stripCLIXML(realCLIXMLMarkedOutput)
	if strings.Contains(got, "#< CLIXML") {
		t.Fatalf("маркер CLIXML остался в выводе:\n%s", got)
	}
	if strings.Contains(got, "<Objs") || strings.Contains(got, "</Objs>") {
		t.Fatalf("XML остался в выводе:\n%s", got)
	}
	if recovered != 1 {
		t.Fatalf("восстановлено ошибок: %d, ожидали 1", recovered)
	}
	if !strings.Contains(got, "[stderr] Get-Item : Cannot bind argument") {
		t.Fatalf("ошибка не превратилась в текстовую строку:\n%s", got)
	}
	if !strings.Contains(got, "Chemtable Star") {
		t.Fatalf("полезный вывод пострадал:\n%s", got)
	}
}

func TestStripCLIXMLRemovesOrphanMarkerOnCRLF(t *testing.T) {
	// Так выглядит вывод, в котором XML уже вырезан, а маркер остался:
	// строка маркера заканчивается CRLF, и (?m)$ спотыкается о \r.
	raw := "-> код 0, 0.70 с\r\n#< CLIXML\r\n\r\nName   Status\r\nspooler Running\r\n"
	got, _ := stripCLIXML(normalizeOutput(raw))
	if strings.Contains(got, "#< CLIXML") {
		t.Fatalf("маркер остался:\n%q", got)
	}
	if !strings.Contains(got, "spooler Running") {
		t.Fatalf("полезный вывод потерян:\n%q", got)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("осталась пачка пустых строк:\n%q", got)
	}
}

func TestFinalizeOutputRun9DISMSample(t *testing.T) {
	// Полный путь обработки на CRLF-байтах (как их отдаёт cmd): декодирование,
	// нормализация, CLIXML, сворачивание прогресса.
	raw := []byte(strings.ReplaceAll("#< CLIXML\r\n\r\n"+realDISMOutput, "\n", "\r\n"))
	got := finalizeOutput(raw)
	if strings.Contains(got, "#< CLIXML") {
		t.Fatalf("маркер CLIXML остался:\n%s", got)
	}
	if strings.Contains(got, "\r") {
		t.Fatalf("в выводе остались CR:\n%q", got)
	}
	if !strings.Contains(got, "Восстановление выполнено успешно.") {
		t.Fatalf("итог DISM потерян:\n%s", got)
	}
	if n := strings.Count(got, "%"); n != 1 {
		t.Fatalf("полосы прогресса не свёрнуты (строк с процентом: %d):\n%s", n, got)
	}
}

// TestFinalizeOutputRealRun9Report гоняет обработку по НАСТОЯЩЕМУ отчёту
// прогона 9 (архив Data/logs/field-run-9-lenovo, вне git). Проверяем на живых
// данных ровно то, что обещали оператору: маркеров CLIXML нет, полосы прогресса
// свёрнуты, итоги sfc и DISM на месте.
func TestFinalizeOutputRealRun9Report(t *testing.T) {
	matches, _ := filepath.Glob(filepath.Join("..", "Data", "logs", "field-run-9-lenovo", "field-L2-sfc-*.txt"))
	if len(matches) == 0 {
		t.Skip("архив прогона 9 не найден (локальный артефакт, в git не хранится)")
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("чтение отчёта: %v", err)
	}
	got := finalizeOutput(raw)
	if strings.Contains(got, "#< CLIXML") {
		t.Errorf("в обработанном выводе остался маркер CLIXML (дефект 19)")
	}
	if strings.Contains(got, "<Objs") {
		t.Errorf("в обработанном выводе остался XML CLIXML")
	}
	if !strings.Contains(got, "не обнаружила нарушений целостности") {
		t.Errorf("итог sfc потерян")
	}
	if !strings.Contains(got, "Восстановление выполнено успешно") {
		t.Errorf("итог DISM потерян")
	}
	if n := strings.Count(got, "%  "); n > 4 {
		t.Errorf("полосы прогресса не свёрнуты: строк с процентом %d (дефект 20)", n)
	}
	t.Logf("отчёт прогона 9: %d симв. -> %d симв. после обработки", len(raw), len(got))
}
