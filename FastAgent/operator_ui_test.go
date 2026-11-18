package main

import (
	"bufio"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout — перехват печати меню: проверяем то, что оператор реально
// видит, а не внутренние структуры.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

func TestMenuShowsScenarioChoice(t *testing.T) {
	baseDir = ".."
	sc, err := loadScenario("standard-windows")
	if err != nil {
		t.Fatal(err)
	}
	st := &opState{sc: sc}
	out := captureStdout(t, func() { opPrintMenu(st) })
	for _, want := range []string{"6  Сценарий", "Стандарт / Стандартная диагностика Windows", "2  Диагностика"} {
		if !strings.Contains(out, want) {
			t.Fatalf("в меню нет %q:\n%s", want, out)
		}
	}
}

func TestScenarioChoiceListsSectionsAndSwitches(t *testing.T) {
	baseDir = ".."
	st := &opState{reader: bufio.NewReader(strings.NewReader("0\n"))}
	out := captureStdout(t, func() { opScenarios(st) })
	for _, want := range []string{"── СЦЕНАРИИ ──", "МЕДОК", "medoc/server-zillya", "Стандарт", "0  назад"} {
		if !strings.Contains(out, want) {
			t.Fatalf("в списке нет %q:\n%s", want, out)
		}
	}
	if st.sc != nil {
		t.Fatalf("отмена не должна менять сценарий: %+v", st.sc)
	}

	// Выбор первого кейса раздела «МЕДОК»: сценарий меняется, итоги сбрасываются.
	st = &opState{
		reader:    bufio.NewReader(strings.NewReader("1\n")),
		sc:        &scenario{Name: "old", Title: "Старый"},
		results:   []checkResult{{ID: "a", OK: false}},
		fixesDone: []string{"f"},
	}
	out = captureStdout(t, func() { opScenarios(st) })
	if st.sc == nil || !strings.Contains(st.sc.Title, "IMAP") {
		t.Fatalf("переключение не сработало: %+v / %s", st.sc, out)
	}
	if st.results != nil || st.fixesDone != nil {
		t.Fatalf("итоги старого сценария должны сброситься: %+v %+v", st.results, st.fixesDone)
	}
	if !strings.Contains(out, "[OK] сценарий: МЕДОК") {
		t.Fatalf("нет подтверждения переключения:\n%s", out)
	}
}

func TestAdviceShowsHintAndRelevantPractice(t *testing.T) {
	baseDir = ".."
	sc, err := loadScenario("medoc/imap-ukrnet")
	if err != nil {
		t.Fatal(err)
	}
	st := &opState{sc: sc}
	probs := []checkResult{{ID: "tcp_imap_993", Title: "Порт 993 (IMAP) открывается", Priority: 1, Message: "не открывается"}}
	out := captureStdout(t, func() { opAdvice(st, probs) })
	for _, want := range []string{"ЧТО ДЕЛАТЬ", "Порт 993 (IMAP) открывается", "Порты наружу не открываются", "с телефона"} {
		if !strings.Contains(out, want) {
			t.Fatalf("в подсказках нет %q:\n%s", want, out)
		}
	}
	// Практика про учётку относится к другому симптому — её быть не должно.
	if strings.Contains(out, "Если порты открыты, а письма не идут") {
		t.Fatalf("показана чужая практика:\n%s", out)
	}
}
