package main

import (
	"os"
	"testing"
)

// Вывод DISM при нехватке прав — образец взят из полевого журнала
// (прогон 1, гостевой ПК LENOVO): команда вернула код 1 с этим текстом.
func TestIsElevationErrorDISM(t *testing.T) {
	out := "\r\nError: 740\r\n\r\nElevated permissions are required to run DISM. \r\n" +
		"Use an elevated command prompt to complete these tasks.\r\n"
	if !isElevationError(out) {
		t.Fatal("вывод DISM про 740 не распознан как ошибка прав")
	}
}

func TestIsElevationErrorVariants(t *testing.T) {
	cases := []string{
		"Error: 740",
		"error:740",
		"ELEVATED PERMISSIONS ARE REQUIRED",
		"Для выполнения операции требуются права администратора.",
		"You must be an administrator to run this tool.",
		"This command requires elevation to continue",
	}
	for _, c := range cases {
		if !isElevationError(c) {
			t.Errorf("не распознано: %q", c)
		}
	}
}

func TestIsElevationErrorNegative(t *testing.T) {
	// Обычные коды и текст не должны трактоваться как нехватка прав.
	cases := []string{
		"",
		"SERVICE_NAME: AJRouter",
		"Кэш сопоставителя DNS успешно очищен.",
		"Error: 0x800f081f",
		"код 1: совпадений не найдено",
		"740 ошибок в журнале", // 740 без слова об ошибке/правах
	}
	for _, c := range cases {
		if isElevationError(c) {
			t.Errorf("ложное срабатывание: %q", c)
		}
	}
}

// На Linux проверка прав идёт по EUID: root — «повышен», и результат известен
// всегда (на Windows функцию покрывают живым прогоном, т.к. нужен whoami /groups).
func TestProbePrivilegesLinux(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("тест запущен от root — ветка определена, поведение то же")
	}
	elevated, known := probePrivileges()
	if !known {
		t.Fatal("на Linux права должны определяться всегда")
	}
	if elevated {
		t.Fatal("не-root процесс не может считаться повышенным")
	}
}
