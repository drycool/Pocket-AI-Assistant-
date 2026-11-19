// remote_test.go — проверки удалённой диагностики (R1–R2).
//
// Тестируется то, что легко и дорого проверить вживую: подпись (и её подделка),
// полный протокол брокера, файл-билет, граница «чтение/изменение» на госте и
// разбор строк консоли. Живой прогон «два процесса через сеть» — в FIELD-TEST.md.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	sig := remoteSign(key, "cafe", "read_file", args, 1700000000, false)
	if sig == "" || len(sig) != 64 {
		t.Fatalf("подпись не 64 hex: %q", sig)
	}
	cmd := remoteCmd{ID: "cafe", Kind: "read_file", Args: args, Sig: sig, TS: 1700000000}
	if !remoteVerify(key, cmd) {
		t.Fatal("своя же подпись не прошла проверку")
	}
	// Approved входит в подпись: иначе реле «дописало» бы подтверждение к
	// чужой команде. Флаг true с подписью от false обязан не сойтись.
	signedFalse := remoteSign(key, "cafe", "run", args, 1700000000, false)
	withApproval := remoteCmd{ID: "cafe", Kind: "run", Args: args, Sig: signedFalse, TS: 1700000000, Approved: true}
	if remoteVerify(key, withApproval) {
		t.Fatal("подпись сошлась после подмены approved")
	}
	signedTrue := remoteSign(key, "cafe", "run", args, 1700000000, true)
	withApproval.Sig = signedTrue
	if !remoteVerify(key, withApproval) {
		t.Fatal("подпись с approved=true не прошла проверку")
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
	cmd.Sig = remoteSign(key, cmd.ID, cmd.Kind, cmd.Args, cmd.TS, cmd.Approved)
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
	// Закрываем живые соединения до httptest.Close: SSE-поток и длинный поллинг
	// иначе держат сервер до клиентского таймаута (45 с).
	t.Cleanup(srv.CloseClientConnections)
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

func TestRemoteExecPolicyOnGuest(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "hosts.txt")
	if err := os.WriteFile(file, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := func(v any) json.RawMessage {
		b, _ := json.Marshal(v)
		return b
	}

	// Чтение разрешено без подтверждения.
	res := remoteExec(remoteCmd{Kind: "read_file", Args: raw(map[string]any{"path": file})})
	if !res.ok || res.exit != 0 || !strings.Contains(res.out, "localhost") {
		t.Fatalf("read_file: ok=%v rc=%d out=%q", res.ok, res.exit, res.out)
	}

	// Инструмент изменения без approved не выполняется — и не «ждёт», а
	// отклоняется: подтверждение в R2 есть только у run.
	if res := remoteExec(remoteCmd{Kind: "write_file", Args: raw(map[string]any{"path": file, "content": "x"})}); res.ok || !strings.Contains(res.out, "только чтение") {
		t.Fatalf("write_file не отклонён: ok=%v out=%q", res.ok, res.out)
	}

	// Секретный файл не читается даже как read_file.
	secret := filepath.Join(dir, "id_rsa")
	_ = os.WriteFile(secret, []byte("-----BEGIN"), 0o600)
	if res := remoteExec(remoteCmd{Kind: "read_file", Args: raw(map[string]any{"path": secret})}); res.ok || !strings.Contains(res.out, "секрет") {
		t.Fatalf("секрет прочитан через remote: ok=%v out=%q", res.ok, res.out)
	}

	// R2: run на уровне гостя L2.
	prev := currentLevel
	currentLevel = L2Fix
	t.Cleanup(func() { currentLevel = prev })

	// Чтение через run — без вопросов.
	if res := remoteExec(remoteCmd{Kind: "run", Args: raw(map[string]any{"command": "id"})}); !res.ok || strings.TrimSpace(res.out) == "" {
		t.Fatalf("run id: ok=%v out=%q", res.ok, res.out)
	}

	// Изменение — сначала «нужно подтверждение», и только так.
	need := remoteExec(remoteCmd{Kind: "run", Args: raw(map[string]any{"command": "ipconfig /flushdns"})})
	if !need.needsApproval || need.ok || need.verdict == "" {
		t.Fatalf("изменение не попросило подтверждения: %+v", need)
	}
	done := remoteExec(remoteCmd{Kind: "run", Approved: true, Args: raw(map[string]any{"command": "ipconfig /flushdns"})})
	if !done.ok || done.needsApproval {
		t.Fatalf("изменение с approved не выполнено: %+v", done)
	}

	// Опасное удалённо не выполняется даже с approved: L3 в этом режиме нет.
	for _, cmd := range []string{"rm -rf /", "format C:"} {
		res := remoteExec(remoteCmd{Kind: "run", Approved: true, Args: raw(map[string]any{"command": cmd})})
		if res.ok || !strings.Contains(res.out, "не выполняется") {
			t.Fatalf("%q выполнен удалённо: %+v", cmd, res)
		}
	}

	// Снимок диагностики собирается тем же read-only путём.
	res = remoteExec(remoteCmd{Kind: "diag"})
	if !res.ok || res.exit != 0 || strings.TrimSpace(res.out) == "" {
		t.Fatalf("diag: ok=%v rc=%d out=%q", res.ok, res.exit, res.out)
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
		{`run ipconfig /flushdns`, "run", false, map[string]string{"command": "ipconfig /flushdns"}},
		{`run "C:\Program Files\x.exe" --flag`, "run", false, map[string]string{"command": `"C:\Program Files\x.exe" --flag`}},
		{`get C:\Windows\win.ini /tmp/win.ini`, "get_file", false, map[string]string{"path": `C:\Windows\win.ini`, "local": "/tmp/win.ini"}},
		{`get "C:\Program Files\app.ini"`, "get_file", false, map[string]string{"path": `C:\Program Files\app.ini`}},
		{`put /tmp/up.bin C:\Users\Public\up.bin`, "put_file", false, map[string]string{"local": "/tmp/up.bin", "path": `C:\Users\Public\up.bin`}},
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

	for _, bad := range []string{"read", "grep", "glob", "run", "get", "put", `read "незакрытая`} {
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

// ---------------------------------------------------------------------------
// R3: передача файлов
// ---------------------------------------------------------------------------

func remoteRawArgs(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// startTestGuest — гостевая петля «поллинг → политика → результат», как в
// настоящем -link, но без сети наружу: всё идёт через httptest-брокер.
func startTestGuest(t *testing.T, base, guest string) {
	t.Helper()
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			cmd, err := linkPoll(base, guest)
			if err != nil {
				return
			}
			if cmd == nil {
				continue
			}
			out := remoteExec(*cmd)
			_ = linkResult(base, guest, remoteResult{
				ID:            cmd.ID,
				OK:            out.ok,
				Out:           out.out,
				Exit:          out.exit,
				NeedsApproval: out.needsApproval,
				Verdict:       out.verdict,
				Data:          out.data,
				Offset:        out.offset,
				Total:         out.total,
				EOF:           out.eof,
				SHA256:        out.sha,
			})
		}
	}()
}

func testPayload(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i*31 + 7)
	}
	return data
}

func TestRemoteGetFileChunks(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "data.bin")
	data := testPayload(remoteChunkSize*2 + 123)
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}

	var got []byte
	var offset int64
	for {
		res := remoteExec(remoteCmd{Kind: "get_file", Args: remoteRawArgs(map[string]any{"path": src, "offset": offset})})
		if !res.ok {
			t.Fatalf("get_file offset=%d: %s", offset, res.out)
		}
		if res.total != int64(len(data)) {
			t.Fatalf("total=%d, ждали %d", res.total, len(data))
		}
		if res.offset != offset {
			t.Fatalf("offset=%d, ждали %d", res.offset, offset)
		}
		chunk, err := base64.StdEncoding.DecodeString(res.data)
		if err != nil {
			t.Fatalf("кусок не base64: %v", err)
		}
		got = append(got, chunk...)
		offset += int64(len(chunk))
		if res.eof {
			break
		}
		if len(chunk) != remoteChunkSize {
			t.Fatalf("неполный кусок без eof: %d", len(chunk))
		}
	}
	if !bytes.Equal(got, data) {
		t.Fatal("собранный файл не совпал с исходным")
	}

	// Хеш присылается в первом куске и совпадает с sha256 файла.
	first := remoteExec(remoteCmd{Kind: "get_file", Args: remoteRawArgs(map[string]any{"path": src})})
	sum := sha256.Sum256(data)
	if first.sha != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 в первом куске: %q", first.sha)
	}

	// offset за концом файла — отказ, а не пустой ответ.
	if res := remoteExec(remoteCmd{Kind: "get_file", Args: remoteRawArgs(map[string]any{"path": src, "offset": int64(len(data)) + 1})}); res.ok {
		t.Fatal("offset за концом файла принят")
	}
	// Каталог не передаётся.
	if res := remoteExec(remoteCmd{Kind: "get_file", Args: remoteRawArgs(map[string]any{"path": dir})}); res.ok {
		t.Fatal("каталог отдан как файл")
	}
	// Секрет не передаётся.
	secret := filepath.Join(dir, "id_rsa")
	if err := os.WriteFile(secret, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := remoteExec(remoteCmd{Kind: "get_file", Args: remoteRawArgs(map[string]any{"path": secret})}); res.ok {
		t.Fatal("секрет передан удалённо")
	}
	// Файл больше лимита — отказ по Stat, без чтения.
	big := filepath.Join(dir, "big.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(remoteMaxFileSize + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if res := remoteExec(remoteCmd{Kind: "get_file", Args: remoteRawArgs(map[string]any{"path": big})}); res.ok {
		t.Fatal("файл больше лимита передан")
	}
}

func TestRemotePutFileApprovalAndChecks(t *testing.T) {
	dir := t.TempDir()
	data := testPayload(remoteChunkSize + 42)
	sum := sha256.Sum256(data)

	prev := currentLevel
	currentLevel = L2Fix
	t.Cleanup(func() { currentLevel = prev })

	target := filepath.Join(dir, "incoming.bin")
	args := func(off int64, approved bool) remoteCmd {
		chunk := data[off:]
		if int64(len(chunk)) > remoteChunkSize {
			chunk = chunk[:remoteChunkSize]
		}
		return remoteCmd{
			Kind:     "put_file",
			Approved: approved,
			Args: remoteRawArgs(map[string]any{
				"path":   target,
				"offset": off,
				"total":  len(data),
				"data":   base64.StdEncoding.EncodeToString(chunk),
				"final":  off+int64(len(chunk)) == int64(len(data)),
				"sha256": hex.EncodeToString(sum[:]),
			}),
		}
	}

	// Без approved не пишется ничего, даже временного файла.
	if res := remoteExec(args(0, false)); !res.needsApproval || res.ok {
		t.Fatalf("без approved: needsApproval=%v ok=%v", res.needsApproval, res.ok)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("файл создан без подтверждения")
	}
	if _, err := os.Stat(target + ".part-fastagent"); err == nil {
		t.Fatal("временный файл создан без подтверждения")
	}

	// С approved куски пишутся, в конце файл появляется под настоящим именем.
	var offset int64
	for {
		res := remoteExec(args(offset, true))
		if !res.ok {
			t.Fatalf("put approved offset=%d: %s", offset, res.out)
		}
		chunkLen := int64(len(data)) - offset
		if chunkLen > remoteChunkSize {
			chunkLen = remoteChunkSize
		}
		offset += chunkLen
		if offset >= int64(len(data)) {
			break
		}
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("файл не записан: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("записанный файл не совпал")
	}

	// Неверный sha256 — файл не появляется под настоящим именем, .part удалён.
	target2 := filepath.Join(dir, "bad.bin")
	badArgs := map[string]any{
		"path": target2, "offset": 0, "total": 3,
		"data":  base64.StdEncoding.EncodeToString([]byte("abc")),
		"final": true, "sha256": strings.Repeat("0", 64),
	}
	if res := remoteExec(remoteCmd{Kind: "put_file", Approved: true, Args: remoteRawArgs(badArgs)}); res.ok {
		t.Fatal("файл записан с неверным sha256")
	}
	if _, err := os.Stat(target2); err == nil {
		t.Fatal("файл с неверным хешем остался под настоящим именем")
	}

	// Системный каталог и секреты — отказ даже с approved.
	for _, p := range []string{"/etc/fastagent-test", filepath.Join(dir, "id_rsa")} {
		res := remoteExec(remoteCmd{Kind: "put_file", Approved: true, Args: remoteRawArgs(map[string]any{
			"path": p, "offset": 0, "total": 1, "data": "eA==", "final": true, "sha256": "x",
		})})
		if res.ok {
			t.Fatalf("%s: запись разрешена", p)
		}
	}

	// Объявленный размер больше лимита — отказ до записи.
	bigArgs := map[string]any{
		"path": filepath.Join(dir, "big2.bin"), "offset": 0,
		"total": remoteMaxFileSize + 1, "data": "", "final": true, "sha256": "",
	}
	if res := remoteExec(remoteCmd{Kind: "put_file", Approved: true, Args: remoteRawArgs(bigArgs)}); res.ok {
		t.Fatal("принят объявленный размер больше лимита")
	}
}

func TestRemoteFileTransferThroughBroker(t *testing.T) {
	_, srv := newTestBroker(t, "s3cret", "")
	code, _, err := remoteCreateSession(srv.URL, "s3cret")
	if err != nil {
		t.Fatalf("сессия: %v", err)
	}
	// Закрываем живые соединения до httptest.Close: SSE-поток и длинный поллинг
	// иначе держат сервер до клиентского таймаута (45 с).
	t.Cleanup(srv.CloseClientConnections)
	guest, err := linkPair(srv.URL, code)
	if err != nil {
		t.Fatalf("пара: %v", err)
	}
	key := remoteDeriveKey(code, guest)

	prev := currentLevel
	currentLevel = L2Fix
	t.Cleanup(func() { currentLevel = prev })

	events := make(chan remoteEvent, 16)
	go remoteListenEvents(srv.URL, code, "s3cret", events)
	startTestGuest(t, srv.URL, guest)

	dir := t.TempDir()
	data := testPayload(remoteChunkSize*2 + 17)
	src := filepath.Join(dir, "источник.bin")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// get: файл реально проходит брокер кусками и сверяется по sha256.
	dst := filepath.Join(dir, "копия.bin")
	if err := remoteGetFile(srv.URL, "s3cret", code, key, src, dst, events); err != nil {
		t.Fatalf("get: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("копия не создана: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("полученная копия не совпала")
	}

	// put: подтверждение y в «терминале оператора», файл у «гостя» появляется.
	remoteDst := filepath.Join(dir, "наверх.bin")
	reader := bufio.NewReader(strings.NewReader("y\n"))
	res, ok := remotePutFile(reader, srv.URL, "s3cret", code, key, src, remoteDst, events)
	if !ok || !res.OK {
		t.Fatalf("put: ok=%v res=%+v", ok, res)
	}
	up, err := os.ReadFile(remoteDst)
	if err != nil {
		t.Fatalf("файл не записан на госте: %v", err)
	}
	if !bytes.Equal(up, data) {
		t.Fatal("записанный на госте файл не совпал")
	}

	// Отказ оператора — файла нет (и это не ошибка транспорта).
	refuseDst := filepath.Join(dir, "отказ.bin")
	res, ok = remotePutFile(bufio.NewReader(strings.NewReader("n\n")), srv.URL, "s3cret", code, key, src, refuseDst, events)
	if !ok || res.OK {
		t.Fatalf("put без подтверждения: ok=%v res=%+v", ok, res)
	}
	if _, err := os.Stat(refuseDst); err == nil {
		t.Fatal("файл записан после отказа оператора")
	}
}

// Файл-билет должен быть Windows-программой: ELF под именем .exe клиент не
// запустит («невозможно запустить это приложение на вашем ПК», 0xc0000033).
// Дефект 19.11.2026: remote.sh передавал брокеру linux-бинарник оператора.
func TestRemoteTicketFileSkipsNonPE(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)

	// Имя не «fastagent»: этот же каталог служит XDG-кэшем, а внутри него
	// remoteTicketFile создаёт подкаталог fastagent/.
	elf := filepath.Join(dir, "operator-bin")
	if err := os.WriteFile(elf, []byte("\x7fELF\x02\x01\x01\x00 linux-arm64"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{}
	cfg.Remote.TicketExe = elf
	if _, err := remoteTicketFile(cfg, "abc123"); err == nil {
		t.Fatal("ELF принят файлом-билетом: клиент получил бы «невозможно запустить»")
	} else if !strings.Contains(err.Error(), "не PE") {
		t.Fatalf("ошибка не объясняет причину: %v", err)
	}
}

func TestRemoteTicketFileWritesPE(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)

	pe := filepath.Join(dir, "fastagent.exe")
	body := []byte("MZ\x90\x00 fake-windows-body")
	if err := os.WriteFile(pe, body, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{}
	cfg.Remote.TicketExe = pe
	dst, err := remoteTicketFile(cfg, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dst) != "fastagent-abc123.exe" {
		t.Fatalf("имя билета не по коду сессии: %s", dst)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatal("содержимое билета не совпало с exe")
	}
}
