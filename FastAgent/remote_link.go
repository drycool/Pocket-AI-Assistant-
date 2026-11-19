// remote_link.go — гостевая сторона удалённой диагностики (этапы R1–R4).
//
// На гостевом ПК запускается один exe; код сессии берётся из ИМЕНИ файла
// (fastagent-a1b2c3.exe), поэтому на чужой клавиатуре не набирается ничего.
// Гость звонит наружу, длинно поллит команды, проверяет подпись и исполняет
// R1 (чтение: diag, read_file, grep_search, file_glob_search), R2 (`run` —
// только после подтверждения оператора) и R3 (get_file/put_file кусками).
// L3 удалённо не разрешён: уровень гостя в этом режиме — L2, опасное и
// «вне белого списка» отклоняется. Запись файла (put_file) — тоже L2, то есть
// только с подтверждением оператора; куски без approved не пишутся вообще.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	fmt.Println("  FASTAGENT — диагностика компьютера")
	fmt.Println("===================================================")
	fmt.Println("  [OK] Связь с оператором установлена.")
	fmt.Println("  Оператор видит этот компьютер. Чтение — без вопросов,")
	fmt.Println("  изменения — только после его подтверждения.")
	fmt.Println("  Это окно не закрывайте: в конце оно попрощается само.")
	fmt.Printf("  Сессия: %s\n", code)
	logRemoteAction("link-start", VReadOnly, true, "code="+code)

	backoff := time.Second
	for {
		cmd, perr := linkPoll(base, guest)
		if perr != nil {
			if errors.Is(perr, errRemoteClosed) {
				fmt.Println()
				fmt.Println("  Готово. Оператор завершил сессию — закройте это окно.")
				return 0
			}
			fmt.Printf("  Связь прервалась, переподключаюсь через %s…\n", backoff)
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
			Data:          out.data,
			Offset:        out.offset,
			Total:         out.total,
			EOF:           out.eof,
			SHA256:        out.sha,
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

	// R3: кусок файла. data — base64, отдельно от out: out режется truncate
	// и содержит человекочитаемую строку о том, что именно передано.
	data   string
	offset int64
	total  int64
	eof    bool
	sha    string
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
	if c.Kind == "get_file" {
		return remoteExecGet(args)
	}
	if c.Kind == "put_file" {
		return remoteExecPut(c, args)
	}
	if c.Kind == "journal" {
		// R4: свой журнал — это чтение, подтверждения не требует. Отдаём хвост,
		// а не файл целиком: журнал в этой сессии уже может быть на сотни строк,
		// а оператору нужны вердикты и approved, то есть последние шаги.
		out, rc := linkJournal(args)
		logRemoteAction("journal", VReadOnly, true, out)
		return execOutcome{out: truncate(out, remoteMaxResultSize), exit: rc, ok: true}
	}
	if c.Kind == "quick" {
		// Быстрые проверки: один короткий снимок вместо длинного diag. Только
		// чтение — ни одна из команд в quickWindows/quickLinux не меняет систему.
		out, rc, err := runQuickProbe()
		if err != nil {
			logRemoteAction("quick", VReadOnly, false, err.Error())
			return execOutcome{out: "ошибка быстрых проверок: " + err.Error(), exit: -1}
		}
		logRemoteAction("quick", VReadOnly, true, out)
		return execOutcome{out: truncate(out, remoteMaxResultSize), exit: rc, ok: true}
	}
	if c.Kind == "report" {
		// Отчёт оператора выводится в консоль КЛИЕНТА: он смотрит на своё окно,
		// а не на наш терминал. Поэтому печатаем здесь, на госте, и возвращаем
		// оператору только подтверждение, что текст дошёл до экрана.
		text, _ := args["text"].(string)
		text = strings.TrimSpace(text)
		if text == "" {
			text = "Проверка закончена. Подробности оператор пришлёт отдельно."
		}
		printGuestReport(text)
		logRemoteAction("report", VReadOnly, true, "отчёт выведен в консоль гостя")
		return execOutcome{out: "отчёт выведен на терминал клиента", ok: true}
	}
	verdict := classifyTool(c.Kind, L1Read)
	if verdict != VReadOnly && !c.Approved {
		msg := fmt.Sprintf("R3: инструмент %q удалённо недоступен (только чтение)", c.Kind)
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

// ---------------------------------------------------------------------------
// R3: передача файлов кусками
// ---------------------------------------------------------------------------

// remoteExecGet — отдать один кусок файла (чтение). Проходит те же запреты,
// что read_file: секреты, каталоги, лимит размера. SHA256 файла целиком
// считается при первом куске — по нему оператор проверяет собранную копию.
func remoteExecGet(args map[string]any) execOutcome {
	p := argString(args, "path")
	if p == "" {
		return execOutcome{out: "не задан path", exit: -1}
	}
	abs, err := toolAbsPath(p)
	if err != nil {
		logRemoteAction("get_file", VReadOnly, false, err.Error())
		return execOutcome{out: err.Error(), exit: -1}
	}
	if isSecretPath(abs) {
		msg := "это файл с секретами (ключи/пароли) — передача запрещена"
		logRemoteAction("get_file "+abs, VForbidden, false, msg)
		return execOutcome{out: msg, exit: -1, verdict: VForbidden.String()}
	}
	fi, err := os.Stat(abs)
	if err != nil {
		msg := "файл не найден: " + err.Error()
		logRemoteAction("get_file "+abs, VReadOnly, false, msg)
		return execOutcome{out: msg, exit: -1}
	}
	if fi.IsDir() {
		msg := "это каталог; передача каталога не поддерживается"
		logRemoteAction("get_file "+abs, VReadOnly, false, msg)
		return execOutcome{out: msg, exit: -1}
	}
	if fi.Size() > remoteMaxFileSize {
		msg := fmt.Sprintf("файл %d байт — больше лимита передачи %d байт", fi.Size(), int64(remoteMaxFileSize))
		logRemoteAction("get_file "+abs, VReadOnly, false, msg)
		return execOutcome{out: msg, exit: -1}
	}
	offset := int64(argInt(args, "offset", 0))
	if offset < 0 || offset > fi.Size() {
		msg := fmt.Sprintf("offset %d вне файла (размер %d)", offset, fi.Size())
		logRemoteAction("get_file "+abs, VReadOnly, false, msg)
		return execOutcome{out: msg, exit: -1}
	}
	n := int64(remoteChunkSize)
	if fi.Size()-offset < n {
		n = fi.Size() - offset
	}
	buf := make([]byte, n)
	read := 0
	if n > 0 {
		f, oerr := os.Open(abs)
		if oerr != nil {
			msg := "чтение: " + oerr.Error()
			logRemoteAction("get_file "+abs, VReadOnly, false, msg)
			return execOutcome{out: msg, exit: -1}
		}
		read, oerr = f.ReadAt(buf, offset)
		f.Close()
		if oerr != nil && oerr != io.EOF {
			msg := "чтение: " + oerr.Error()
			logRemoteAction("get_file "+abs, VReadOnly, false, msg)
			return execOutcome{out: msg, exit: -1}
		}
	}
	sum := ""
	if offset == 0 {
		// Хеш не должен ломать передачу: если посчитать не удалось, оператор
		// получит пустой sha256 и проверит размер — это честнее отказа.
		sum, _ = remoteFileSHA256(abs)
	}
	out := fmt.Sprintf("%s [%d..%d] %d байт", abs, offset, offset+int64(read), fi.Size())
	logRemoteAction("get_file "+abs, VReadOnly, true, out)
	return execOutcome{
		out:     out,
		ok:      true,
		verdict: VReadOnly.String(),
		data:    base64.StdEncoding.EncodeToString(buf[:read]),
		offset:  offset,
		total:   fi.Size(),
		eof:     offset+int64(read) >= fi.Size(),
		sha:     sum,
	}
}

// remoteExecPut — принять один кусок файла. Запись — изменение (L2), поэтому
// без approved гость НИЧЕГО не пишет, а отвечает needs_approval; оператор
// подтверждает у себя, и только потом куски идут с approved=true. В конце
// сверяется sha256 всего файла, и только потом .part переименовывается:
// недописанный или подменённый файл не появится под настоящим именем.
func remoteExecPut(c remoteCmd, args map[string]any) execOutcome {
	p := argString(args, "path")
	if p == "" {
		return execOutcome{out: "не задан path", exit: -1}
	}
	abs, err := toolAbsPath(p)
	if err != nil {
		logRemoteAction("put_file", VForbidden, false, err.Error())
		return execOutcome{out: err.Error(), exit: -1}
	}
	if isSecretPath(abs) {
		msg := "путь относится к секретам (ключи/пароли) — запись запрещена"
		logRemoteAction("put_file "+abs, VForbidden, false, msg)
		return execOutcome{out: msg, exit: -1, verdict: VForbidden.String()}
	}
	if isProtectedWritePath(abs) {
		msg := "системный каталог — запись запрещена политикой"
		logRemoteAction("put_file "+abs, VForbidden, false, msg)
		return execOutcome{out: msg, exit: -1, verdict: VForbidden.String()}
	}
	total := int64(argInt(args, "total", -1))
	if total < 0 || total > remoteMaxFileSize {
		msg := fmt.Sprintf("объявленный размер %d байт вне лимита передачи (0…%d)", total, int64(remoteMaxFileSize))
		logRemoteAction("put_file "+abs, VForbidden, false, msg)
		return execOutcome{out: msg, exit: -1}
	}
	dir := filepath.Dir(abs)
	if fi, derr := os.Stat(dir); derr != nil || !fi.IsDir() {
		msg := fmt.Sprintf("каталог %s не существует — создайте его вручную", dir)
		logRemoteAction("put_file "+abs, VForbidden, false, msg)
		return execOutcome{out: msg, exit: -1}
	}
	verdict := classifyTool("write_file", currentLevel)
	if verdict == VForbidden || verdict == VNotAllowed {
		msg := fmt.Sprintf("запись недоступна удалённо на уровне L%d: %s", currentLevel, verdict)
		logRemoteAction("put_file "+abs, verdict, false, msg)
		return execOutcome{out: msg, exit: -1, verdict: verdict.String()}
	}
	if !c.Approved {
		logRemoteAction("put_file "+abs, verdict, false, "ждёт подтверждения оператора")
		return execOutcome{
			out:           fmt.Sprintf("нужно подтверждение оператора: %s", verdict),
			exit:          -2,
			needsApproval: true,
			verdict:       verdict.String(),
		}
	}
	offset := int64(argInt(args, "offset", 0))
	if offset < 0 || offset > total {
		msg := fmt.Sprintf("offset %d вне объявленного размера %d", offset, total)
		logRemoteAction("put_file "+abs, VForbidden, false, msg)
		return execOutcome{out: msg, exit: -1}
	}
	raw, derr := base64.StdEncoding.DecodeString(argString(args, "data"))
	if derr != nil {
		msg := "битый base64 в куске: " + derr.Error()
		logRemoteAction("put_file "+abs, VForbidden, false, msg)
		return execOutcome{out: msg, exit: -1}
	}
	if len(raw) > remoteChunkSize {
		msg := fmt.Sprintf("кусок %d байт больше лимита %d", len(raw), remoteChunkSize)
		logRemoteAction("put_file "+abs, VForbidden, false, msg)
		return execOutcome{out: msg, exit: -1}
	}
	if offset+int64(len(raw)) > total {
		msg := fmt.Sprintf("кусок выходит за объявленный размер (offset %d + %d > %d)", offset, len(raw), total)
		logRemoteAction("put_file "+abs, VForbidden, false, msg)
		return execOutcome{out: msg, exit: -1}
	}
	part := abs + ".part-fastagent"
	flags := os.O_WRONLY | os.O_CREATE
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	f, oerr := os.OpenFile(part, flags, 0o644)
	if oerr != nil {
		msg := "запись: " + oerr.Error()
		logRemoteAction("put_file "+abs, VForbidden, true, msg)
		return execOutcome{out: msg, exit: -1}
	}
	if fi, serr := f.Stat(); serr == nil && fi.Size() != offset {
		f.Close()
		msg := fmt.Sprintf("ожидался offset %d, а в файле %d байт — начните передачу заново", offset, fi.Size())
		logRemoteAction("put_file "+abs, VForbidden, true, msg)
		return execOutcome{out: msg, exit: -1}
	}
	if _, werr := f.WriteAt(raw, offset); werr != nil {
		f.Close()
		msg := "запись: " + werr.Error()
		logRemoteAction("put_file "+abs, VForbidden, true, msg)
		return execOutcome{out: msg, exit: -1}
	}
	final := argBool(args, "final")
	if !final {
		if cerr := f.Close(); cerr != nil {
			msg := "закрытие: " + cerr.Error()
			logRemoteAction("put_file "+abs, VForbidden, true, msg)
			return execOutcome{out: msg, exit: -1}
		}
		got := offset + int64(len(raw))
		out := fmt.Sprintf("принято %d/%d байт", got, total)
		logRemoteAction("put_file "+abs, verdict, true, out)
		return execOutcome{out: out, ok: true, verdict: verdict.String(), offset: offset, total: total}
	}
	if terr := f.Truncate(total); terr != nil {
		f.Close()
		msg := "выравнивание размера: " + terr.Error()
		logRemoteAction("put_file "+abs, VForbidden, true, msg)
		return execOutcome{out: msg, exit: -1}
	}
	_ = f.Sync()
	if cerr := f.Close(); cerr != nil {
		msg := "закрытие: " + cerr.Error()
		logRemoteAction("put_file "+abs, VForbidden, true, msg)
		return execOutcome{out: msg, exit: -1}
	}
	// Сверка хеша: до неё файл лежит под временным именем, поэтому
	// «записалось наполовину» не выглядит как успешная запись.
	want := argString(args, "sha256")
	got, herr := remoteFileSHA256(part)
	if herr != nil {
		os.Remove(part)
		msg := "сверка хеша: " + herr.Error()
		logRemoteAction("put_file "+abs, VForbidden, true, msg)
		return execOutcome{out: msg, exit: -1}
	}
	if want == "" || !strings.EqualFold(got, want) {
		os.Remove(part)
		msg := fmt.Sprintf("sha256 не сошёлся (у меня %s, ждали %s) — файл не записан", got, want)
		logRemoteAction("put_file "+abs, VForbidden, true, msg)
		return execOutcome{out: msg, exit: -1}
	}
	if rerr := os.Rename(part, abs); rerr != nil {
		os.Remove(part)
		msg := "переименование: " + rerr.Error()
		logRemoteAction("put_file "+abs, VForbidden, true, msg)
		return execOutcome{out: msg, exit: -1}
	}
	out := fmt.Sprintf("записано %d байт в %s (sha256 %s)", total, abs, shortHex(got))
	logRemoteAction("put_file "+abs, verdict, true, out)
	return execOutcome{out: out, ok: true, verdict: verdict.String(), total: total, eof: true, sha: got}
}

// remoteFileSHA256 — хеш файла целиком. Читаем не больше лимита передачи:
// хешировать файл в сотни мегабайт ради сверки незачем.
func remoteFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, remoteMaxFileSize+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// shortHex — первые 16 символов хеша для журнала и сообщений.
func shortHex(h string) string {
	if len(h) > 16 {
		return h[:16] + "…"
	}
	return h
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

// linkJournal — R4: хвост собственного журнала гостя для архива сессии.
// Первые строки ответа — host= и path=: оператор узнаёт, у кого именно он
// снимал диагностику и где лежит полный журнал (его можно забрать через get).
func linkJournal(args map[string]any) (string, int) {
	n := archiveJournalLines
	if v, ok := args["lines"].(float64); ok && v > 0 {
		n = int(v)
	}
	if n > 1000 {
		n = 1000
	}
	path := remoteLogPath()
	var b strings.Builder
	size := int64(0)
	if fi, statErr := os.Stat(path); statErr == nil {
		size = fi.Size()
	}
	fmt.Fprintf(&b, "host=%s\npath=%s\nsize=%d\n", hostname(), path, size)
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(&b, "журнал недоступен: %v\n", err)
		return b.String(), 1
	}
	body := tailLines(string(data), n)
	lines := strings.Count(body, "\n") + 1
	if strings.TrimSpace(body) == "" {
		lines = 0
	}
	fmt.Fprintf(&b, "lines=%d\n---\n%s", lines, body)
	return b.String(), 0
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
