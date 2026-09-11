package main

// Тесты самопроверки политики доступа (-policycheck). Это не дублирование
// policy_test.go: таблица ожиданий из policycheck.go печатается на ГОСТЕВОМ ПК
// и попадает в отчёт полевого теста, поэтому она обязана совпадать с движком —
// иначе на реальной машине будет напечатано «РАСХОЖДЕНИЕ» при верной политике.

import (
	"testing"
)

func TestPolicyCheckCasesMatchEngine(t *testing.T) {
	for _, c := range policyCases {
		for lvl := 0; lvl < 4; lvl++ {
			if got := classify(c.cmd, lvl); got != c.expect[lvl] {
				t.Errorf("%q на L%d: движок даёт %q, а таблица самопроверки ждёт %q",
					c.cmd, lvl, got.String(), c.expect[lvl].String())
			}
		}
	}
}

// Сам прогон самопроверки должен завершаться успехом (код 0).
func TestRunPolicyCheckReturnsZero(t *testing.T) {
	if code := runPolicyCheck(); code != 0 {
		t.Fatalf("самопроверка политики вернула код %d — есть расхождения", code)
	}
}

// Таблица должна покрывать все четыре уровня у каждой команды и не быть пустой.
func TestPolicyCheckCoverage(t *testing.T) {
	if len(policyCases) < 20 {
		t.Fatalf("в самопроверке всего %d команд — слишком мало для доказательства", len(policyCases))
	}
	levels := map[Verdict]int{}
	for _, c := range policyCases {
		for lvl := 0; lvl < 4; lvl++ {
			levels[c.expect[lvl]]++
		}
	}
	for _, v := range []Verdict{VReadOnly, VNeedsYes, VDangerous, VNotAllowed, VForbidden} {
		if levels[v] == 0 {
			t.Errorf("в таблице самопроверки нет ни одного вердикта %q", v.String())
		}
	}
}
