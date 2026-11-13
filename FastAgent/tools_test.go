package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// --- glob ---

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		rel, pattern string
		want         bool
	}{
		{"a.log", "*.log", true},
		{"a.txt", "*.log", false},
		{"sub/a.log", "*.log", false},
		{"sub/a.log", "**/*.log", true},
		{"a.log", "**/*.log", true},
		{"deep/x/y/a.log", "**/*.log", true},
		{"Data/config.json", "Data/*.json", true},
		{"Other/config.json", "Data/*.json", false},
		{"a/b/c.txt", "a/**/c.txt", true},
		{"a/c.txt", "a/**/c.txt", true},
		{"README.md", "**/*.log", false},
	}
	for _, c := range cases {
		if got := globMatch(c.rel, c.pattern); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, ожидалось %v", c.rel, c.pattern, got, c.want)
		}
	}
}

// --- read_file ---

func TestReadFileOffsetLimit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(p, []byte("line1\nline2\nline3\nline4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := toolReadFile(map[string]any{"path": p, "offset": 2, "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2: line2") || !strings.Contains(out, "3: line3") {
		t.Fatalf("нет нужных строк: %q", out)
	}
	if strings.Contains(out, "1: line1") || strings.Contains(out, "4: line4") {
		t.Fatalf("offset/limit не сработали: %q", out)
	}
}

func TestReadFileBinaryRefused(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bin.dat")
	if err := os.WriteFile(p, []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := toolReadFile(map[string]any{"path": p}); err == nil {
		t.Fatal("бинарный файл должен быть отвергнут")
	}
}

func TestReadFileSecretRefused(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(p, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := toolReadFile(map[string]any{"path": p}); err == nil {
		t.Fatal("секретный файл должен быть отвергнут")
	}
}

func TestReadFileMissing(t *testing.T) {
	if _, err := toolReadFile(map[string]any{"path": filepath.Join(t.TempDir(), "nope.txt")}); err == nil {
		t.Fatal("отсутствующий файл должен давать ошибку")
	}
}

// --- grep / glob ---

func TestGrepSearchFindsAndFilters(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.log"), []byte("ERROR one\nINFO ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.log"), []byte("ERROR two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("ERROR three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := toolGrepSearch(map[string]any{"pattern": "ERROR", "path": dir, "glob": "*.log"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "ERROR") != 2 {
		t.Fatalf("ожидалось 2 совпадения, получено: %q", out)
	}
	if strings.Contains(out, "three") {
		t.Fatalf("glob не отфильтровал .txt: %q", out)
	}
	if _, err := toolGrepSearch(map[string]any{"pattern": "[", "path": dir}); err == nil {
		t.Fatal("невалидная регулярка должна давать ошибку")
	}
}

func TestGrepSearchNoHits(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644)
	out, err := toolGrepSearch(map[string]any{"pattern": "zzz", "path": dir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "совпадений нет") {
		t.Fatalf("ожидалось честное «нет»: %q", out)
	}
}

func TestFileGlobSearch(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.log"), nil, 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "sub", "b.log"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "c.txt"), nil, 0o644)

	out, err := toolFileGlobSearch(map[string]any{"pattern": "**/*.log", "path": dir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.log") || !strings.Contains(out, "sub/b.log") {
		t.Fatalf("не хватает найденных файлов: %q", out)
	}
	if strings.Contains(out, "c.txt") {
		t.Fatalf("txt не должен попадать: %q", out)
	}
}

// --- write_file ---

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.txt")
	out, err := toolWriteFile(map[string]any{"path": p, "content": "hello\n"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "записано") {
		t.Fatalf("ответ: %q", out)
	}
	raw, _ := os.ReadFile(p)
	if string(raw) != "hello\n" {
		t.Fatalf("содержимое: %q", raw)
	}
	if _, err := os.Stat(p + ".tmp-fastagent"); !os.IsNotExist(err) {
		t.Fatal("временный файл не должен оставаться")
	}
}

func TestWriteFileMissingDir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "no-such-dir", "x.txt")
	if _, err := toolWriteFile(map[string]any{"path": p, "content": "x"}); err == nil {
		t.Fatal("запись в несуществующий каталог должна быть отвергнута")
	}
}

func TestProtectedWritePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		if !isProtectedWritePath(`C:\Windows\System32\x.dll`) {
			t.Fatal("C:\\Windows должен быть защищён")
		}
		return
	}
	for _, p := range []string{"/etc/passwd", "/usr/bin/x", "/boot/vmlinuz", "/"} {
		if !isProtectedWritePath(p) {
			t.Errorf("%s должен быть защищён", p)
		}
	}
	for _, p := range []string{"/tmp/x", "/home/u/x", "/var/log/x"} {
		if isProtectedWritePath(p) {
			t.Errorf("%s не должен быть защищён", p)
		}
	}
}

// --- конверт и политика ---

func TestParseToolEnvelope(t *testing.T) {
	text := "Пояснение модели.\n```tool\n{\"name\":\"read_file\",\"arguments\":{\"path\":\"C:\\\\Windows\\\\win.ini\"}}\n```"
	call, ok := parseToolEnvelope(text)
	if !ok {
		t.Fatal("конверт не распознан")
	}
	if call.Function.Name != "read_file" || !strings.Contains(call.Function.Arguments, "win.ini") {
		t.Fatalf("вызов: %#v", call)
	}

	if _, ok := parseToolEnvelope("```json\n{\"name\":\"evil_tool\",\"arguments\":{}}\n```"); ok {
		t.Fatal("неизвестный инструмент не должен исполняться")
	}
	if _, ok := parseToolEnvelope("```json\n{\"обычный\":\"пример\"}\n```"); ok {
		t.Fatal("обычный JSON не должен считаться вызовом")
	}
}

func TestClassifyToolLevels(t *testing.T) {
	cases := []struct {
		name string
		lvl  int
		want Verdict
	}{
		{"read_file", 0, VForbidden},
		{"read_file", 1, VReadOnly},
		{"grep_search", 1, VReadOnly},
		{"write_file", 0, VForbidden},
		{"write_file", 1, VNotAllowed},
		{"write_file", 2, VNeedsYes},
		{"write_file", 3, VNeedsYes},
		{"unknown", 3, VNotAllowed},
	}
	for _, c := range cases {
		if got := classifyTool(c.name, c.lvl); got != c.want {
			t.Errorf("classifyTool(%q, %d) = %v, ожидалось %v", c.name, c.lvl, got, c.want)
		}
	}
}

func TestToolDefsFilteredByLevel(t *testing.T) {
	if defs := toolDefsForLevel(0); len(defs) != 0 {
		t.Fatalf("на L0 инструментов быть не должно: %d", len(defs))
	}
	l1 := toolDefsForLevel(1)
	if len(l1) != 3 {
		t.Fatalf("на L1 ожидались 3 read-инструмента, получено %d", len(l1))
	}
	l2 := toolDefsForLevel(2)
	if len(l2) != 4 {
		t.Fatalf("на L2 ожидались 4 инструмента, получено %d", len(l2))
	}
}

func TestRunToolRefusesWriteAtL1(t *testing.T) {
	old := currentLevel
	defer func() { currentLevel = old }()
	currentLevel = L1Read
	res := runTool(ToolCall{Function: ToolFunction{
		Name: "write_file", Arguments: `{"path":"/tmp/x","content":"y"}`,
	}}, nil)
	if res.OK || !strings.Contains(res.Note, "уровня") {
		t.Fatalf("write_file на L1 должен быть отвергнут: %#v", res)
	}
}

// --- buildMessages: целостность tool-пары ---

func TestBuildMessagesKeepsToolPairs(t *testing.T) {
	cfg := defaultConfig()
	cfg.HistoryLimit = 3
	history := []Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "c1"}}},
		{Role: "tool", Content: "result", ToolCallID: "c1"},
		{Role: "assistant", Content: "answer"},
	}
	msgs := buildMessages(cfg, history)
	if len(msgs) > 1 && msgs[1].Role == "tool" {
		t.Fatalf("обрезка оставила role=tool без вызова: %#v", msgs)
	}
}

// --- chatStream: нативные tool_calls ---

func toolCallResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"","tool_calls":[`+
		`{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"x\"}"}}]},`+
		`"finish_reason":"tool_calls"}]}`)
}

func TestChatStreamNonStreamToolCalls(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		toolCallResponse(w)
	}))
	defer up.Close()
	cfg := defaultConfig()
	cfg.Stream = false
	p := Provider{Name: "stub", BaseURL: up.URL, APIKey: "k", Model: "m"}
	text, calls, _, err := chatStream(p, []Message{{Role: "user", Content: "hi"}}, cfg,
		[]ToolDef{{Type: "function"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if text != "" {
		t.Fatalf("текст должен быть пуст: %q", text)
	}
	if len(calls) != 1 || calls[0].Function.Name != "read_file" || calls[0].ID != "c1" {
		t.Fatalf("вызовы: %#v", calls)
	}
}

func TestChatStreamSSEToolCalls(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, piece := range []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"pa"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"x\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		} {
			_, _ = io.WriteString(w, "data: "+piece+"\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer up.Close()
	cfg := defaultConfig() // Stream = true
	p := Provider{Name: "stub", BaseURL: up.URL, APIKey: "k", Model: "m"}
	_, calls, _, err := chatStream(p, []Message{{Role: "user", Content: "hi"}}, cfg,
		[]ToolDef{{Type: "function"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("ожидался 1 вызов: %#v", calls)
	}
	if calls[0].Function.Arguments != `{"path":"x"}` {
		t.Fatalf("аргументы собраны неверно: %q", calls[0].Function.Arguments)
	}
}

var _ = json.Valid // json нужен в тестах выше через импорт пакета

// --- fallback без tools API ---

func TestSendMessageRetriesWithoutTools(t *testing.T) {
	var sawTools bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `"tools"`) {
			sawTools = true
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"this model does not support tools"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	cfg := defaultConfig()
	cfg.Stream = false
	cfg.Providers = []Provider{{Name: "stub", BaseURL: up.URL, APIKey: "k", Model: "m"}}
	text, calls, _, _, err := sendMessage(cfg, []Message{{Role: "user", Content: "hi"}},
		[]ToolDef{{Type: "function"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !sawTools {
		t.Fatal("первый запрос должен был уйти с tools")
	}
	if text != "ok" || len(calls) != 0 {
		t.Fatalf("ответ: %q, вызовы: %#v", text, calls)
	}
}

// --- runAgentTurn: полный цикл ---

func TestRunAgentTurnExecutesTool(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(p, []byte("содержимое-маркер\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": p})

	step := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step++
		if step == 1 {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"","tool_calls":[`+
				`{"id":"c1","type":"function","function":{"name":"read_file","arguments":`+jsonString(string(args))+`}}]},`+
				`"finish_reason":"tool_calls"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"готово"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()

	old := currentLevel
	currentLevel = L1Read
	defer func() { currentLevel = old }()

	cfg := defaultConfig()
	cfg.Stream = false
	cfg.Providers = []Provider{{Name: "stub", BaseURL: up.URL, APIKey: "k", Model: "m"}}

	history := []Message{{Role: "user", Content: "прочитай note.txt"}}
	history, answer, _, _, err := runAgentTurn(cfg, history, turnOptions{maxSteps: 3})
	if err != nil {
		t.Fatal(err)
	}
	if answer != "готово" {
		t.Fatalf("финальный ответ: %q", answer)
	}
	foundTool := false
	for _, m := range history {
		if m.Role == "tool" && strings.Contains(m.Content, "содержимое-маркер") {
			foundTool = true
		}
	}
	if !foundTool {
		t.Fatalf("в истории нет результата read_file: %#v", history)
	}
}

// jsonString — строка как JSON-значение (для вложения в ответ сервера).
func jsonString(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}
