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

// Правила, которые на живом прогоне 19.11.2026 молчали: журналы не читались
// (wevtutil рвал XPath с пробелами), службы обрезались таблицей до «ed», отчёты
// WER считались только по .dmp. Здесь — тот же вывод Lenovo, но уже с маркерами.
func TestScenarioMarkerRulesCatchLenovoOutput(t *testing.T) {
	baseDir = ".."
	find := func(sc *scenario, id string) scenarioCheck {
		for _, c := range sc.Checks {
			if c.ID == id {
				return c
			}
		}
		t.Fatalf("нет проверки %s", id)
		return scenarioCheck{}
	}

	win, err := loadScenario("standard-windows")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		check  scenarioCheck
		out    string
		wantOK bool
	}{
		{"ошибки System", find(win, "errors_system"),
			"ERR: 11-19 08:30 id=41 Service Control Manager\n", false},
		{"ошибки System — чисто", find(win, "errors_system"), "", true},
		{"ошибки Application", find(win, "errors_app"),
			"ERR: 11-19 08:31 id=1000 Application Error\n", false},
		{"службы", find(win, "services"),
			"STOPPED: edgeupdate / Microsoft Edge Update Service (edgeupdate)\nSTOPPED: WSLService / WSL Service\n", false},
		{"службы работают", find(win, "services"), "", true},
		{"место на C:", find(win, "disk_low"), "LOW SPACE: C: свободно 9.3 ГБ из 111.2\n", false},
		{"крупный файл", find(win, "large_files"),
			"C:\\Users\\Admin\\Downloads\\GDS2 2023.10.19.rar 757968124\n", false},
		{"нет крупных файлов", find(win, "large_files"),
			"C:\\Users\\Admin\\Downloads\\Git-2.52.0-64-bit.exe 66292520\n", true},
		{"отчёты WER", find(win, "wer"),
			"CRASH: AppCrash_explorer.exe_5149\nCRASH: Critical_10.0.19041\n", false},
		{"дампы WER", find(win, "wer"), "DUMP: 111926-01.dmp\n", false},
	}
	for _, tc := range cases {
		res := tc.check.evaluate(tc.out, true, false)
		if res.OK != tc.wantOK {
			t.Errorf("%s: OK=%v, ожидалось %v (сообщение %q)", tc.name, res.OK, tc.wantOK, res.Message)
		}
	}

	mail, err := loadScenario("medoc/imap-ukrnet")
	if err != nil {
		t.Fatal(err)
	}
	if res := find(mail, "mail_errors").evaluate("MAILERR: 11-19 08:34 id=2 Schannel\n", true, false); res.OK {
		t.Error("ошибка почты/SSL в журнале не замечена")
	}
	if res := find(mail, "mail_errors").evaluate("обычная строка\n", true, false); !res.OK {
		t.Errorf("чистый журнал признан проблемой: %q", res.Message)
	}
	if res := find(mail, "medoc_running").evaluate("", true, false); res.OK {
		t.Error("пустой список процессов должен означать «МЕДОК не запущен»")
	}
	if res := find(mail, "medoc_running").evaluate("MEDOC-RUN: medoc id=4242\n", true, false); !res.OK {
		t.Error("запущенный МЕДОК не распознан")
	}

	srv, err := loadScenario("medoc/server-zillya")
	if err != nil {
		t.Fatal(err)
	}
	if res := find(srv, "medoc_service").evaluate("MEDOC-SVC: MEDocServer status=Stopped start=Automatic\n", true, false); res.OK {
		t.Error("остановленная служба МЕДОК признана работающей")
	}
	if res := find(srv, "medoc_service").evaluate("MEDOC-SVC: MEDocServer status=Running start=Automatic\n", true, false); !res.OK {
		t.Errorf("работающая служба МЕДОК не распознана: %q", res.Message)
	}
	if res := find(srv, "fw_rules").evaluate("FW: Правило МЕДОК action=Block dir=Outbound enabled=True\n", true, false); res.OK {
		t.Error("блокирующее правило брандмауэра не замечено")
	}
	if res := find(srv, "app_errors").evaluate("APPERR: 11-19 08:35 id=10013 Zillya\n", true, false); res.OK {
		t.Error("ошибка МЕДОК/фильтра в журнале не замечена")
	}
}
