package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Паспорт случая — это не украшение: по нему сценарий узнаётся при рецидиве.
// Поэтому он либо полный, либо его нет; «проверено» без причины и починки —
// обещание, а не опыт.
func TestScenarioKnowledgeAccepted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known.json")
	body := `{
	  "name":"x","title":"Случай","checks":[{"id":"a","title":"a","commands":["hostname"]}],
	  "fixes":[],
	  "knowledge":{"host":"DESKTOP-3DHVKKC","date":"2026-11-19","symptom":"станция не видит сервер",
	    "root_cause":"зомби-сокет","fix":"перезапуск службы","verified":true,"session":"remote-bd3ffb-20261119-084845"}
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sc, err := loadScenario(path)
	if err != nil {
		t.Fatalf("паспорт должен приниматься: %v", err)
	}
	if sc.Knowledge == nil || sc.Knowledge.Host != "DESKTOP-3DHVKKC" || !sc.Knowledge.Verified {
		t.Fatalf("паспорт разобран неверно: %+v", sc.Knowledge)
	}
}

func TestScenarioKnowledgeRejectsBadDate(t *testing.T) {
	path := writeScenarioFile(t, `{"name":"x","title":"x",
	  "checks":[{"id":"a","title":"a","commands":["hostname"]}],
	  "knowledge":{"symptom":"s","date":"19.11.2026"}}`)
	if _, err := loadScenario(path); err == nil {
		t.Fatal("дата не в формате 2006-01-02 должна ломать загрузку")
	}
}

func TestScenarioKnowledgeRejectsVerifiedWithoutFix(t *testing.T) {
	path := writeScenarioFile(t, `{"name":"x","title":"x",
	  "checks":[{"id":"a","title":"a","commands":["hostname"]}],
	  "knowledge":{"symptom":"s","verified":true}}`)
	if _, err := loadScenario(path); err == nil {
		t.Fatal("verified без root_cause/fix должен ломать загрузку")
	}
}

func TestScenarioKnowledgeRejectsNoSymptom(t *testing.T) {
	path := writeScenarioFile(t, `{"name":"x","title":"x",
	  "checks":[{"id":"a","title":"a","commands":["hostname"]}],
	  "knowledge":{"host":"PC1"}}`)
	if _, err := loadScenario(path); err == nil {
		t.Fatal("паспорт без symptom должен ломать загрузку")
	}
}

// Каталог должен отличать прожитый случай от предполётного чек-листа: иначе
// оператор не увидит, что этот кейс уже был и чем кончился.
func TestScenarioCatalogMarksLearned(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "scenarios", "learned")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"case","title":"Прожитый случай","section":"learned","section_title":"Из практики",
	  "checks":[{"id":"a","title":"a","commands":["hostname"]}],
	  "knowledge":{"symptom":"станция не видит сервер","host":"PC1","date":"2026-11-19"}}`
	if err := os.WriteFile(filepath.Join(sub, "case.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	old := baseDir
	baseDir = dir
	defer func() { baseDir = old }()

	cat := scenarioCatalog()
	if len(cat) != 1 || !cat[0].Learned {
		t.Fatalf("каталог не пометил прожитый случай: %+v", cat)
	}
}

func writeScenarioFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "case.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Агент на чужой машине не имеет памяти Jev: проверенные действия приезжают
// вместе с кейсом. Проверяем, что в промпт попадают и команды, и паспорт.
func TestScenarioContextCarriesVerifiedActions(t *testing.T) {
	old := chatScenario
	defer func() { chatScenario = old }()

	chatScenario = nil
	if got := scenarioContext(); got != "" {
		t.Fatalf("без кейса контекст должен быть пустым: %q", got)
	}

	chatScenario = &scenario{
		Title:        "Прожитый случай",
		SectionTitle: "Из практики",
		Checks: []scenarioCheck{{
			ID: "a", Title: "Порт слушается", Commands: []string{"netstat -ano | findstr :9996"},
		}},
		Fixes: []scenarioFix{{
			ID: "f", Title: "Перезапуск", Commands: []string{"restart-service Medoc"}, Recheck: []string{"a"},
		}},
		Knowledge: &scenarioKnowledge{
			Host: "PC1", Date: "2026-11-19", Symptom: "станция не видит сервер",
			RootCause: "зомби-сокет", Fix: "перезапуск", Verified: true,
		},
	}
	got := scenarioContext()
	for _, want := range []string{"ПРОВЕРЕННЫЕ ДЕЙСТВИЯ", "netstat -ano | findstr :9996",
		"restart-service Medoc", "PC1", "зомби-сокет", "проверено"} {
		if !strings.Contains(got, want) {
			t.Errorf("в контексте кейса нет %q:\n%s", want, got)
		}
	}
}
