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

func TestLoadScenarioMedocCases(t *testing.T) {
	baseDir = ".."
	for _, name := range []string{"medoc/imap-ukrnet", "medoc/server-zillya"} {
		sc, err := loadScenario(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if sc.Section != "medoc" || sc.SectionTitle != "МЕДОК" {
			t.Fatalf("%s: раздел %q/%q", name, sc.Section, sc.SectionTitle)
		}
		if len(sc.Practice) == 0 {
			t.Fatalf("%s: практика решения пустая", name)
		}
		for _, f := range sc.Fixes {
			if len(f.Recheck) == 0 {
				t.Fatalf("%s: у починки %s нет повторной проверки", name, f.ID)
			}
		}
	}
}

func TestScenarioCatalogGroupsSections(t *testing.T) {
	baseDir = ".."
	cat := scenarioCatalog()
	if len(cat) < 4 {
		t.Fatalf("в каталоге %d сценариев, ожидалось минимум 4: %+v", len(cat), cat)
	}
	sections := map[string]bool{}
	names := map[string]bool{}
	for _, e := range cat {
		sections[e.Section] = true
		names[e.Name] = true
	}
	if !sections["medoc"] || !sections["standard"] {
		t.Fatalf("нет разделов medoc/standard: %+v", sections)
	}
	if !names["medoc/server-zillya"] || !names["standard-windows"] {
		t.Fatalf("каталог не увидел кейсы: %+v", names)
	}
}

// TestScenarioCommandsAllowedOnGuest — сценарий не имеет права присылать
// команды, которые политика гостя заблокирует: проверки идут без
// подтверждения (только чтение), починки — с подтверждением (L2+).
func TestScenarioCommandsAllowedOnGuest(t *testing.T) {
	baseDir = ".."
	for _, e := range scenarioCatalog() {
		sc, err := loadScenario(e.Name)
		if err != nil {
			t.Fatalf("%s: %v", e.Name, err)
		}
		for _, c := range sc.Checks {
			for _, cmd := range c.Commands {
				if v := classify(cmd, L1Read); v != VReadOnly {
					t.Errorf("%s/%s: проверка %q даёт %s, а должна быть только чтением",
						e.Name, c.ID, cmd, v)
				}
			}
		}
		for _, f := range sc.Fixes {
			for _, cmd := range f.Commands {
				v := classify(cmd, L2Fix)
				if v == VForbidden || v == VNotAllowed {
					t.Errorf("%s/%s: починка %q даёт %s — гость её не выполнит даже с подтверждением",
						e.Name, f.ID, cmd, v)
				}
			}
		}
	}
}

func TestScenarioRecheckMustExist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad-recheck.json")
	body := `{"name":"x","checks":[{"id":"a","title":"a","commands":["ipconfig /all"]}],
		"fixes":[{"id":"f","title":"f","confirm":"y","commands":["ipconfig /flushdns"],"recheck":["nope"]}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadScenario(path); err == nil {
		t.Fatal("recheck на неизвестную проверку должен ломать загрузку")
	}
}

func TestScenarioDuplicateCheckID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dup.json")
	body := `{"name":"x","checks":[{"id":"a","title":"a","commands":["ipconfig /all"]},
		{"id":"a","title":"b","commands":["ipconfig /all"]}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadScenario(path); err == nil {
		t.Fatal("дубль id проверки должен ломать загрузку")
	}
}

func TestPracticeApplies(t *testing.T) {
	failed := map[string]bool{"dns": true}
	if practiceApplies(scenarioPractice{When: []string{"dns"}}, failed) != true {
		t.Fatal("практика должна показываться, когда её проверка провалена")
	}
	if practiceApplies(scenarioPractice{When: []string{"tcp_imap_993"}}, failed) {
		t.Fatal("чужую практику показывать нельзя")
	}
	if !practiceApplies(scenarioPractice{}, failed) {
		t.Fatal("практика без when относится ко всему кейсу")
	}
}

func TestSetResultReplacesByID(t *testing.T) {
	st := &opState{results: []checkResult{{ID: "a", OK: false}, {ID: "b", OK: true}}}
	st.setResult(checkResult{ID: "a", OK: true, Message: "ок"})
	if !st.results[0].OK || st.results[0].Message != "ок" || len(st.results) != 2 {
		t.Fatalf("итог не заменился: %+v", st.results)
	}
	st.setResult(checkResult{ID: "c", OK: false})
	if len(st.results) != 3 {
		t.Fatalf("новая проверка не добавилась: %+v", st.results)
	}
}
