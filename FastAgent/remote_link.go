// remote_link.go — гостевая сторона удалённой диагностики (этап R1).
//
// На гостевом ПК запускается один exe; код сессии берётся из ИМЕНИ файла
// (fastagent-a1b2c3.exe), поэтому на чужой клавиатуре не набирается ничего.
// Гость звонит наружу, длинно поллит команды, проверяет подпись и исполняет
// R1 (чтение: diag, read_file, grep_search, file_glob_search) и R2 (`run` —
// только после подтверждения оператора). L3 удалённо не разрешён: уровень
// гостя в этом режиме — L2, опасное и «вне белого списка» отклоняется.
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
	// R2: удалённо — не выше L2. Ниже L2 поднимаем: иначе «починка» недоступна
	// вовсе; выше L2 не поднимаем никогда (L3 остаётся физическому присутствию).
	if currentLevel < L2Fix {
		currentLevel = L2Fix
		fmt.Println("[i] Удалённый режим: уровень поднят до L2 (каждое изменение — только с y/n оператора).")
	} else if currentLevel > L2Fix {
		currentLevel = L2Fix
		fmt.Println("[i] Удалённый режим: уровень L2 — L3 удалённо не разрешён.")
	}
	fmt.Println("===================================================")
	fmt.Println("        FASTAGENT — гость удалённой диагностики (R2)")
	fmt.Println("===================================================")
	fmt.Printf("Сессия: %s | брокер: %s\n", code, base)
	fmt.Println("[i] Связь установлена. Жду команды оператора: чтение всегда,")
	fmt.Println("    run — только после подтверждения у оператора. Ctrl+C — выйти.")
	logRemoteAction("link-start", VReadOnly, true, "code="+code)

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
			logRemoteAction("cmd-rejected", VForbidden, false, "bad signature id="+cmd.ID)
			continue
		}
		start := time.Now()
		out := remoteExec(*cmd)
		res := remoteResult{
			ID:            cmd.ID,
			OK:            out.ok,
			Out:           out.out,
			Exit:          out.exit,
			MS:            time.Since(start).Milliseconds(),
			NeedsApproval: out.needsApproval,
			Verdict:       out.verdict,
		}
		if err := linkResult(base, guest, res); err != nil {
			fmt.Printf("[link] результат не отправлен: %v\n", err)
		}
	}
}

var errRemoteClosed = errors.New("сессия закрыта")

// execOutcome — итог исполнения на госте. NeedsApproval отдельным полем, а не
// «магическим» exit-кодом: консоль не должна угадывать по тексту.
type execOutcome struct {
	out           string
	exit          int
	ok            bool
	needsApproval bool
	verdict       string
}

// remoteExec — исполнение команды НА ГОСТЕ. Политика проверяется здесь, а не
// на брокере: чужая сторона не может попросить выполнить то, что гость
// запрещает. R2: изменение исполняется только при c.Approved (подписано
// оператором), опасное и «вне белого списка» на L2 отклоняется.
func remoteExec(c remoteCmd) execOutcome {
	var args map[string]any
	if len(c.Args) > 0 && string(c.Args) != "null" {
		if err := json.Unmarshal(c.Args, &args); err != nil {
			logRemoteAction(c.Kind, VForbidden, false, "битые аргументы: "+err.Error())
			return execOutcome{out: "битые аргументы: " + err.Error(), exit: -1}
		}
	}
	if c.Kind == "diag" {
		out, rc, err := runCommand(readOnlyDiag(), 90*time.Second)
		if err != nil {
			logRemoteAction("diag", VReadOnly, false, err.Error())
			return execOutcome{out: "ошибка диагностики: " + err.Error(), exit: -1}
		}
		logRemoteAction("diag", VReadOnly, true, out)
		return execOutcome{out: truncate(out, remoteMaxResultSize), exit: rc, ok: true}
	}
	if c.Kind == "run" {
		return remoteExecRun(c, args)
	}
	verdict := classifyTool(c.Kind, L1Read)
	if verdict != VReadOnly && !c.Approved {
		msg := fmt.Sprintf("R1: инструмент %q недоступен удалённо — только чтение", c.Kind)
		logRemoteAction("tool:"+c.Kind, verdict, false, msg)
		return execOutcome{out: msg, exit: -1, verdict: verdict.String()}
	}
	out, err := execTool(c.Kind, args)
	if err != nil {
		logRemoteAction("tool:"+c.Kind, verdict, c.Approved, err.Error())
		return execOutcome{out: "ошибка: " + err.Error(), exit: 1, verdict: verdict.String()}
	}
	logRemoteAction("tool:"+c.Kind, verdict, c.Approved, out)
	return execOutcome{out: truncate(out, remoteMaxResultSize), ok: true}
}

// remoteExecRun — R2: команда по политике гостя. Порядок как в локальном
// handleCommand: сначала вердикт, потом подтверждение, потом запуск и журнал.
// Вердикт считает гость; брокер и консоль на него не влияют.
func remoteExecRun(c remoteCmd, args map[string]any) execOutcome {
	cmdText, _ := args["command"].(string)
	cmdText = strings.TrimSpace(cmdText)
	if cmdText == "" {
		logRemoteAction("run", VForbidden, false, "пустая команда")
		return execOutcome{out: "пустая команда", exit: -1}
	}
	verdict := classify(cmdText, currentLevel)
	switch verdict {
	case VForbidden, VNotAllowed:
		// L3 удалённо не разрешён: опасное и «вне белого списка» — отказ, и
		// никакое approved здесь не помогает — это граница режима, а не сессии.
		msg := fmt.Sprintf("R2: команда не выполняется на госте (уровень L%d): %s", currentLevel, verdict)
		logRemoteAction(cmdText, verdict, false, "отказ: "+msg)
		return execOutcome{out: msg, exit: -1, verdict: verdict.String()}
	case VNeedsYes, VDangerous:
		if !c.Approved {
			logRemoteAction(cmdText, verdict, false, "ждёт подтверждения оператора")
			return execOutcome{
				out:           fmt.Sprintf("нужно подтверждение оператора: %s", verdict),
				exit:          -2,
				needsApproval: true,
				verdict:       verdict.String(),
			}
		}
	}
	start := time.Now()
	tmo := commandTimeout(cmdText)
	out, code, err := runCommand(cmdText, tmo)
	dur := time.Since(start)
	if err != nil && strings.TrimSpace(out) == "" {
		out = "ошибка запуска: " + err.Error()
	}
	logRemoteAction(cmdText, verdict, c.Approved, fmt.Sprintf("exit=%d %.2fs %s",
		code, dur.Seconds(), truncate(out, 1000)))
	return execOutcome{
		out:     truncate(out, remoteMaxResultSize),
		exit:    code,
		ok:      true,
		verdict: verdict.String(),
	}
}

// logRemoteAction — отдельный журнал гостя: provenance=remote виден сразу,
// как и в проектной записке (§5). Пишется и при отказе. approved приходит
// из подписанной команды, а не выводится из вердикта: иначе выполненная после
// y/n починка оставалась бы в журнале «неподтверждённой».
func logRemoteAction(cmd string, verdict Verdict, approved bool, out string) {
	if logsDir == "" {
		return // go test: пути ещё не инициализированы, файл в каталоге репозитория не нужен
	}
	entry := map[string]any{
		"ts":         time.Now().Format(time.RFC3339),
		"provenance": "remote",
		"level":      currentLevel,
		"cmd":        cmd,
		"verdict":    verdict.String(),
		"approved":   approved,
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
