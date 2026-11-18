// remote_link.go — гостевая сторона удалённой диагностики (этап R1).
//
// На гостевом ПК запускается один exe; код сессии берётся из ИМЕНИ файла
// (fastagent-a1b2c3.exe), поэтому на чужой клавиатуре не набирается ничего.
// Гость звонит наружу, длинно поллит команды, проверяет подпись и исполняет
// ТОЛЬКО чтение: diag, read_file, grep_search, file_glob_search.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var remoteClient = &http.Client{Timeout: 45 * time.Second}

var reRemoteTicket = regexp.MustCompile(`(?i)fastagent[-_]([0-9a-f]{6})`)

// remoteCodeFromExe — код из имени файла; пусто, если не нашёлся.
func remoteCodeFromExe(argv0 string) string {
	base := filepath.Base(argv0)
	if m := reRemoteTicket.FindStringSubmatch(base); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

func validRemoteCode(code string) bool {
	if len(code) != remoteCodeLen {
		return false
	}
	for _, r := range code {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// runLink — цикл гостя. Работает до Ctrl+C, закрытия сессии или потери сети
// (в последнем случае — с нарастающей паузой, а не молчаливым выходом).
func runLink(cfg Config, brokerURL string) int {
	code := remoteCodeFromExe(os.Args[0])
	if code == "" {
		fmt.Print("Код сессии (6 hex из имени файла fastagent-XXXXXX.exe): ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		code = strings.ToLower(strings.TrimSpace(line))
	}
	if !validRemoteCode(code) {
		fmt.Printf("[ERROR] «%s» не похоже на код сессии (6 hex).\n", code)
		return 2
	}
	base := strings.TrimRight(firstNonEmpty(brokerURL, cfg.Remote.BrokerURL, "https://pi5.tail823290.ts.net/fa"), "/")

	guest, err := linkPair(base, code)
	if err != nil {
		fmt.Printf("[ERROR] пара не удалась: %v\n", err)
		return 1
	}
	key := remoteDeriveKey(code, guest)
	fmt.Println("===================================================")
	fmt.Println("        FASTAGENT — гость удалённой диагностики (R1)")
	fmt.Println("===================================================")
	fmt.Printf("Сессия: %s | брокер: %s\n", code, base)
	fmt.Println("[i] Связь установлена. Жду команды оператора: только чтение")
	fmt.Println("    (diag, read_file, grep_search, file_glob_search). Ctrl+C — выйти.")
	logRemoteAction("link-start", VReadOnly, "code="+code)

	backoff := time.Second
	for {
		cmd, perr := linkPoll(base, guest)
		if perr != nil {
			if errors.Is(perr, errRemoteClosed) {
				fmt.Println("[link] оператор закрыл сессию — выхожу.")
				return 0
			}
			fmt.Printf("[link] связь: %v (повтор через %s)\n", perr, backoff)
			time.Sleep(backoff)
			if backoff < 10*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		if cmd == nil {
			continue // 204: команд пока нет, это норма
		}
		if !remoteVerify(key, *cmd) {
			// Подпись не сходится — значит это не та консоль или команду
			// подделали по дороге. Не исполняем и не отвечаем содержимым.
			fmt.Printf("[link] ОТКАЗ: подпись команды %s не сходится\n", cmd.ID)
			logRemoteAction("cmd-rejected", VForbidden, "bad signature id="+cmd.ID)
			continue
		}
		start := time.Now()
		out, rc, ok := remoteExec(*cmd)
		res := remoteResult{ID: cmd.ID, OK: ok, Out: out, Exit: rc, MS: time.Since(start).Milliseconds()}
		if err := linkResult(base, guest, res); err != nil {
			fmt.Printf("[link] результат не отправлен: %v\n", err)
		}
	}
}

var errRemoteClosed = errors.New("сессия закрыта")

// remoteExec — исполнение команды НА ГОСТЕ. Политика проверяется здесь, а не
// на брокере: чужая сторона не может попросить выполнить то, что R1 запрещает.
func remoteExec(c remoteCmd) (string, int, bool) {
	var args map[string]any
	if len(c.Args) > 0 && string(c.Args) != "null" {
		if err := json.Unmarshal(c.Args, &args); err != nil {
			logRemoteAction(c.Kind, VForbidden, "битые аргументы: "+err.Error())
			return "битые аргументы: " + err.Error(), -1, false
		}
	}
	if c.Kind == "diag" {
		out, rc, err := runCommand(readOnlyDiag(), 90*time.Second)
		if err != nil {
			logRemoteAction("diag", VReadOnly, err.Error())
			return "ошибка диагностики: " + err.Error(), -1, false
		}
		logRemoteAction("diag", VReadOnly, out)
		return truncate(out, remoteMaxResultSize), rc, true
	}
	verdict := classifyTool(c.Kind, L1Read)
	if verdict != VReadOnly {
		msg := fmt.Sprintf("R1: инструмент %q недоступен удалённо — только чтение", c.Kind)
		logRemoteAction("tool:"+c.Kind, verdict, msg)
		return msg, -1, false
	}
	out, err := execTool(c.Kind, args)
	if err != nil {
		logRemoteAction("tool:"+c.Kind, verdict, err.Error())
		return "ошибка: " + err.Error(), 1, false
	}
	logRemoteAction("tool:"+c.Kind, verdict, out)
	return truncate(out, remoteMaxResultSize), 0, true
}

// logRemoteAction — отдельный журнал гостя: provenance=remote виден сразу,
// как и в проектной записке (§5). Пишется и при отказе.
func logRemoteAction(cmd string, verdict Verdict, out string) {
	if logsDir == "" {
		return // go test: пути ещё не инициализированы, файл в каталоге репозитория не нужен
	}
	entry := map[string]any{
		"ts":         time.Now().Format(time.RFC3339),
		"provenance": "remote",
		"level":      currentLevel,
		"cmd":        cmd,
		"verdict":    verdict.String(),
		"approved":   verdict == VReadOnly, // R1: подтверждений нет, только чтение
		"output":     truncate(out, 1000),
		"host":       hostname(),
	}
	line, _ := json.Marshal(entry)
	f, err := os.OpenFile(remoteLogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

func remoteLogPath() string {
	name := "actions_remote_" + time.Now().Format("2006-01-02") + ".log"
	if logsDir == "" {
		return name
	}
	return filepath.Join(logsDir, name)
}

// ---------------------------------------------------------------------------
// HTTP-клиент гостя
// ---------------------------------------------------------------------------

func linkPair(base, code string) (string, error) {
	body, _ := json.Marshal(map[string]string{"code": code})
	var out struct {
		GuestToken string `json:"guest_token"`
		Error      string `json:"error"`
	}
	if err := remoteJSON(http.MethodPost, base+"/fa/pair", "", body, &out); err != nil {
		return "", err
	}
	if out.GuestToken == "" {
		return "", fmt.Errorf("брокер не выдал guest-токен: %s", out.Error)
	}
	return out.GuestToken, nil
}

func linkPoll(base, guest string) (*remoteCmd, error) {
	req, err := http.NewRequest(http.MethodGet, base+"/fa/poll?wait=25", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+guest)
	resp, err := remoteClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var cmd remoteCmd
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&cmd); err != nil {
			return nil, err
		}
		return &cmd, nil
	case http.StatusNoContent:
		return nil, nil
	case http.StatusGone, http.StatusUnauthorized:
		return nil, errRemoteClosed
	default:
		return nil, fmt.Errorf("поллинг: HTTP %d", resp.StatusCode)
	}
}

func linkResult(base, guest string, res remoteResult) error {
	body, _ := json.Marshal(res)
	return remoteJSON(http.MethodPost, base+"/fa/result", guest, body, nil)
}

// remoteJSON — общий помощник: тело, токен, разбор ответа и HTTP-ошибки.
func remoteJSON(method, url, token string, body []byte, out any) error {
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := remoteClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(raw))
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, e.Error)
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}
