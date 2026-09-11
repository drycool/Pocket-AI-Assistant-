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
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
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
