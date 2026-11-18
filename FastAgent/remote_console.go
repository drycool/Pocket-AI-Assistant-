// remote_console.go — консоль оператора удалённой диагностики (R1–R2).
//
// Оператор запускает на pi5 `fastagent -remote new`: брокер создаёт пару
// «код → сессия», рядом кладётся файл-билет fastagent-<код>.exe, который
// перетаскивается гостю через AnyDesk. Дальше оператор печатает команды в
// СВОЁМ терминале, а гость их исполняет и присылает вывод.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// consoleHelp — что можно в консоли R1. Только чтение: run/write_file здесь
// нет намеренно, их добавление — этап R2.
const consoleHelp = `Команды консоли (R1–R2):
  diag                                  снимок системы гостя
  read <путь>                           прочитать файл
  grep <шаблон> [каталог] [glob]        поиск по содержимому
  glob <шаблон> [каталог]               поиск файлов по имени
  run <команда>                         выполнить команду на госте (R2)
  status | help | quit                  состояние / справка / выход

run выполняет команду по политике ГОСТЯ. Изменяющая команда сначала спросит
подтверждение ЗДЕСЬ, в вашем терминале, и только потом уйдёт на гость с
подписанным approved=true. Опасное и «вне белого списка» удалённо не
выполняется: L3 в удалённом режиме не разрешён.`

// runRemote — вся операторская часть: сессия, файл-билет, ожидание гостя,
// затем REPL или одна команда (-remote-cmd).
func runRemote(cfg Config, brokerURL, secret, oneCmd string) int {
	base := strings.TrimRight(firstNonEmpty(brokerURL, cfg.Remote.BrokerURL, "http://127.0.0.1:8866"), "/")
	secret = firstNonEmpty(secret, cfg.Remote.Secret)
	code, _, err := remoteCreateSession(base, secret)
	if err != nil {
		fmt.Printf("[ERROR] сессия не создана: %v\n", err)
		return 1
	}
	fmt.Println("===================================================")
	fmt.Println("        FASTAGENT — консоль удалённой диагностики (R2)")
	fmt.Println("===================================================")
	fmt.Printf("Сессия: %s | брокер: %s\n", code, base)
	if path, terr := remoteTicketFile(cfg, code); terr == nil {
		fmt.Printf("Файл-билет: %s\n", path)
		fmt.Println("Перетащите его в окно AnyDesk; гость дважды щёлкнет — и подключится.")
	} else {
		fmt.Printf("[!] Файл-билет не подготовлен: %v\n", terr)
		fmt.Println("    Тогда гость запускается со своего exe и вводит код вручную: " + code)
	}

	events := make(chan remoteEvent, 64)
	go remoteListenEvents(base, code, secret, events)

	// Ждём пару: до этого подписывать команды нечем (ключа ещё нет).
	fmt.Println("[i] Жду подключения гостя (окно 15 минут, Ctrl+C — отмена)…")
	key, ok := remoteWaitPair(events, code, remotePairWindow)
	if !ok {
		fmt.Println("[!] Гость не подключился.")
		return 1
	}
	fmt.Println("[OK] Гость на связи. " + strings.SplitN(consoleHelp, "\n", 2)[0])

	if strings.TrimSpace(oneCmd) != "" {
		return remoteRunOne(base, secret, code, key, oneCmd, events)
	}
	return remoteREPL(base, secret, code, key, events)
}

// remoteWaitPair — ждём событие paired и выводим ключ из кода и guest-токена.
// Параллельно обрабатываем closed, иначе ожидание зависнет на закрытой сессии.
func remoteWaitPair(events <-chan remoteEvent, code string, timeout time.Duration) ([]byte, bool) {
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-events:
			switch ev.Type {
			case "paired":
				fmt.Println("[link] гость подключён, ключ сессии выведен.")
				return remoteDeriveKey(code, ev.GuestToken), true
			case "closed":
				return nil, false
			}
		case <-deadline:
			return nil, false
		}
	}
}

// remoteWaitResult — ждём результат конкретной команды; чужие результаты
// печатаем и пропускаем. Таймаут свой на команду: sfc/dism идут десятки минут
// (commandTimeout на госте поднимает их до 45), поэтому run ждёт дольше.
func remoteWaitResult(events <-chan remoteEvent, id string, timeout time.Duration) (remoteResult, bool) {
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-events:
			switch ev.Type {
			case "result":
				if ev.Result == nil {
					continue
				}
				if id != "" && ev.Result.ID != id {
					fmt.Printf("[i] результат чужой команды %s (пропущен)\n", ev.Result.ID)
					continue
				}
				return *ev.Result, true
			case "closed":
				fmt.Println("[link] сессия закрыта гостем.")
				return remoteResult{}, false
			}
		case <-deadline:
			fmt.Printf("[!] Гость не ответил за %s (команда могла ещё выполняться).\n", timeout)
			return remoteResult{}, false
		}
	}
}

func remotePrintResult(res remoteResult) {
	if res.OK {
		// Код возврата показываем всегда: «выполнено» и «выполнено успешно» —
		// разные вещи, и оператор не должен догадываться.
		fmt.Printf("\n── результат (%.2f с, exit=%d) ──\n%s\n",
			float64(res.MS)/1000, res.Exit, strings.TrimRight(res.Out, "\n"))
		return
	}
	fmt.Printf("\n── ОТКАЗ/ОШИБКА (exit=%d) ──\n%s\n", res.Exit, strings.TrimRight(res.Out, "\n"))
}

// remoteSendCmd — подписать у себя и поставить в очередь брокера. Возвращает
// id: результат надо сопоставлять именно со своей командой, а не «первый
// пришедший» (в SSE могут прийти и чужие, и отложенные события).
func remoteSendCmd(base, secret, code string, key []byte, kind string, args json.RawMessage, approved bool) (string, error) {
	id := randHex(4)
	cmd := remoteCmd{
		ID:       id,
		Kind:     kind,
		Args:     args,
		TS:       time.Now().Unix(),
		Approved: approved,
	}
	cmd.Sig = remoteSign(key, cmd.ID, cmd.Kind, cmd.Args, cmd.TS, cmd.Approved)
	body, _ := json.Marshal(map[string]any{"code": code, "cmd": cmd})
	return id, remoteJSON(http.MethodPost, base+"/fa/cmd", secret, body, nil)
}

// remoteWaitFor — ожидание результата с таймаутом по виду команды: run может
// длиться десятки минут (sfc/dism), чтение — нет.
func remoteWaitFor(events <-chan remoteEvent, id string, kind string) (remoteResult, bool) {
	timeout := 120 * time.Second
	if kind == "run" {
		timeout = 46 * time.Minute
	}
	return remoteWaitResult(events, id, timeout)
}

// remoteSubmit — R2: отправить команду без подтверждения и, если гость ответил
// «нужно подтверждение», спросить ОПЕРАТОРА здесь и повторить с approved=true.
// Подпись пересчитывается: approved входит в каноническую строку, поэтому
// «дописать» подтверждение по дороге нельзя.
func remoteSubmit(reader *bufio.Reader, base, secret, code string, key []byte, kind string, args json.RawMessage, events <-chan remoteEvent) (remoteResult, bool) {
	id, err := remoteSendCmd(base, secret, code, key, kind, args, false)
	if err != nil {
		fmt.Printf("[ERROR] команда не отправлена: %v\n", err)
		return remoteResult{}, false
	}
	res, ok := remoteWaitFor(events, id, kind)
	if !ok || !res.NeedsApproval {
		return res, ok
	}
	fmt.Println("\n── требуется подтверждение оператора ──")
	fmt.Printf("Вердикт гостя: %s\n", res.Verdict)
	fmt.Println("Подтверждает только человек за этим терминалом: автоподтверждения нет.")
	word, prompt := "y", "      Выполнить на госте? (y/n): "
	if res.Verdict == VDangerous.String() {
		word, prompt = "yes", "      Выполнить на госте? Введите yes: "
	}
	if reader == nil || !confirm(reader, prompt, word) {
		fmt.Println("      Отменено оператором: команда на госте не выполнена.")
		return remoteResult{ID: res.ID, Out: "оператор не подтвердил выполнение", Exit: -1, Verdict: res.Verdict}, true
	}
	id, err = remoteSendCmd(base, secret, code, key, kind, args, true)
	if err != nil {
		fmt.Printf("[ERROR] подтверждение не отправлено: %v\n", err)
		return remoteResult{}, false
	}
	return remoteWaitFor(events, id, kind)
}

func remoteRunOne(base, secret, code string, key []byte, line string, events <-chan remoteEvent) int {
	kind, args, quit, err := parseRemoteCommand(line)
	if err != nil {
		fmt.Println(err)
		return 2
	}
	if quit {
		fmt.Println("[i] выходить некуда: команды нет.")
		return 2
	}
	// reader нужен только для y/n при run; если stdin не терминал, confirm
	// честно откажет — автоподтверждения в этом режиме нет.
	reader := bufio.NewReader(os.Stdin)
	res, ok := remoteSubmit(reader, base, secret, code, key, kind, args, events)
	if !ok {
		return 1
	}
	remotePrintResult(res)
	return 0
}

func remoteREPL(base, secret, code string, key []byte, events <-chan remoteEvent) int {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("fa> ")
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		kind, args, quit, perr := parseRemoteCommand(line)
		if perr != nil {
			fmt.Println(perr)
			continue
		}
		if quit {
			remoteCloseSession(base, secret, code)
			fmt.Println("[i] сессия закрыта.")
			return 0
		}
		if kind == "" {
			fmt.Println(consoleHelp)
			continue
		}
		res, ok := remoteSubmit(reader, base, secret, code, key, kind, args, events)
		if ok {
			remotePrintResult(res)
		}
	}
	remoteCloseSession(base, secret, code)
	return 0
}

// parseRemoteCommand — человеческая строка → kind и JSON-аргументы того же
// вида, что у декларативных инструментов этапа 10. Пустой kind = help.
func parseRemoteCommand(line string) (kind string, args json.RawMessage, quit bool, err error) {
	fields, ferr := splitQuoted(line)
	if ferr != nil {
		return "", nil, false, ferr
	}
	if len(fields) == 0 {
		return "", nil, false, nil
	}
	verb := strings.ToLower(fields[0])
	mk := func(m map[string]any) (string, json.RawMessage, bool, error) {
		raw, _ := json.Marshal(m)
		return verbToKind(verb), raw, false, nil
	}
	switch verb {
	case "help", "?":
		return "", nil, false, nil
	case "quit", "exit", "close":
		return "close", nil, true, nil
	case "diag":
		return "diag", nil, false, nil
	case "read", "cat", "read_file":
		if len(fields) < 2 {
			return "", nil, false, fmt.Errorf("использование: read <путь>")
		}
		return mk(map[string]any{"path": fields[1]})
	case "grep", "grep_search":
		if len(fields) < 2 {
			return "", nil, false, fmt.Errorf("использование: grep <шаблон> [каталог] [glob]")
		}
		m := map[string]any{"pattern": fields[1]}
		if len(fields) >= 3 {
			m["path"] = fields[2]
		}
		if len(fields) >= 4 {
			m["glob"] = fields[3]
		}
		return mk(m)
	case "glob", "file_glob_search":
		if len(fields) < 2 {
			return "", nil, false, fmt.Errorf("использование: glob <шаблон> [каталог]")
		}
		m := map[string]any{"pattern": fields[1]}
		if len(fields) >= 3 {
			m["path"] = fields[2]
		}
		return mk(m)
	case "run", "cmd", "exec":
		// Команда — остаток строки КАК ЕСТЬ: кавычки и пайпы принадлежат
		// команде, а не парсеру консоли.
		rest := strings.TrimSpace(line[len(fields[0]):])
		if rest == "" {
			return "", nil, false, fmt.Errorf("использование: run <команда>")
		}
		raw, _ := json.Marshal(map[string]any{"command": rest})
		return "run", raw, false, nil
	}
	return "", nil, false, fmt.Errorf("неизвестная команда %q; help — список", verb)
}

// verbToKind — имя ручки в консоли → kind команды (совпадает с инструментами).
func verbToKind(verb string) string {
	switch verb {
	case "read", "cat", "read_file":
		return "read_file"
	case "grep", "grep_search":
		return "grep_search"
	case "glob", "file_glob_search":
		return "file_glob_search"
	}
	return verb
}

// splitQuoted — разбиение на слова с поддержкой "кавычек" (пути с пробелами
// на Windows — норма, а не редкость).
func splitQuoted(line string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range line {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t') && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if inQuote {
		return nil, fmt.Errorf("незакрытая кавычка")
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Операторские HTTP-ручки и файл-билет
// ---------------------------------------------------------------------------

func remoteCreateSession(base, secret string) (code, opToken string, err error) {
	var out struct {
		Code    string `json:"code"`
		OpToken string `json:"op_token"`
	}
	if err := remoteJSON(http.MethodPost, base+"/fa/session", secret, []byte("{}"), &out); err != nil {
		return "", "", err
	}
	if out.Code == "" || out.OpToken == "" {
		return "", "", fmt.Errorf("брокер вернул пустую сессию")
	}
	return out.Code, out.OpToken, nil
}

func remoteCloseSession(base, secret, code string) {
	body, _ := json.Marshal(map[string]string{"code": code})
	if err := remoteJSON(http.MethodPost, base+"/fa/close", secret, body, nil); err != nil {
		fmt.Printf("[!] закрыть сессию не удалось: %v\n", err)
	}
}

// remoteTicketFile — положить копию exe рядом (в кэш) с кодом в имени.
// Копия, а не ссылка: гостю отправляется именно файл.
func remoteTicketFile(cfg Config, code string) (string, error) {
	candidates := []string{cfg.Remote.TicketExe, filepath.Join(filepath.Dir(os.Args[0]), "fastagent.exe"), "FastAgent/fastagent.exe"}
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "fastagent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		data, rerr := os.ReadFile(c)
		if rerr != nil {
			continue
		}
		dst := filepath.Join(dir, "fastagent-"+code+".exe")
		if werr := os.WriteFile(dst, data, 0o755); werr != nil {
			return "", werr
		}
		return dst, nil
	}
	return "", fmt.Errorf("не найден exe для файла-билета (задайте remote.ticket_exe)")
}

// remoteListenEvents — SSE-поток от брокера в канал; при обрыве переподключаемся.
func remoteListenEvents(base, code, secret string, out chan<- remoteEvent) {
	for {
		req, err := http.NewRequest(http.MethodGet, base+"/fa/events?code="+code, nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+secret)
			resp, derr := remoteClient.Do(req)
			if derr == nil && resp != nil && resp.StatusCode == http.StatusOK {
				remoteReadSSE(resp.Body, out)
				resp.Body.Close()
			} else if resp != nil {
				resp.Body.Close()
			}
		}
		time.Sleep(2 * time.Second)
	}
}

func remoteReadSSE(body io.Reader, out chan<- remoteEvent) {
	buf := bufio.NewReader(body)
	for {
		line, err := buf.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev remoteEvent
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) == nil {
			select {
			case out <- ev:
			default:
			}
		}
	}
}
