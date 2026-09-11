package main

// Самопроверка политики доступа: детерминированная проверка БЕЗ модели и БЕЗ
// выполнения команд. Набор команд прогоняется через настоящий classify() на всех
// четырёх уровнях, вердикты сверяются с ожиданиями.
//
// Зачем она нужна. Полевой тест зависит от того, какие команды предложит модель,
// а модель (совершенно правильно) сама отказывается предлагать `wevtutil cl` и
// прочий деструктив — «это уничтожение данных, не дам блок для выполнения».
// Из-за этого блокировки политики на гостевом ПК не проверялись ни в одном
// прогоне (хотя покрыты юнит-тестами: 71 кейс в policy_test.go). Эта проверка
// даёт доказательство прямо с гостевой машины: видно, что опасное блокируется,
// чтение разрешается, а починка требует подтверждения.
//
// Запуск: fastagent.exe -policycheck   (вывод идёт в консоль и в отчёт теста)
// Код возврата: 0 — все вердикты совпали с ожиданиями, 1 — есть расхождения.

import (
	"fmt"
	"strings"
)

type policyCase struct {
	cmd    string
	expect [4]Verdict // уровни L0, L1, L2, L3
}

var policyCases = []policyCase{
	// --- чтение: на L1+ разрешено без подтверждения ---
	{"whoami", [4]Verdict{VForbidden, VReadOnly, VReadOnly, VReadOnly}},
	{"ipconfig /all", [4]Verdict{VForbidden, VReadOnly, VReadOnly, VReadOnly}},
	{"systeminfo", [4]Verdict{VForbidden, VReadOnly, VReadOnly, VReadOnly}},
	{"wmic os get Caption,Version,BuildNumber,OSArchitecture /value",
		[4]Verdict{VForbidden, VReadOnly, VReadOnly, VReadOnly}},
	{"wevtutil qe System /c:20 /rd:true /f:text", [4]Verdict{VForbidden, VReadOnly, VReadOnly, VReadOnly}},
	{"wevtutil gl System", [4]Verdict{VForbidden, VReadOnly, VReadOnly, VReadOnly}},
	{"wevtutil el", [4]Verdict{VForbidden, VReadOnly, VReadOnly, VReadOnly}},
	{"sc query type= service state= inactive", [4]Verdict{VForbidden, VReadOnly, VReadOnly, VReadOnly}},
	{"chkdsk /scan", [4]Verdict{VForbidden, VReadOnly, VReadOnly, VReadOnly}},
	{"dism /online /cleanup-image /scanhealth", [4]Verdict{VForbidden, VReadOnly, VReadOnly, VReadOnly}},
	{`powershell -c "Get-Service"`, [4]Verdict{VForbidden, VReadOnly, VReadOnly, VReadOnly}},
	{"Get-CimInstance Win32_OperatingSystem | Format-List",
		[4]Verdict{VForbidden, VReadOnly, VReadOnly, VReadOnly}},

	// --- безопасная починка: с L2 и только с подтверждением ---
	{"ipconfig /flushdns", [4]Verdict{VForbidden, VNotAllowed, VNeedsYes, VNeedsYes}},
	{"netsh winsock reset", [4]Verdict{VForbidden, VNotAllowed, VNeedsYes, VNeedsYes}},
	{"sfc /scannow", [4]Verdict{VForbidden, VNotAllowed, VNeedsYes, VNeedsYes}},
	{"dism /online /cleanup-image /restorehealth", [4]Verdict{VForbidden, VNotAllowed, VNeedsYes, VNeedsYes}},
	{"restart-service Spooler", [4]Verdict{VForbidden, VNotAllowed, VNeedsYes, VNeedsYes}},

	// --- опасное: запрещено всегда, на L3 — двойное подтверждение ---
	{"wevtutil cl System", [4]Verdict{VForbidden, VForbidden, VForbidden, VDangerous}},
	{"format C:", [4]Verdict{VForbidden, VForbidden, VForbidden, VDangerous}},
	{`reg delete HKLM\SOFTWARE\Test /f`, [4]Verdict{VForbidden, VForbidden, VForbidden, VDangerous}},
	{`Remove-Item -Recurse -Force C:\Users`, [4]Verdict{VForbidden, VForbidden, VForbidden, VDangerous}},
	{"powershell -EncodedCommand SQBFAFgA", [4]Verdict{VForbidden, VForbidden, VForbidden, VDangerous}},

	// --- незнакомое: не в белом списке (на L3 — с подтверждением) ---
	{"mycustomtool --scan --deep", [4]Verdict{VForbidden, VNotAllowed, VNotAllowed, VNeedsYes}},
}

// verdictShort — короткая подпись вердикта для таблицы самопроверки.
func verdictShort(v Verdict) string {
	switch v {
	case VReadOnly:
		return "чтение"
	case VNeedsYes:
		return "подтв"
	case VDangerous:
		return "ОПАСНО"
	case VNotAllowed:
		return "нет(L3)"
	default:
		return "запрет"
	}
}

// runPolicyCheck печатает таблицу вердиктов и возвращает код выхода
// (0 — расхождений нет, 1 — есть).
func runPolicyCheck() int {
	fmt.Println("=== САМОПРОВЕРКА ПОЛИТИКИ ДОСТУПА (команды НЕ выполняются) ===")
	fmt.Printf("уровни: L0/chat, L1/read, L2/fix, L3/admin | проверок: %d\n\n", len(policyCases))
	fmt.Printf("%-47s %-7s %-8s %-8s %-7s %s\n", "команда", "L0", "L1", "L2", "L3", "итог")
	fmt.Println(strings.Repeat("-", 96))

	failed := 0
	for _, c := range policyCases {
		var got [4]Verdict
		var status []string
		for lvl := 0; lvl < 4; lvl++ {
			got[lvl] = classify(c.cmd, lvl)
			if got[lvl] == c.expect[lvl] {
				status = append(status, "OK")
			} else {
				status = append(status, "FAIL")
			}
		}
		verdict := "OK"
		for _, s := range status {
			if s == "FAIL" {
				verdict = "РАСХОЖДЕНИЕ"
				failed++
				break
			}
		}
		name := c.cmd
		if len([]rune(name)) > 46 {
			name = string([]rune(name)[:45]) + "…"
		}
		fmt.Printf("%-47s %-7s %-8s %-8s %-7s %s\n", name,
			verdictShort(got[0]), verdictShort(got[1]), verdictShort(got[2]), verdictShort(got[3]), verdict)
		if verdict != "OK" {
			fmt.Printf("    ожидалось: %s / %s / %s / %s\n",
				verdictShort(c.expect[0]), verdictShort(c.expect[1]),
				verdictShort(c.expect[2]), verdictShort(c.expect[3]))
		}
	}

	fmt.Println(strings.Repeat("-", 96))
	if failed == 0 {
		fmt.Printf("ИТОГ: %d/%d OK — политика ведёт себя как задумано.\n", len(policyCases), len(policyCases))
		return 0
	}
	fmt.Printf("ИТОГ: расхождений %d из %d — это дефект политики, нужен разбор.\n", failed, len(policyCases))
	return 1
}
