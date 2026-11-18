package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadScenarioWindows(t *testing.T) {
	baseDir = ".."
	sc, err := loadScenario("standard-windows")
	if err != nil {
		t.Fatalf("сценарий не загружен: %v", err)
	}
	if len(sc.Checks) < 8 || len(sc.Fixes) < 3 {
		t.Fatalf("ожидалось не меньше 8 проверок и 3 починок, получилось %d/%d", len(sc.Checks), len(sc.Fixes))
	}
	for _, c := range sc.Checks {
		if c.ID == "" || len(c.Commands) == 0 {
			t.Fatalf("проверка без id или команд: %+v", c)
		}
		if c.Priority < 1 || c.Priority > 3 {
			t.Fatalf("проверка %s: приоритет %d", c.ID, c.Priority)
		}
	}
	for _, f := range sc.Fixes {
		if strings.TrimSpace(f.Confirm) == "" {
			t.Fatalf("починка %s без вопроса подтверждения", f.ID)
		}
	}
}

func TestLoadScenarioLinux(t *testing.T) {
	baseDir = ".."
	if _, err := loadScenario("standard-linux"); err != nil {
		t.Fatalf("linux-сценарий не загружен: %v", err)
	}
}

func TestLoadScenarioBadRegex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	body := `{"name":"bad","checks":[{"id":"x","title":"x","commands":["x"],"rules":[{"regex":"("}]}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadScenario(path); err == nil {
		t.Fatal("битый regex должен давать ошибку загрузки")
	}
}

func TestScenarioEvaluateRules(t *testing.T) {
	c := scenarioCheck{ID: "e", Title: "ошибки", Priority: 1,
		Rules: []scenarioRule{{
			Regex:   `(?m)^\s*Level:\s*[12]\b`,
			Message: "ошибок: {n}",
		}}}
	res := c.evaluate("Level: 2\nLevel: 3\nLevel: 1\n", true, false)
	if res.OK || res.Message != "ошибок: 2" {
		t.Fatalf("ожидалась проблема с числом 2, получили %+v", res)
	}
	res = c.evaluate("Level: 3\n", true, false)
	if !res.OK {
		t.Fatalf("без совпадений проверка должна быть ОК: %+v", res)
	}
}

func TestScenarioEvaluateAbsentRule(t *testing.T) {
	c := scenarioCheck{ID: "d", Title: "защита", Priority: 2,
		Rules: []scenarioRule{{Regex: "AntivirusEnabled", ProblemIf: "absent",
			Message: "нет данных", OKMessage: "защита включена"}}}
	if res := c.evaluate("AntivirusEnabled : True", true, false); !res.OK || res.Message != "защита включена" {
		t.Fatalf("ожидалось ОК: %+v", res)
	}
	if res := c.evaluate("команда не найдена", true, false); res.OK || res.Message != "нет данных" {
		t.Fatalf("отсутствие признака — проблема: %+v", res)
	}
}

func TestScenarioEvaluateCommandErrorAndDenied(t *testing.T) {
	c := scenarioCheck{ID: "x", Title: "x", Priority: 3}
	if res := c.evaluate("что-то", false, false); res.OK {
		t.Fatal("ошибка команды не может быть «в порядке»")
	}
	c.IgnoreExit = true
	if res := c.evaluate("что-то", false, false); !res.OK {
		t.Fatal("ignore_exit должен снимать ошибку кода возврата")
	}
	if res := c.evaluate("что-то", true, true); res.OK {
		t.Fatal("отказ гостя «нужно подтверждение» должен быть проблемой")
	}
}

func TestScenarioEmptyProblem(t *testing.T) {
	c := scenarioCheck{ID: "x", Title: "x", EmptyProblem: true}
	if res := c.evaluate("  \n", true, false); res.OK {
		t.Fatal("пустой вывод при empty_problem — проблема")
	}
}

func TestVerifyTicketAgainstManifest(t *testing.T) {
	dir := t.TempDir()
	baseDir = dir
	if err := os.MkdirAll(filepath.Join(dir, "scenarios"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "fastagent.exe")
	if err := os.WriteFile(exe, []byte("ticket-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := remoteFileSum(exe)
	if err != nil {
		t.Fatal(err)
	}
	man := `{"guest_exe":{"name":"fastagent.exe","size":12,"sha256":"` + sum + `"}}`
	if err := os.WriteFile(filepath.Join(dir, "scenarios", "manifest.json"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, detail := verifyTicket(exe); !ok {
		t.Fatalf("эталон должен совпасть: %s", detail)
	}
	// Подмена файла: размер тот же, содержимое другое.
	if err := os.WriteFile(exe, []byte("TICKET-BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, detail := verifyTicket(exe); ok || !strings.Contains(detail, "sha256") {
		t.Fatalf("подмена должна ловиться по sha256: ok=%v %s", ok, detail)
	}
	// Обрезанная копия: другой размер.
	if err := os.WriteFile(exe, []byte("ticket"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, detail := verifyTicket(exe); ok || !strings.Contains(detail, "размер") {
		t.Fatalf("обрезка должна ловиться по размеру: ok=%v %s", ok, detail)
	}
}

func TestVerifyTicketWithoutManifest(t *testing.T) {
	baseDir = t.TempDir()
	if ok, _ := verifyTicket(filepath.Join(baseDir, "нет.exe")); ok {
		t.Fatal("без эталона проверка не может считаться пройденной")
	}
}

func TestParseJournalHeader(t *testing.T) {
	out := "host=LENOVO\npath=C:\\logs\\a.log\nsize=2048\nlines=7\n---\nstep"
	host, path, lines, size := parseJournalHeader(out)
	if host != "LENOVO" || path != `C:\logs\a.log` || lines != 7 || size != 2048 {
		t.Fatalf("шапка разобрана неверно: %q %q %d %d", host, path, lines, size)
	}
}
