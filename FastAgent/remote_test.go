// remote_test.go — проверки удалённой диагностики (этап R1).
//
// Тестируется то, что легко и дорого проверить вживую: подпись (и её подделка),
// полный протокол брокера, файл-билет, отказ на всё, кроме чтения, и разбор
// строк консоли. Живой прогон «два процесса через сеть» — в FIELD-TEST.md.
package main

import (
	"bufio"
	"bytes"
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

func newTestBroker(t *testing.T, secret, ticket string) (*remoteBroker, *httptest.Server) {
	t.Helper()
	b := newRemoteBroker(secret, ticket)
	srv := httptest.NewServer(b.handler())
	t.Cleanup(srv.Close)
	return b, srv
}

func TestRemoteSignRoundTrip(t *testing.T) {
	key := remoteDeriveKey("a1b2c3", "deadbeef")
	args := json.RawMessage(`{"path":"C:\\Windows\\win.ini"}`)
	sig := remoteSign(key, "cafe", "read_file", args, 1700000000)
	if sig == "" || len(sig) != 64 {
		t.Fatalf("подпись не 64 hex: %q", sig)
	}
	cmd := remoteCmd{ID: "cafe", Kind: "read_file", Args: args, Sig: sig, TS: 1700000000}
	if !remoteVerify(key, cmd) {
		t.Fatal("своя же подпись не прошла проверку")
	}
	// Подмена аргумента — подпись обязана не сойтись.
	tampered := cmd
	tampered.Args = json.RawMessage(`{"path":"C:\\Windows\\System32\\config\\SAM"}`)
	if remoteVerify(key, tampered) {
		t.Fatal("подпись сошлась на подменённых аргументах")
	}
	// Другой код сессии — другой ключ.
	if remoteVerify(remoteDeriveKey("ffffff", "deadbeef"), cmd) {
		t.Fatal("ключ чужой сессии принял команду")
	}
	// Другой guest-токен — тоже другой ключ.
	if remoteVerify(remoteDeriveKey("a1b2c3", "cafebabe"), cmd) {
		t.Fatal("ключ с другим guest-токеном принял команду")
	}
}

func TestRemoteCodeFromExe(t *testing.T) {
	cases := map[string]string{
		`C:\Users\Public\fastagent-a1b2c3.exe`: "a1b2c3",
		"/tmp/fastagent-012f9a":                "012f9a",
		"fastagent_ABCDEF.exe":                 "abcdef",
		"fastagent.exe":                        "",
		"agent.exe":                            "",
		"":                                     "",
	}
	for name, want := range cases {
		if got := remoteCodeFromExe(name); got != want {
			t.Errorf("remoteCodeFromExe(%q) = %q, ждали %q", name, got, want)
		}
	}
	if !validRemoteCode("a1b2c3") {
		t.Error("a1b2c3 должен быть валидным кодом")
	}
	for _, bad := range []string{"", "a1b2c", "a1b2c3d", "a1b2c3g", "A1B2C3"} {
		if validRemoteCode(bad) {
			t.Errorf("%q принят как валидный код", bad)
		}
	}
}

func TestBrokerSessionLifecycle(t *testing.T) {
	_, srv := newTestBroker(t, "s3cret", "")
	base := srv.URL

	// Без секрета операторских ручек нет.
	var e struct {
		Error string `json:"error"`
	}
	errReq := func() int {
		req, _ := http.NewRequest(http.MethodPost, base+"/fa/session", strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("session без секрета: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if code := errReq(); code != http.StatusForbidden {
		t.Fatalf("создание сессии без секрета: HTTP %d, ждали 403", code)
	}

	code, opToken, err := remoteCreateSession(base, "s3cret")
	if err != nil || code == "" || opToken == "" {
		t.Fatalf("сессия: code=%q op=%q err=%v", code, opToken, err)
	}

	// Пара по чужому коду не проходит.
	if _, err := linkPair(base, "000000"); err == nil {
		t.Fatal("пара по несуществующему коду прошла")
	}

	guest, err := linkPair(base, code)
	if err != nil || guest == "" {
		t.Fatalf("пара: %v", err)
	}
	// Повторная пара запрещена: одна сессия — один гость.
	if _, err := linkPair(base, code); err == nil {
		t.Fatal("повторная пара прошла")
	}

	// До подключения оператор не должен ставить команды? Он может, но 202
	// разрешён только после пары — проверим после закрытия отдельным тестом.
	key := remoteDeriveKey(code, guest)
	args := json.RawMessage(`{"path":"/etc/hostname"}`)
	cmd := remoteCmd{ID: "beef", Kind: "read_file", Args: args, TS: time.Now().Unix()}
	cmd.Sig = remoteSign(key, cmd.ID, cmd.Kind, cmd.Args, cmd.TS)
	body, _ := json.Marshal(map[string]any{"code": code, "cmd": cmd})
	if err := remoteJSON(http.MethodPost, base+"/fa/cmd", "s3cret", body, nil); err != nil {
		t.Fatalf("команда не поставлена: %v", err)
	}

	got, err := linkPoll(base, guest)
	if err != nil || got == nil {
		t.Fatalf("поллинг: cmd=%v err=%v", got, err)
	}
	if got.ID != cmd.ID || got.Kind != "read_file" {
		t.Fatalf("брокер отдал не ту команду: %+v", got)
	}
	if !remoteVerify(key, *got) {
		t.Fatal("подпись команды, прошедшей через брокера, не сходится")
	}

	// 204 на пустой очереди: это норма, а не ошибка.
	req, _ := http.NewRequest(http.MethodGet, base+"/fa/poll?wait=0", nil)
	req.Header.Set("Authorization", "Bearer "+guest)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("поллинг пустой: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("пустой поллинг: HTTP %d, ждали 204", resp.StatusCode)
	}

	// Гость присылает результат.
	if err := linkResult(base, guest, remoteResult{ID: cmd.ID, OK: true, Out: "gpu2", Exit: 0, MS: 3}); err != nil {
		t.Fatalf("результат не принят: %v", err)
	}

	// Статус для оператора.
	req, _ = http.NewRequest(http.MethodGet, base+"/fa/status?code="+code, nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("статус: %v", err)
	}
	var st struct {
		Paired  bool `json:"paired"`
		Pending int  `json:"pending"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if !st.Paired || st.Pending != 0 {
		t.Fatalf("статус после пары и поллинга: %+v", st)
	}

	// Гость не может выдать себя за оператора: команда без секрета — 403.
	body, _ = json.Marshal(map[string]any{"code": code, "cmd": cmd})
	req, _ = http.NewRequest(http.MethodPost, base+"/fa/cmd", bytes.NewReader(body))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cmd без секрета: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cmd без секрета: HTTP %d, ждали 403", resp.StatusCode)
	}
	_ = e
}

func TestBrokerSSEEvents(t *testing.T) {
	_, srv := newTestBroker(t, "s3cret", "")
	code, _, err := remoteCreateSession(srv.URL, "s3cret")
	if err != nil {
		t.Fatalf("сессия: %v", err)
	}
	guest, err := linkPair(srv.URL, code)
	if err != nil {
		t.Fatalf("пара: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/fa/events?code="+code, nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE: %v", err)
	}
	defer resp.Body.Close()

	lines := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data: ") {
				lines <- sc.Text()
			}
		}
		close(lines)
	}()

	// Результат после подписки обязан прийти событием.
	if err := linkResult(srv.URL, guest, remoteResult{ID: "x1", OK: true, Out: "ok"}); err != nil {
		t.Fatalf("результат: %v", err)
	}
	// Первым может прийти отложенное paired (канал буферизован) — ждём result.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("SSE закрылся раньше результата")
			}
			var ev remoteEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				t.Fatalf("событие не JSON: %v (%s)", err, line)
			}
			if ev.Type == "paired" {
				continue
			}
			if ev.Type != "result" || ev.Result == nil || ev.Result.ID != "x1" {
				t.Fatalf("не то событие: %+v", ev)
			}
			return
		case <-deadline:
			t.Fatal("SSE не отдал result за 5 с")
		}
	}
}

func TestBrokerTicket(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "fastagent-test.exe")
	if err := os.WriteFile(exe, []byte("MZ-fake-binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, srv := newTestBroker(t, "", exe)
	code, _, err := remoteCreateSession(srv.URL, "")
	if err != nil {
		t.Fatalf("сессия: %v", err)
	}
	resp, err := http.Get(srv.URL + "/fa/ticket/" + code + ".exe")
	if err != nil {
		t.Fatalf("билет: %v", err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(data) != "MZ-fake-binary" {
		t.Fatalf("билет: HTTP %d, тело %q", resp.StatusCode, data)
	}
	if resp.Header.Get("Content-Disposition") == "" {
		t.Error("нет Content-Disposition: имя файла с кодом не подсказано")
	}
	// Чужой код — 404: билет только для живой сессии.
	resp, err = http.Get(srv.URL + "/fa/ticket/aaaaaa.exe")
	if err != nil {
		t.Fatalf("билет чужого кода: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("билет чужого кода: HTTP %d, ждали 404", resp.StatusCode)
	}

	// Брокер без настроенного exe честно говорит, что билета нет.
	_, srv2 := newTestBroker(t, "", "")
	code2, _, err := remoteCreateSession(srv2.URL, "")
	if err != nil {
		t.Fatalf("сессия 2: %v", err)
	}
	resp, err = http.Get(srv2.URL + "/fa/ticket/" + code2 + ".exe")
	if err != nil {
		t.Fatalf("билет без exe: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("билет без exe: HTTP %d, ждали 404", resp.StatusCode)
	}
}

func TestBrokerExpiryAndClose(t *testing.T) {
	oldIdle := remoteIdleTimeout
	remoteIdleTimeout = 30 * time.Millisecond
	defer func() { remoteIdleTimeout = oldIdle }()

	_, srv := newTestBroker(t, "", "")
	code, _, err := remoteCreateSession(srv.URL, "")
	if err != nil {
		t.Fatalf("сессия: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if _, err := linkPair(srv.URL, code); err == nil {
		t.Fatal("просроченная сессия приняла пару")
	}

	remoteIdleTimeout = oldIdle
	code, _, err = remoteCreateSession(srv.URL, "")
	if err != nil {
		t.Fatalf("сессия 2: %v", err)
	}
	guest, err := linkPair(srv.URL, code)
	if err != nil {
		t.Fatalf("пара 2: %v", err)
	}
	remoteCloseSession(srv.URL, "", code)
	// Закрытая сессия: поллинг говорит «gone», а не висит.
	if _, err := linkPoll(srv.URL, guest); err == nil {
		t.Fatal("закрытая сессия продолжает отдавать команды")
	}
}

func TestRemoteExecOnlyRead(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "hosts.txt")
	if err := os.WriteFile(file, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := func(v any) json.RawMessage {
		b, _ := json.Marshal(v)
		return b
	}

	// Чтение разрешено.
	out, rc, ok := remoteExec(remoteCmd{Kind: "read_file", Args: raw(map[string]any{"path": file})})
	if !ok || rc != 0 || !strings.Contains(out, "localhost") {
		t.Fatalf("read_file: ok=%v rc=%d out=%q", ok, rc, out)
	}

	// Запись запрещена: R1 — только чтение, и отказ приходит от политики.
	if out, _, ok := remoteExec(remoteCmd{Kind: "write_file", Args: raw(map[string]any{"path": file, "content": "x"})}); ok || !strings.Contains(out, "только чтение") {
		t.Fatalf("write_file не отклонён: ok=%v out=%q", ok, out)
	}

	// Произвольная команда тоже запрещена.
	if out, _, ok := remoteExec(remoteCmd{Kind: "run", Args: raw(map[string]any{"command": "id"})}); ok || !strings.Contains(out, "только чтение") {
		t.Fatalf("run не отклонён: ok=%v out=%q", ok, out)
	}

	// Секретный файл не читается даже как read_file.
	secret := filepath.Join(dir, "id_rsa")
	_ = os.WriteFile(secret, []byte("-----BEGIN"), 0o600)
	if out, _, ok := remoteExec(remoteCmd{Kind: "read_file", Args: raw(map[string]any{"path": secret})}); ok || !strings.Contains(out, "секрет") {
		t.Fatalf("секрет прочитан через remote: ok=%v out=%q", ok, out)
	}

	// Снимок диагностики собирается тем же read-only путём.
	out, rc, ok = remoteExec(remoteCmd{Kind: "diag"})
	if !ok || rc != 0 || strings.TrimSpace(out) == "" {
		t.Fatalf("diag: ok=%v rc=%d out=%q", ok, rc, out)
	}
}

func TestParseRemoteCommand(t *testing.T) {
	cases := []struct {
		line string
		kind string
		quit bool
		want map[string]string
	}{
		{"diag", "diag", false, nil},
		{`read C:\Windows\win.ini`, "read_file", false, map[string]string{"path": `C:\Windows\win.ini`}},
		{`read "C:\Program Files\app.ini"`, "read_file", false, map[string]string{"path": `C:\Program Files\app.ini`}},
		{`grep ошибка C:\Windows *.log`, "grep_search", false, map[string]string{"pattern": "ошибка", "path": `C:\Windows`, "glob": "*.log"}},
		{`glob *.ini C:\`, "file_glob_search", false, map[string]string{"pattern": "*.ini", "path": `C:\`}},
		{"help", "", false, nil},
		{"quit", "close", true, nil},
	}
	for _, c := range cases {
		kind, args, quit, err := parseRemoteCommand(c.line)
		if err != nil {
			t.Errorf("%q: %v", c.line, err)
			continue
		}
		if kind != c.kind || quit != c.quit {
			t.Errorf("%q: kind=%q quit=%v, ждали %q/%v", c.line, kind, quit, c.kind, c.quit)
		}
		if c.want != nil {
			var got map[string]any
			if err := json.Unmarshal(args, &got); err != nil {
				t.Errorf("%q: аргументы не JSON: %v", c.line, err)
				continue
			}
			for k, v := range c.want {
				if s, _ := got[k].(string); s != v {
					t.Errorf("%q: %s=%q, ждали %q", c.line, k, got[k], v)
				}
			}
		}
	}

	for _, bad := range []string{"read", "grep", "glob", "run dir", `read "незакрытая`} {
		if _, _, _, err := parseRemoteCommand(bad); err == nil {
			t.Errorf("%q: ожидалась ошибка", bad)
		}
	}
}

func TestSplitQuoted(t *testing.T) {
	got, err := splitQuoted(`a "b c" d`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b c", "d"}
	if len(got) != len(want) {
		t.Fatalf("splitQuoted = %v, ждали %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("splitQuoted = %v, ждали %v", got, want)
		}
	}
	if _, err := splitQuoted(`"abc`); err == nil {
		t.Fatal("незакрытая кавычка не замечена")
	}
}
