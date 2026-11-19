// session_archive_test.go — проверки R4: архив сессии, журнал гостя, /save и
// вердикт /fb.
//
// Проверяется контракт, на котором держится «следующий случай дешевле»:
// архив показывает и то, что просил оператор, и то, что решил гость (verdict,
// approved), а уроки в память попадают только по явной команде. Живой прогон
// «два процесса через сеть» — в FIELD-TEST.md.

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withLogsDir — logsDir на время теста: журнал гостя и txt-архив пишутся туда.
func withLogsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := logsDir
	logsDir = dir
	t.Cleanup(func() { logsDir = prev })
	return dir
}

func TestArchiveTextAndLessons(t *testing.T) {
	arch := newSessionArchive("a1b2c3", "https://pi5:8443")
	if arch.Code != "a1b2c3" || arch.Broker != "https://pi5:8443" {
		t.Fatalf("сессия не та: %+v", arch)
	}
	arch.Add("diag", "diag", remoteResult{OK: true, Exit: 0, MS: 1200, Out: "снимок системы"})
	arch.Add("run sfc /scannow", "run", remoteResult{
		OK: false, Exit: -1, Verdict: "VModify", NeedsApproval: true,
		Out: "R3: инструмент \"run\" удалённо недоступен (только чтение)\nвторая строка",
	})
	arch.Add("journal", "journal", remoteResult{OK: true, Out: "host=WINBOX7\npath=C:\\FastAgent\\logs\\a.log\nlines=2\n---\nline-1\nline-2"})
	arch.Note("/save", "архив записан")

	steps, refused, got, sent := arch.counts()
	if steps != 3 || refused != 1 || got != 0 || sent != 0 {
		t.Fatalf("счётчики: steps=%d refused=%d got=%d sent=%d", steps, refused, got, sent)
	}
	if arch.GuestHost != "WINBOX7" || arch.GuestLog != `C:\FastAgent\logs\a.log` {
		t.Fatalf("host/path из журнала: %q / %q", arch.GuestHost, arch.GuestLog)
	}

	text := arch.Text()
	for _, want := range []string{
		"АРХИВ СЕССИИ УДАЛЁННОЙ ДИАГНОСТИКИ",
		"Сессия:       a1b2c3",
		"Брокер:       https://pi5:8443",
		"Гость:        WINBOX7",
		"Шагов:        3 (отказов и ошибок 1)",
		"fa> run sfc /scannow",
		"итог: ОТКАЗ/ОШИБКА",
		"вердикт: VModify",
		"запрошено подтверждение",
		"ЖУРНАЛ ГОСТЯ (вердикты и подтверждения)",
		"line-1",
		"(архив записан)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в тексте архива нет %q", want)
		}
	}

	lessons := arch.Lessons("Сбой был в очереди печати; помог перезапуск Spooler.")
	for _, want := range []string{
		"# Сессия удалённой диагностики a1b2c3 :: WINBOX7",
		"## Что это",
		"## Ход сессии",
		"| `diag` | ок |",
		"| `run sfc /scannow` | отказ |",
		"## Отказы и ошибки",
		"## Журнал гостя (verdict / approved)",
		"## Заметка оператора",
		"перезапуск Spooler",
	} {
		if !strings.Contains(lessons, want) {
			t.Errorf("в уроках нет %q", want)
		}
	}
	// Заметка не заполнена — так и говорим, а не выдумываем вывод.
	if !strings.Contains(arch.Lessons(""), "Не заполнена") {
		t.Error("пустая заметка не отмечена")
	}
}

func TestArchiveTextTruncatesLongOutput(t *testing.T) {
	arch := newSessionArchive("dd", "http://b")
	arch.Add("read big", "read_file", remoteResult{OK: true, Out: strings.Repeat("я", archiveEntryMax)})
	text := arch.Text()
	if !strings.Contains(text, "вывод обрезан в архиве") {
		t.Fatal("длинный вывод не помечен как обрезанный")
	}
	if len(text) > archiveEntryMax*2 {
		t.Fatalf("архив раздулся: %d байт", len(text))
	}
	// Обрезка — по границе руны: половина «я» в файле была бы мусором.
	i := strings.Index(text, "…[вывод обрезан")
	if i < 0 {
		t.Fatal("нет пометки об обрезке")
	}
	prefix := strings.TrimSpace(text[:i])
	if !strings.HasSuffix(prefix, "я") {
		t.Fatalf("обрезка разрезала UTF-8 руну: %q", prefix[len(prefix)-8:])
	}
}

func TestArchiveDurationIgnoresSaveNote(t *testing.T) {
	arch := newSessionArchive("dur", "http://b")
	arch.Started = time.Now().Add(-2 * time.Minute)
	arch.Entries = []archiveEntry{
		{At: arch.Started.Add(30 * time.Second), Line: "diag", Res: &remoteResult{OK: true}},
		{At: arch.Started.Add(10 * time.Minute), Line: "/save", Note: "архив записан"},
	}
	// Служебная запись /save не должна растягивать сессию: txt-архив пишется до
	// неё, уроки — после, и раньше они называли разное время одной сессии
	// (полевой прогон bd3ffb: 2m4s и 3m5s).
	if got := arch.duration().Round(time.Second); got != 30*time.Second {
		t.Fatalf("длительность с учётом /save: %s, ожидалось 30s", got)
	}
	if txt, md := arch.Text(), arch.Lessons(""); !strings.Contains(txt, "Длительность: 30s") || !strings.Contains(md, "длительность 30s") {
		t.Fatalf("txt и заметка называют разное время:\ntxt=%q\nmd=%q",
			grepLine(txt, "Длительность"), grepLine(md, "длительность"))
	}
}

// grepLine — строка с подстрокой (для сообщений об ошибке).
func grepLine(s, sub string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return "<нет>"
}

func TestTailLines(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"a\nb\nc", 2, "b\nc"},
		{"a\nb\nc", 5, "a\nb\nc"},
		{"a\nb\nc", 0, ""},
		{"", 3, ""},
		{"a\nb\n", 1, "b"},
	}
	for _, c := range cases {
		if got := tailLines(c.in, c.n); got != c.want {
			t.Errorf("tailLines(%q, %d) = %q, ждали %q", c.in, c.n, got, c.want)
		}
	}
}

func TestTruncateNote(t *testing.T) {
	if s, cut := truncateNote("коротко", 100); cut || s != "коротко" {
		t.Fatalf("короткий текст обрезан: %q cut=%v", s, cut)
	}
	long := strings.Repeat("я", 50)
	s, cut := truncateNote(long, 21)
	if !cut {
		t.Fatal("длинный текст не обрезан")
	}
	if len(s) > 21 {
		t.Fatalf("обрезано больше лимита: %d", len(s))
	}
	if !strings.HasSuffix(s, "я") {
		t.Fatalf("разрез по половине руны: %q", s)
	}
}

func TestParseRemoteCommandJournalSave(t *testing.T) {
	cases := []struct {
		line     string
		wantKind string
		check    func(t *testing.T, args map[string]any)
	}{
		{"journal", "journal", func(t *testing.T, a map[string]any) {
			if _, ok := a["lines"]; ok {
				t.Fatalf("лишние lines: %v", a)
			}
		}},
		{"journal 15", "journal", func(t *testing.T, a map[string]any) {
			if a["lines"] != float64(15) {
				t.Fatalf("lines: %v", a["lines"])
			}
		}},
		{"log 3", "journal", func(t *testing.T, a map[string]any) {
			if a["lines"] != float64(3) {
				t.Fatalf("lines: %v", a["lines"])
			}
		}},
		{"journal abc", "journal", func(t *testing.T, a map[string]any) {
			if _, ok := a["lines"]; ok {
				t.Fatalf("нечисло принято за lines: %v", a)
			}
		}},
		{"save", "save", func(t *testing.T, a map[string]any) {
			if a["note"] != "" {
				t.Fatalf("заметка: %v", a["note"])
			}
		}},
		{"save заметка с пробелами", "save", func(t *testing.T, a map[string]any) {
			if a["note"] != "заметка с пробелами" {
				t.Fatalf("заметка: %v", a["note"])
			}
		}},
		{"archive тоже", "save", func(t *testing.T, a map[string]any) {
			if a["note"] != "тоже" {
				t.Fatalf("заметка: %v", a["note"])
			}
		}},
	}
	for _, c := range cases {
		kind, raw, quit, err := parseRemoteCommand(c.line)
		if err != nil || quit || kind != c.wantKind {
			t.Fatalf("%q: kind=%q quit=%v err=%v", c.line, kind, quit, err)
		}
		var args map[string]any
		if err := json.Unmarshal(raw, &args); err != nil {
			t.Fatalf("%q: аргументы не JSON: %v", c.line, err)
		}
		c.check(t, args)
	}
}

func TestLinkJournalTail(t *testing.T) {
	withLogsDir(t)
	for i := 1; i <= 3; i++ {
		logRemoteAction("read_file", VReadOnly, true, "запись-"+string(rune('0'+i)))
	}

	out, rc := linkJournal(map[string]any{"lines": float64(2)})
	if rc != 0 {
		t.Fatalf("журнал: rc=%d\n%s", rc, out)
	}
	if !strings.Contains(out, "host="+hostname()+"\n") {
		t.Errorf("нет host=: %q", out)
	}
	if !strings.Contains(out, "path="+remoteLogPath()) {
		t.Errorf("нет path=: %q", out)
	}
	if !strings.Contains(out, "lines=2") {
		t.Errorf("неверное число строк: %q", out)
	}
	if strings.Count(out, "provenance") != 2 {
		t.Errorf("в хвост попало не 2 записи: %q", out)
	}
	// Хвост — последние записи; первая строка журнала из трёх не в нём.
	if strings.Contains(out, "запись-1") {
		t.Errorf("в хвосте оказалась первая запись: %q", out)
	}
	if !strings.Contains(out, "запись-3") || !strings.Contains(out, "запись-2") {
		t.Errorf("в хвосте нет последних записей: %q", out)
	}

	// Журнала нет — честная ошибка в тексте, а не пустой ответ.
	_ = os.Remove(remoteLogPath())
	out, rc = linkJournal(nil)
	if rc != 1 || !strings.Contains(out, "журнал недоступен") {
		t.Fatalf("нет журнала: rc=%d out=%q", rc, out)
	}
}

func TestSaveArchiveWritesFiles(t *testing.T) {
	logs := withLogsDir(t)
	mem := t.TempDir()
	jev := t.TempDir()
	prevMem, prevRe, prevJev := archiveMemoryDir, archiveReindex, archiveJevDir
	archiveMemoryDir, archiveReindex, archiveJevDir = mem, false, jev
	t.Cleanup(func() {
		archiveMemoryDir, archiveReindex, archiveJevDir = prevMem, prevRe, prevJev
	})

	arch := newSessionArchive("f00d", "http://broker")
	arch.Add("diag", "diag", remoteResult{OK: true, Out: "снимок"})
	if !arch.dirty {
		t.Fatal("сессия с шагом не помечена как изменённая")
	}

	if !saveArchive(arch, "вывод: очередь печати") {
		t.Fatal("архив не записан")
	}
	if arch.dirty {
		t.Fatal("после сохранения сессия всё ещё изменена")
	}

	txts, _ := filepath.Glob(filepath.Join(logs, "remote-f00d-*.txt"))
	if len(txts) != 1 {
		t.Fatalf("txt-архив: %v", txts)
	}
	txt, err := os.ReadFile(txts[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(txt), "АРХИВ СЕССИИ") || !strings.Contains(string(txt), "снимок") {
		t.Errorf("txt-архив неполный: %q", string(txt))
	}

	mds, _ := filepath.Glob(filepath.Join(mem, "projects", "remote-f00d-*.md"))
	if len(mds) != 1 {
		t.Fatalf("уроки: %v", mds)
	}
	md, err := os.ReadFile(mds[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "## Заметка оператора") || !strings.Contains(string(md), "очередь печати") {
		t.Errorf("уроки неполные: %q", string(md))
	}

	// Без каталога памяти — только txt, и это не ошибка.
	archiveMemoryDir = ""
	arch2 := newSessionArchive("beef", "http://broker")
	arch2.Add("diag", "diag", remoteResult{OK: true})
	if !saveArchive(arch2, "") {
		t.Fatal("архив без каталога памяти не записан")
	}
	if mds, _ := filepath.Glob(filepath.Join(mem, "projects", "remote-beef-*.md")); len(mds) != 0 {
		t.Fatalf("уроки записаны без каталога памяти: %v", mds)
	}
}

func TestPostJevFeedback(t *testing.T) {
	var got map[string]any
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"verdict":               "accepted",
			"recorded":              true,
			"known_decision":        true,
			"verdicts_for_decision": 2,
			"previous_verdict":      "partial",
		})
	}))
	defer srv.Close()

	ans, err := postJevFeedback(RagConfig{URL: srv.URL}, "abc123", "accepted", "как сбросить DNS", "помогло")
	if err != nil {
		t.Fatalf("вердикт не отправлен: %v", err)
	}
	if gotPath != "/feedback" || gotMethod != http.MethodPost {
		t.Errorf("запрос: %s %s", gotMethod, gotPath)
	}
	if got["decision_id"] != "abc123" || got["verdict"] != "accepted" || got["source"] != "human" {
		t.Errorf("поля вердикта: %v", got)
	}
	if got["query"] != "как сбросить DNS" || got["comment"] != "помогло" {
		t.Errorf("вопрос/комментарий: %v", got)
	}
	if !ans.Recorded || !ans.KnownDecision || ans.Verdicts != 2 || ans.Previous != "partial" || ans.Verdict != "accepted" {
		t.Errorf("ответ роутера разобран неверно: %+v", ans)
	}

	// Пустой decision_id и неизвестный вердикт — ошибки до сети.
	if _, err := postJevFeedback(RagConfig{URL: srv.URL}, "  ", "accepted", "q", ""); err == nil {
		t.Error("вердикт без decision_id принят")
	}
	if _, err := postJevFeedback(RagConfig{URL: srv.URL}, "abc", "отлично", "q", ""); err == nil {
		t.Error("неизвестный вердикт принят")
	}
	if _, err := postJevFeedback(RagConfig{}, "abc", "accepted", "q", ""); err == nil {
		t.Error("вердикт принят без rag.url")
	}

	// HTTP-ошибка роутера — тоже ошибка, а не «записано».
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "нет такого decision_id", http.StatusBadRequest)
	}))
	defer bad.Close()
	if _, err := postJevFeedback(RagConfig{URL: bad.URL}, "abc", "accepted", "q", ""); err == nil {
		t.Error("HTTP 400 принят за успех")
	}
}

// ---------------------------------------------------------------------------
// R4 через брокер: журнал гостя и /save так, как их видит оператор
// ---------------------------------------------------------------------------

func startR4Guest(t *testing.T, srvURL, code string) (string, []byte, <-chan remoteEvent) {
	t.Helper()
	guest, err := linkPair(srvURL, code)
	if err != nil {
		t.Fatalf("пара: %v", err)
	}
	key := remoteDeriveKey(code, guest)
	events := make(chan remoteEvent, 16)
	go remoteListenEvents(srvURL, code, "s3cret", events)
	startTestGuest(t, srvURL, guest)
	return guest, key, events
}

func TestRemoteJournalThroughBroker(t *testing.T) {
	logs := withLogsDir(t)
	_, srv := newTestBroker(t, "s3cret", "")
	code, _, err := remoteCreateSession(srv.URL, "s3cret")
	if err != nil {
		t.Fatalf("сессия: %v", err)
	}
	t.Cleanup(srv.CloseClientConnections)

	logRemoteAction("read_file", VReadOnly, true, "шаг гостя")
	_, key, events := startR4Guest(t, srv.URL, code)

	res, ok := remoteJournal(srv.URL, "s3cret", code, key, events, 10)
	if !ok || !res.OK {
		t.Fatalf("journal: ok=%v res=%+v", ok, res)
	}
	for _, want := range []string{"host=", "path=", "lines=", "---", "шаг гостя"} {
		if !strings.Contains(res.Out, want) {
			t.Errorf("в журнале гостя нет %q:\n%s", want, res.Out)
		}
	}
	if !strings.HasPrefix(res.Out, "host=") || strings.Index(res.Out, "\n---\n") < 0 {
		t.Fatalf("формат журнала не тот:\n%s", res.Out)
	}

	// Архив забирает host/path из этого же ответа — без второго запроса.
	arch := newSessionArchive(code, srv.URL)
	arch.Add("journal", "journal", res)
	if arch.GuestHost == "" || arch.GuestHost == hostname() {
		t.Logf("host гостя совпал с оператором (тест в одном процессе): %q", arch.GuestHost)
	}
	if !strings.HasSuffix(arch.GuestLog, ".log") {
		t.Fatalf("path журнала: %q", arch.GuestLog)
	}
	if !strings.Contains(arch.Text(), "ЖУРНАЛ ГОСТЯ") {
		t.Error("журнал не попал в текст архива")
	}

	// Журнал — чтение: подтверждения оператора он не требует. Гостю не нужен
	// approved, и в результате нет NeedsApproval.
	if res.NeedsApproval {
		t.Error("journal потребовал подтверждения")
	}
	_ = logs
}

func TestRemoteSaveThroughBroker(t *testing.T) {
	logs := withLogsDir(t)
	mem := t.TempDir()
	prevMem, prevRe := archiveMemoryDir, archiveReindex
	archiveMemoryDir, archiveReindex = mem, false
	t.Cleanup(func() { archiveMemoryDir, archiveReindex = prevMem, prevRe })

	_, srv := newTestBroker(t, "s3cret", "")
	code, _, err := remoteCreateSession(srv.URL, "s3cret")
	if err != nil {
		t.Fatalf("сессия: %v", err)
	}
	t.Cleanup(srv.CloseClientConnections)
	logRemoteAction("read_file", VReadOnly, true, "до сохранения")
	_, key, events := startR4Guest(t, srv.URL, code)

	arch := newSessionArchive(code, srv.URL)
	kind, raw, quit, err := parseRemoteCommand("save тест архива")
	if err != nil || quit || kind != "save" {
		t.Fatalf("разбор save: kind=%q quit=%v err=%v", kind, quit, err)
	}
	// Одна локальная команда: save не ходит на гость за разрешением, но сам
	// забирает журнал и пишет файлы.
	if !remoteRunParsed(nil, srv.URL, "s3cret", code, key, "save тест архива", kind, raw, events, arch) {
		t.Fatal("save вернул отказ")
	}
	if arch.dirty {
		t.Fatal("после save сессия всё ещё изменена")
	}
	if arch.GuestHost == "" {
		t.Fatal("save не приложил журнал гостя")
	}
	// Служебный забор журнала — не шаг оператора: он не должен надувать
	// «Шагов: N» и добавлять в ленту строку, которой оператор не печатал.
	// Единственная локальная запись — пометка самого save.
	if steps, _, _, _ := arch.counts(); steps != 0 {
		t.Fatalf("шагов после save: %d", steps)
	}
	for _, e := range arch.Entries {
		if e.Kind == "journal" {
			t.Fatalf("save добавил журнал шагом: %+v", e)
		}
	}
	if !strings.Contains(arch.Text(), "ЖУРНАЛ ГОСТЯ") {
		t.Error("журнал гостя не попал в текст архива")
	}

	txts, _ := filepath.Glob(filepath.Join(logs, "remote-"+code+"-*.txt"))
	if len(txts) != 1 {
		t.Fatalf("txt-архив: %v", txts)
	}
	txt, _ := os.ReadFile(txts[0])
	if !strings.Contains(string(txt), "ЖУРНАЛ ГОСТЯ") || !strings.Contains(string(txt), "до сохранения") {
		t.Errorf("в архиве нет журнала гостя: %q", string(txt))
	}

	mds, _ := filepath.Glob(filepath.Join(mem, "projects", "remote-"+code+"-*.md"))
	if len(mds) != 1 {
		t.Fatalf("уроки: %v", mds)
	}
	md, _ := os.ReadFile(mds[0])
	if !strings.Contains(string(md), "тест архива") {
		t.Errorf("заметка не попала в уроки: %q", string(md))
	}
}

// Выход из сессии после save не должен терять заметку оператора: quit забирает
// журнал ещё раз (dirty=true) и пишет архив повторно. Дефект смоук-прогона
// 18.11.2026: второй save уходил с пустой заметкой и затирал раздел в уроках.
func TestSaveArchiveKeepsNoteAcrossExitSave(t *testing.T) {
	withLogsDir(t)
	mem := t.TempDir()
	prevMem, prevRe := archiveMemoryDir, archiveReindex
	archiveMemoryDir, archiveReindex = mem, false
	t.Cleanup(func() { archiveMemoryDir, archiveReindex = prevMem, prevRe })

	arch := newSessionArchive("c0de", "http://broker")
	arch.Add("diag", "diag", remoteResult{OK: true, Out: "снимок"})
	if !saveArchive(arch, "заметка оператора") {
		t.Fatal("первый архив не записан")
	}
	if arch.lastNote != "заметка оператора" {
		t.Fatalf("заметка не запомнена: %q", arch.lastNote)
	}

	// После save оператор сделал ещё шаг, затем quit забрал журнал служебно:
	// сессия снова dirty, и архив на выходе обязан сохранить заметку.
	arch.Add("read C:\\Windows\\win.ini", "read_file", remoteResult{OK: true, Out: "текст"})
	arch.takeGuestJournal("host=WINBOX7\npath=C:\\logs\\a.log\nlines=1\n---\nstep")
	if !arch.dirty {
		t.Fatal("после шага сессия не помечена изменённой")
	}
	if !saveArchive(arch, arch.lastNote) {
		t.Fatal("архив на выходе не записан")
	}

	mds, _ := filepath.Glob(filepath.Join(mem, "projects", "remote-c0de-*.md"))
	if len(mds) != 1 {
		t.Fatalf("уроки: %v", mds)
	}
	md, _ := os.ReadFile(mds[0])
	if !strings.Contains(string(md), "заметка оператора") {
		t.Errorf("заметка потеряна при выходе: %q", string(md))
	}
	if strings.Contains(string(md), "Не заполнена") {
		t.Errorf("уроки перезаписаны пустой заметкой: %q", string(md))
	}
}
