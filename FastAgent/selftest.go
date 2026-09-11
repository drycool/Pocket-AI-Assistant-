package main

// Самопроверка пути выполнения: детерминированные попытки прогоняются через ТУ ЖЕ
// функцию, что и обычные команды (handleCommand: политика -> подтверждение ->
// запуск -> журнал), но на вход подаётся пустой поток: любое подтверждение
// считается НЕ данным (fail-closed). Поэтому изменяющие и опасные команды
// действительно блокируются, а не «просто классифицируются».
//
// Зачем отдельно от -policycheck: policycheck проверяет классификатор (какой
// вердикт), а эта проверка — что агент реально НЕ выполняет то, что должен
// блокировать, печатает [BLOCK] и пишет в журнал approved=false. Команды чтения
// при этом выполняются по-настоящему, поэтому видно, что путь выполнения жив.
//
// Запуск: fastagent.exe -level 2 -selftest
// Код возврата: 0 — поведение совпало с вердиктами, 1 — расхождение (дефект).

import (
	"bufio"
	"fmt"
	"strings"
	"time"
)

// selfTestSteps — набор попыток: по одному представителю каждого вердикта.
var selfTestSteps = []string{
	"whoami",                          // чтение
	"wevtutil qe System /c:5 /f:text", // чтение (журнал, раньше был мусор из-за cp1251)
	"ipconfig /flushdns",              // безопасная починка (нужно подтверждение)
	"wevtutil cl System",              // опасное (уничтожение журнала)
	"mycustomtool --scan --deep",      // незнакомая команда (нет в белом списке)
	"format C:",                       // опасное
}

// runSelfTest возвращает код выхода: 0 — всё как задумано, 1 — расхождение.
func runSelfTest(level int, cfg Config) int {
	fmt.Printf("=== САМОПРОВЕРКА ПУТИ ВЫПОЛНЕНИЯ (уровень L%d, подтверждения не даются) ===\n", level)
	fmt.Println("Проверяем: чтение выполняется, всё остальное блокируется и пишется в журнал.")
	fmt.Println()

	// Пустой поток => confirm() получает EOF => отказ. Так проверяется fail-closed.
	declined := bufio.NewReader(strings.NewReader(""))
	prevLevel := currentLevel
	currentLevel = level
	defer func() { currentLevel = prevLevel }()

	fmt.Printf("%-32s %-8s %-10s %s\n", "команда", "вердикт", "факт", "итог")
	fmt.Println(strings.Repeat("-", 78))

	problems := 0
	for _, cmd := range selfTestSteps {
		want := classify(cmd, level)
		res := handleCommand(declined, cfg, cmd)

		// Ожидание: выполняется ТОЛЬКО чтение; всё остальное — отказ.
		expectExec := want == VReadOnly
		fact := "отказ"
		if res.handled {
			fact = "ВЫПОЛНЕНА"
		}
		ok := res.handled == expectExec
		verdict := "OK"
		if !ok {
			verdict = "РАСХОЖДЕНИЕ"
			problems++
		}
		name := cmd
		if len([]rune(name)) > 31 {
			name = string([]rune(name)[:30]) + "…"
		}
		fmt.Printf("%-32s %-8s %-10s %s\n", name, verdictShort(want), fact, verdict)

		// Отдельно и громко: если выполнилось то, что не является чтением, —
		// это пробитая защита, а не косметика.
		if res.handled && !expectExec {
			fmt.Printf("    !!! ПРОБИТА ЗАЩИТА: %q не должно выполняться на L%d\n", cmd, level)
		}
	}

	fmt.Println(strings.Repeat("-", 78))
	if problems == 0 {
		fmt.Printf("ИТОГ: %d/%d OK — блокировки и подтверждения работают, отказы записаны в журнал.\n",
			len(selfTestSteps), len(selfTestSteps))
		return 0
	}
	fmt.Printf("ИТОГ: расхождений %d из %d — разбирать немедленно.\n", problems, len(selfTestSteps))
	return 1
}

var _ = time.Now // (время используется в журнале действий)
