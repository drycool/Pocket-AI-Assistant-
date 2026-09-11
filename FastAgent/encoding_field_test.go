package main

// Живая проверка декодера на настоящем выводе wevtutil из четвёртого полевого
// прогона (LENOVO, 11.09.2026 17:48). Отчёты лежат вне git (данные гостевого
// ПК), поэтому при отсутствии файла тест пропускается, а не падает.

import (
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

const realReport = "../Data/logs/field-run-4-lenovo/field-L2-blk-11.09.2026_174809.txt"

func boxDrawingCount(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x2500 && r <= 0x257F {
			n++
		}
	}
	return n
}

func TestRescueRealWevtutilOutput(t *testing.T) {
	data, err := os.ReadFile(realReport)
	if err != nil {
		t.Skipf("отчёт прогона 4 недоступен (%v) — тест пропущен", err)
	}
	text := string(data)

	// Берём блок вывода wevtutil qe целиком (от первой записи до ответа модели).
	i := strings.Index(text, "  Log Name: System")
	if i < 0 {
		t.Fatal("в отчёте нет вывода wevtutil — проверьте разметку отчёта")
	}
	j := strings.Index(text[i:], "\nAI > ")
	if j < 0 {
		j = len(text) - i
	}
	// Метку обрезки ("…[обрезано N симв.]") агент добавляет ПОСЛЕ декодирования,
	// её символы в таблице cp866 отсутствуют — режем блок перед ней.
	if k := strings.Index(text[i:], "…[обрезано"); k >= 0 && k < j {
		j = k
	}
	block := text[i : i+j]

	// 1. Дефект действительно был: в отчёте псевдографика вместо кириллицы.
	if n := boxDrawingCount(block); n == 0 {
		t.Fatalf("в отчёте нет псевдографики — дефект воспроизвести не удалось (block=%d симв.)", len(block))
	}
	// 2. Обратное кодирование возвращает исходные байты cp1251 (как их выдал wevtutil).
	// Сравниваем РУНЫ, а не байты: кириллица в UTF-8 занимает 2 байта, а в
	// однобайтовой cp866 — 1, поэтому длина в байтах законно уменьшается.
	raw := encodeCP866(block)
	if n := utf8.RuneCountInString(block); n != len(raw) {
		t.Fatalf("обратное кодирование потеряло символы: %d рун -> %d байт", n, len(raw))
	}
	// 3. Декодер обязан распознать чужую кодировку и выдать читаемый русский текст.
	fixed := decodeConsoleBytes(raw)
	if n := boxDrawingCount(fixed); n != 0 {
		t.Fatalf("после декодирования осталась псевдографика: %d символов", n)
	}
	for _, want := range []string{
		"Level: Предупреждение",
		"Opcode: Сведения",
		"Keyword: Классический",
		"Microsoft-Windows-DistributedCOM",
		"LENOVO\\Admin",
		"Параметры разрешений по умолчанию для компьютера не дают разрешения",
	} {
		if !strings.Contains(fixed, want) {
			t.Errorf("в спасённом тексте нет %q", want)
		}
	}
	// 4. И «мохнатых» знаков из старого отчёта тоже нет.
	for _, bad := range []string{"╧Ёхф", "╩ырёё", "╤тхф", "яюы№чют"} {
		if strings.Contains(fixed, bad) {
			t.Errorf("в спасённом тексте остался мусор %q", bad)
		}
	}
	t.Logf("спасено %d символов вывода wevtutil (псевдографики было %d)",
		len([]rune(fixed)), boxDrawingCount(block))
}
