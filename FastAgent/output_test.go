package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Обрезка вывода не должна рвать многобайтные символы: в третьем полевом
// прогоне в журнале появился один U+FFFD — след от разреза ровно посередине
// руны (вывод Get-WinEvent, обрезанный до 4000 байт).
func TestTruncateKeepsRunesIntact(t *testing.T) {
	// 2000 русских букв «Д» = 4000 байт ровно; добавляем ещё букву, чтобы
	// граница обрезки попала ВНУТРЬ руны.
	s := strings.Repeat("Д", 2001)

	out := truncate(s, 4000)
	if strings.ContainsRune(out, utf8.RuneError) {
		t.Fatalf("в обрезанном выводе появился U+FFFD: %q", out[len(out)-8:])
	}
	if !utf8.ValidString(out) {
		t.Fatalf("обрезанный вывод — не валидный UTF-8")
	}
	if !strings.HasPrefix(out, "Д") || !strings.Contains(out, "[обрезано") {
		t.Fatalf("нет префикса данных или пометки об обрезке: %q", out)
	}
	if !strings.Contains(out, "[обрезано 1 симв.]") {
		t.Fatalf("помечено неверное число потерянных символов: %q", out[len(out)-20:])
	}
}

func TestTruncateShortStringUnchanged(t *testing.T) {
	if got := truncate("короткая строка", 4000); got != "короткая строка" {
		t.Fatalf("короткая строка изменилась: %q", got)
	}
}

// Английский текст с обрезкой в середине ASCII-байта: резать по границе руны
// здесь нечего, но результат обязан остаться валидным.
func TestTruncateASCII(t *testing.T) {
	out := truncate(strings.Repeat("x", 10), 4)
	if !strings.HasPrefix(out, "xxxx") || !utf8.ValidString(out) {
		t.Fatalf("неожиданный результат: %q", out)
	}
}
