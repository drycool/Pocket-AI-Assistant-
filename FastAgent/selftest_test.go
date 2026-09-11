package main

// Тесты самопроверки пути выполнения (-selftest): на каждом уровне поведение
// обязано совпасть с вердиктами политики, а именно — выполняться должно ТОЛЬКО
// чтение. Тест действительно запускает handleCommand, поэтому дополнительно
// проверяет, что блокировка не зависит от ввода оператора (ввод пустой = отказ).

import (
	"testing"
)

func TestRunSelfTestAllLevels(t *testing.T) {
	initPaths()
	for lvl := 0; lvl <= 3; lvl++ {
		if code := runSelfTest(lvl, Config{}); code != 0 {
			t.Errorf("уровень L%d: самопроверка пути выполнения вернула код %d", lvl, code)
		}
	}
}

// Представители всех вердиктов должны присутствовать в наборе попыток, иначе
// самопроверка может «пройти», не проверив ничего важного.
func TestSelfTestStepsCoverVerdicts(t *testing.T) {
	seen := map[Verdict]bool{}
	for _, cmd := range selfTestSteps {
		for lvl := 0; lvl <= 3; lvl++ {
			seen[classify(cmd, lvl)] = true
		}
	}
	for _, v := range []Verdict{VReadOnly, VNeedsYes, VDangerous, VNotAllowed, VForbidden} {
		if !seen[v] {
			t.Errorf("в наборе самопроверки нет ни одной команды с вердиктом %q", v.String())
		}
	}
}
