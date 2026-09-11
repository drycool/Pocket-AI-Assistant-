package main

// Декодирование вывода Windows-команд.
//
// Проблема (поймана в полевом тесте на LENOVO, Windows 10 IoT LTSC): при
// перенаправлении вывода в pipe нативные утилиты (ipconfig, systeminfo, wmic,
// sc) пишут текст в OEM-кодировке (cp866 на русской Windows). Go отдаёт байты
// как есть, поэтому в консоли и в отчёте появлялись "��������" вместо
// "Корпоративная LTSC", и модель получала мусор вместо названий служб.
//
// Решение: если байты не являются корректным UTF-8, декодировать их из
// консольной кодировки (определяется через [Text.Encoding]::GetOEMCP()).
// stdlib-only: таблицы ниже, никаких golang.org/x/text.

import (
	"encoding/base64"
	"fmt"
	"html"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
)

// cp866High — байты 0x80..0xFF кодировки CP866 (OEM, консоль русской Windows).
const cp866High = "" +
	"АБВГДЕЖЗИЙКЛМНОП" + // 0x80-0x8F
	"РСТУФХЦЧШЩЪЫЬЭЮЯ" + // 0x90-0x9F
	"абвгдежзийклмноп" + // 0xA0-0xAF
	"░▒▓│┤╡╢╖╕╣║╗╝╜╛┐" + // 0xB0-0xBF
	"└┴┬├─┼╞╟╚╔╩╦╠═╬╧" + // 0xC0-0xCF
	"╨╤╥╙╘╒╓╫╪┘┌█▄▌▐▀" + // 0xD0-0xDF
	"рстуфхцчшщъыьэюя" + // 0xE0-0xEF
	"ЁёЄєЇїЎў°∙·√№¤■\u00a0" // 0xF0-0xFF

// cp1251High — байты 0x80..0xFF кодировки CP1251 (ANSI, русская Windows).
const cp1251High = "" +
	"ЂЃ‚ѓ„…†‡€‰Љ‹ЊЌЋЏ" + // 0x80-0x8F
	"ђ‘’“”•–—\u0098™љ›њќћџ" + // 0x90-0x9F (0x98 в CP1251 не определён)
	"\u00a0ЎўЈ¤Ґ¦§Ё©Є«¬\u00ad®Ї" + // 0xA0-0xAF
	"°±Ііґµ¶·ё№є»јЅѕї" + // 0xB0-0xBF
	"АБВГДЕЖЗИЙКЛМНОП" + // 0xC0-0xCF
	"РСТУФХЦЧШЩЪЫЬЭЮЯ" + // 0xD0-0xDF
	"абвгдежзийклмноп" + // 0xE0-0xEF
	"рстуфхцчшщъыьэюя" // 0xF0-0xFF

var (
	cp866Runes  = []rune(cp866High)
	cp1251Runes = []rune(cp1251High)

	consoleCPOnce sync.Once
	consoleCP     int
	encMode       = "auto" // windows_encoding из конфига: auto|cp866|cp1251|utf8|off
)

// setEncodingMode применяет настройку windows_encoding из конфига.
func setEncodingMode(mode string) {
	if strings.TrimSpace(mode) != "" {
		encMode = strings.ToLower(strings.TrimSpace(mode))
	}
}

// detectedConsoleCP определяет OEM-кодовую страницу системы (один раз за запуск).
func detectedConsoleCP() int {
	consoleCPOnce.Do(func() {
		consoleCP = 866 // значение по умолчанию для русской Windows
		if runtime.GOOS != "windows" {
			consoleCP = 65001
			return
		}
		out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive",
			"-Command", "[Text.Encoding]::GetOEMCP()").Output()
		if err != nil {
			return
		}
		if n, e := strconv.Atoi(strings.TrimSpace(string(out))); e == nil {
			consoleCP = n
		}
	})
	return consoleCP
}

// decodeTable превращает байты в строку по таблице старшей половины кодировки.
func decodeTable(b []byte, high []rune) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		if c < 0x80 || int(c-0x80) >= len(high) {
			sb.WriteByte(c)
			continue
		}
		sb.WriteRune(high[c-0x80])
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// Спасение «мохнатого» вывода: не всякая утилита пишет в OEM-кодировке.
// wevtutil /f:text печатает в ANSI (cp1251) даже под chcp 866 — в отчёте это
// выглядело как `Level: ╧ЁхфєяЁхцфхэшх` вместо «Предупреждение» (найдено в
// четвёртом полевом прогоне). Поэтому в режиме auto декодируем те же байты и
// второй таблицей, выбирая вариант с меньшим числом «подозрительных» символов
// (псевдографика и редкие знаки, которые появляются только при чтении
// кириллицы не той однобайтной таблицей). При равенстве остаётся вариант
// кодовой страницы консоли, поэтому ASCII и честный cp866 не меняются.
// ---------------------------------------------------------------------------

// rareRunes — знаки, которые встречаются только при чтении кириллицы не той
// однобайтной таблицей (в обычном русском тексте их нет). «Ё»/«ё» здесь НЕТ:
// это нормальные русские буквы.
var rareRunes = func() map[rune]bool {
	m := make(map[rune]bool)
	for _, r := range "ЂЃЄЅІЇЈЉЊЋЌЎЏҐґўѕїјљњћќє∙√№■░▒▓" {
		m[r] = true
	}
	return m
}()

func isCyrRune(r rune) bool { return (r >= 'А' && r <= 'я') || r == 'Ё' || r == 'ё' }

// mojibakeScore считает (подозрительные символы, кириллические буквы).
// Псевдографика сама по себе легитимна — ею рисует деревья `tree` и рамки
// таблиц, поэтому подозрительной считается только псевдографика ВНУТРИ
// кириллического слова: в чужой однобайтовой кодировке байты русских букв
// попадают именно в блок 0x2500-0x257F (cp866 рисует так байты cp1251).
func mojibakeScore(s string) (bad, cyr int) {
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case r >= 0x2500 && r <= 0x257F:
			var left, right rune
			if i > 0 {
				left = rs[i-1]
			}
			if i+1 < len(rs) {
				right = rs[i+1]
			}
			if isCyrRune(left) || isCyrRune(right) {
				bad++
			}
		case rareRunes[r]:
			bad++
		case isCyrRune(r):
			cyr++
		}
	}
	return bad, cyr
}

// rescueMojibake выбирает лучшую декодировку одних и тех же байт: первой идёт
// primary, поэтому при равенстве оценки остаётся именно она.
func rescueMojibake(b []byte, primary string) string {
	best := primary
	bestBad, bestCyr := mojibakeScore(primary)
	if bestBad == 0 {
		return best // чисто: спасать нечего (обычный случай)
	}
	for _, high := range [][]rune{cp1251Runes, cp866Runes} {
		cand := decodeTable(b, high)
		bad, cyr := mojibakeScore(cand)
		if bad < bestBad || (bad == bestBad && cyr > bestCyr) {
			best, bestBad, bestCyr = cand, bad, cyr
		}
	}
	return best
}

// decodeConsoleBytes — единая точка: байты вывода команды -> UTF-8 для консоли,
// отчёта и для модели.
func decodeConsoleBytes(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	switch encMode {
	case "off":
		return string(b)
	case "utf8":
		return string(b)
	case "cp866":
		return decodeTable(b, cp866Runes)
	case "cp1251":
		return decodeTable(b, cp1251Runes)
	}
	// auto: корректный UTF-8 (Linux, современные утилиты) — как есть,
	// иначе — по кодовой странице консоли, с попыткой спасти чужую кодировку.
	if utf8.Valid(b) {
		return string(b)
	}
	switch detectedConsoleCP() {
	case 1251:
		return rescueMojibake(b, decodeTable(b, cp1251Runes))
	default:
		// 866 и прочие (437/850 без кириллицы): для ASCII-вывода результат
		// идентичен, а для русского — единственный осмысленный вариант.
		return rescueMojibake(b, decodeTable(b, cp866Runes))
	}
}

// ---------------------------------------------------------------------------
// Вывод PowerShell: CLIXML вместо текста
// ---------------------------------------------------------------------------

// Проблема (дефект 15, полевой прогон 7). Когда стандартный вывод и stderr
// PowerShell перенаправлены в pipe (а мы читаем именно так), он сериализует
// потоки ошибок и прогресса в CLIXML — служебный XML вместо текста:
//
//	#< CLIXML
//	<Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04">
//	<Obj S="progress" ...><AV>Preparing modules for first use.</AV>...</Obj>
//	<S S="Error">Get-Item : Cannot bind argument to parameter 'Path' because it is null._x000D__x000A_</S>
//	</Objs>
//
// Эти килобайты мусора уходили и в консоль оператора, и в отчёт, и в контекст
// модели (в прогоне 7 модель даже объясняла их пользователю: «CLIXML-обёртка —
// это просто сериализация прогресс-записей»). Прогресс-записи не нужны вообще,
// а ошибки нужны ЧЕЛОВЕЧЕСКИМ текстом.
var (
	reCLIXMLBlock = regexp.MustCompile(`(?s)(?:#< CLIXML[ \t]*\r?\n)?<Objs\b[^>]*>.*?</Objs>`)
	reCLIXMLEntry = regexp.MustCompile(`(?s)<S S="[^"]*">(.*?)</S>`)
	reCLIXMLMarks = regexp.MustCompile(`(?m)^#< CLIXML[ \t]*$`)
)

// stripCLIXML заменяет CLIXML-блоки на читаемый текст (`[stderr] …`) и
// возвращает количество восстановленных строк ошибок. Прогресс-записи
// отбрасываются: они не несут диагностической ценности.
func stripCLIXML(out string) (string, int) {
	if !strings.Contains(out, "#< CLIXML") && !strings.Contains(out, "<Objs Version=") {
		return out, 0
	}
	recovered := 0
	out = reCLIXMLBlock.ReplaceAllStringFunc(out, func(block string) string {
		var lines []string
		for _, m := range reCLIXMLEntry.FindAllStringSubmatch(block, -1) {
			if txt := decodeCLIXMLText(m[1]); txt != "" {
				lines = append(lines, "[stderr] "+txt)
				recovered++
			}
		}
		if len(lines) == 0 {
			return "" // только progress-записи — выкидываем целиком
		}
		return "\n" + strings.Join(lines, "\n") + "\n"
	})
	out = reCLIXMLMarks.ReplaceAllString(out, "")
	return out, recovered
}

// decodeCLIXMLText разбирает текст одной записи CLIXML: убирает escapes вида
// _x000D_/_x000A_, раскрывает XML-сущности и снимает удвоение обратного слэша
// (CLIXML экранирует `\` как `\\`, поэтому в отчёте пути выглядели так:
// C:\\путь\\к\\mycustomtool.exe).
func decodeCLIXMLText(s string) string {
	for _, e := range [][2]string{
		{"_x000D_", "\r"}, {"_x000A_", "\n"}, {"_x0009_", "\t"}, {"_x0020_", " "},
	} {
		s = strings.ReplaceAll(s, e[0], e[1])
	}
	s = strings.ReplaceAll(s, `\\`, `\`)
	s = html.UnescapeString(s)
	return strings.TrimRight(s, "\r\n\t ")
}

// finalizeOutput — единая точка обработки вывода команды: декодировать байты
// консоли (cp866/cp1251) и убрать CLIXML-мусор PowerShell.
func finalizeOutput(raw []byte) string {
	out := decodeConsoleBytes(raw)
	cleaned, _ := stripCLIXML(out)
	return cleaned
}

// ---------------------------------------------------------------------------
// Передача скрипта PowerShell: одна оболочка вместо двух
// ---------------------------------------------------------------------------

// encodePowerShellScript кодирует скрипт в Base64 (UTF-16LE) для -EncodedCommand.
//
// Зачем (дефект 13, полевой прогон 6): модель отдавала готовую строку
// `powershell -NoProfile -Command "Get-Service | Where-Object {$_.Status -eq 'Stopped'}"`,
// а агент оборачивал ЕЁ в свою команду `powershell -Command <строка>`. Внешний
// PowerShell раскрывал `$_` в своём контексте (там это $null) — внутренний
// получал `.Status` и падал с «The term '.Status' is not recognized». Команда,
// которая в обычной консоли работает, у агента ломалась, и модель уходила в
// ложный диагноз («символ съелся оболочкой cmd, надо экранировать $»).
//
// -EncodedCommand снимает проблему целиком: скрипт передаётся в Base64, ни одна
// оболочка не раскрывает $переменные, %переменные% и не ест кавычки.
func encodePowerShellScript(script string) string {
	runes := utf16.Encode([]rune(script))
	buf := make([]byte, 0, len(runes)*2)
	for _, r := range runes {
		buf = append(buf, byte(r), byte(r>>8)) // UTF-16LE — порядок байт, ожидаемый powershell.exe
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// rePSWrapper — вызов PowerShell/pwsh (с .exe и без, любой регистр).
var rePSWrapper = regexp.MustCompile(`(?is)^\s*(?:powershell|pwsh)(?:\.exe)?\b`)

// rePSCommandFlag — флаг -c/-command и всё, что за ним (без учёта регистра).
// Ленивый `.*?` берёт ПЕРВОЕ вхождение: наш флаг стоит до тела скрипта.
var rePSCommandFlag = regexp.MustCompile(`(?is)^.*?-(?:c|command)\s+`)

// rePSOpaqueFlag — -e/-enc/-encodedcommand: нагрузку разобрать нельзя.
var rePSOpaqueFlag = regexp.MustCompile(`(?is)^.*?-(?:e|enc|encodedcommand)\b`)

// execScript решает, какой текст реально пойдёт на выполнение.
//
// Если модель сама обернула команду в `powershell ... -Command "..."`, обёртка
// снимается: двумя оболочками текст скрипта проходит через лишний разбор
// (см. encodePowerShellScript). Второй возвращаемый признак — «обёртка снята»,
// он нужен для прозрачности: оператор должен видеть, что именно запускается.
func execScript(cmd string) (script string, unwrapped bool) {
	inner, opaque := unwrapShell(cmd)
	if inner == "" || opaque || inner == strings.TrimSpace(cmd) {
		return cmd, false
	}
	return inner, true
}

// ---------------------------------------------------------------------------
// Запуск команд: выбор оболочки и кодовая страница консоли
// ---------------------------------------------------------------------------

// rePSTokens — признаки синтаксиса PowerShell (cmdlet'ы вида Get-*, $переменные,
// хэш-таблицы @{...}, скобочные блоки).
var rePSTokens = regexp.MustCompile(`(?i)(^|[\s|;(])(get|set|test|select|where|sort|format|measure|new|remove|start|stop|restart|clear|resolve|compare|out|add|import|export|convertto|convertfrom|invoke|enable|disable|update|install|uninstall|join|split|group|foreach|write|read|copy|move|rename|add|enter|exit|push|pop)-[a-z]|\$|@\{|\{[^}]*\}`)

// isPowerShellCommand определяет, нужно ли запускать команду через PowerShell.
// Нативные утилиты (systeminfo, ipconfig, wmic, sc, netsh) идут через cmd.exe:
// там мы управляем кодовой страницей консоли (см. buildCmdFile).
func isPowerShellCommand(cmd string) bool {
	return rePSTokens.MatchString(cmd)
}

// buildCmdFile формирует тело временного .cmd для нативной команды.
// Зачем: консоль в UTF-8 (chcp 65001) ломает вывод нативных утилит — findstr и
// подобные читают OEM-байты (cp866) как UTF-8 и заменяют кириллицу на '?'.
// Поэтому на время команды переключаем консоль в OEM-кодировку и возвращаем
// UTF-8 обратно, чтобы наш собственный вывод оставался читаемым.
func buildCmdFile(cmd string, oemCP, restoreCP int) string {
	return "@echo off\r\n" +
		fmt.Sprintf("chcp %d >nul\r\n", oemCP) +
		cmd + "\r\n" +
		"set \"FASTAGENT_RC=%ERRORLEVEL%\"\r\n" +
		fmt.Sprintf("chcp %d >nul\r\n", restoreCP) +
		"exit /b %FASTAGENT_RC%\r\n"
}

// ensureUTF8Console переключает консоль в UTF-8 (или OEM-совместимый режим, если
// так задано в конфиге) — иначе наш русский интерфейс показывается как «кракозябры»
// при запуске exe двойным щелчком без START.bat.
func ensureUTF8Console() {
	if runtime.GOOS != "windows" || encMode == "cp866" || encMode == "cp1251" {
		return
	}
	_ = exec.Command("cmd", "/c", "chcp 65001 >nul").Run()
}
