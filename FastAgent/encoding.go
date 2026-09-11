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
	// иначе — по кодовой странице консоли.
	if utf8.Valid(b) {
		return string(b)
	}
	switch detectedConsoleCP() {
	case 1251:
		return decodeTable(b, cp1251Runes)
	default:
		// 866 и прочие (437/850 без кириллицы): для ASCII-вывода результат
		// идентичен, а для русского — единственный осмысленный вариант.
		return decodeTable(b, cp866Runes)
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
