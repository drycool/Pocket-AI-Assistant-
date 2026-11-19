// remote_console.go — консоль оператора удалённой диагностики (R1–R4).
//
// Оператор запускает на pi5 `fastagent -remote new`: брокер создаёт пару
// «код → сессия», рядом кладётся файл-билет fastagent-<код>.exe, который
// перетаскивается гостю через AnyDesk. Дальше оператор печатает команды в
// СВОЁМ терминале, а гость их исполняет и присылает вывод.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// consoleHelp — что можно в консоли оператора.
const consoleHelp = `Команды консоли (R1–R4):
  diag                                  снимок системы гостя
  read <путь>                           прочитать файл
  grep <шаблон> [каталог] [glob]        поиск по содержимому
  glob <шаблон> [каталог]               поиск файлов по имени
  run <команда>                         выполнить команду на госте (R2)
  get <путь на госте> [локальный путь]  забрать файл (R3, до 50 МиБ)
  put <локальный файл> <путь на госте>  положить файл (R3, до 50 МиБ)
  journal [N]                           последние N строк журнала гостя (R4)
  save [заметка]                        архив сессии в Data/logs + уроки (R4)
  status | help | quit                  состояние / справка / выход

run и put выполняются по политике ГОСТЯ. Изменяющая команда и запись файла
сначала спросят подтверждение ЗДЕСЬ, в вашем терминале, и только потом уйдут
на гость с подписанным approved=true. Опасное и «вне белого списка» удалённо
не выполняется: L3 в удалённом режиме не разрешён. put не пишет в системные
каталоги и в секреты; файл на госте появляется под настоящим именем только
после сверки sha256. save пишет архив сессии (шаги, отказы, журнал гостя) в
Data/logs/remote-*.txt и, если задан -memory-dir, уроки в память Jev.`

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
	fmt.Println("        FASTAGENT — консоль удалённой диагностики (R1–R5)")
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
	fmt.Println("[OK] Гость на связи. Дальше — меню; старая консоль команд: -plain.")
	if archiveMemoryDir != "" {
		note := ""
		if archiveReindex {
			note = " (с переиндексацией)"
		}
		fmt.Printf("[i] R4: уроки сессии пойдут в %s%s\n", archiveMemoryDir, note)
	} else {
		fmt.Println("[i] R4: архив сессии — только txt в Data/logs (каталог памяти не задан).")
	}

	arch := newSessionArchive(code, base)
	if strings.TrimSpace(oneCmd) != "" {
		rc := remoteRunOne(base, secret, code, key, oneCmd, events, arch)
		if arch.dirty {
			saveArchive(arch, "одна команда: "+oneCmd)
		}
		return rc
	}
	// R5: после пары — меню оператора, если не просили старую консоль.
	var rc int
	if operatorPlain {
		rc = remoteREPL(base, secret, code, key, events, arch)
	} else {
		rc = runOperatorUI(cfg, base, secret, code, key, events, arch)
	}
	if arch.dirty {
		// Заметку берём из последнего сохранения: иначе quit после `save
		// заметка` перезаписывал уроки пустой заметкой и вывод оператора
		// пропадал (дефект смоук-прогона 18.11.2026).
		saveArchive(arch, arch.lastNote)
	}
	return rc
}

// remoteJournal — R4: попросить у гостя хвост его журнала. Отдельной ручкой, а
// не через run: журнал гостя — это чтение собственного файла, и подтверждения
// оператора оно не требует.
func remoteJournal(base, secret, code string, key []byte, events <-chan remoteEvent, lines int) (remoteResult, bool) {
	if lines <= 0 {
		lines = archiveJournalLines
	}
	args, _ := json.Marshal(map[string]any{"lines": lines})
	id, err := remoteSendCmd(base, secret, code, key, "journal", args, false)
	if err != nil {
		fmt.Printf("[!] журнал гостя не запрошен: %v\n", err)
		return remoteResult{}, false
	}
	return remoteWaitResult(events, id, 30*time.Second)
}

// pullGuestJournal — забрать журнал гостя в архив. Вердикты и approved живут
// только на госте: без них архив показывает, что просил оператор, и молчит о
// том, что гость на самом деле решил.
func pullGuestJournal(base, secret, code string, key []byte, events <-chan remoteEvent, arch *sessionArchive) {
	res, ok := remoteJournal(base, secret, code, key, events, archiveJournalLines)
	if !ok {
		fmt.Println("[i] журнал гостя не получен — архив без него (сессия уже закрывалась).")
		return
	}
	if !res.OK {
		fmt.Println("[i] гость не отдал журнал — архив без него.")
		return
	}
	// Служебный забор (перед save/quit) — не шаг оператора: он обновляет
	// раздел «журнал гостя», но не увеличивает «Шагов: N».
	arch.takeGuestJournal(res.Out)
	fmt.Printf("[i] журнал гостя приложен: %s (%s)\n",
		firstNonEmpty(arch.GuestHost, "гость"), arch.GuestLog)
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

// ---------------------------------------------------------------------------
// R3: передача файлов
// ---------------------------------------------------------------------------

// remoteGetFile — забрать файл с гостя кусками. Каждый кусок — отдельная
// подписанная команда get_file; локально сборка идёт во временный .part и
// переименовывается только после сверки размера и sha256. Так оборванная
// передача не оставляет полуфайл под настоящим именем.
func remoteGetFile(base, secret, code string, key []byte, remotePath, localPath string, events <-chan remoteEvent) error {
	_, err := remoteGetFileTo(base, secret, code, key, remotePath, localPath, events)
	return err
}

// remoteGetFileTo — то же, но возвращает путь, под которым файл лёг локально:
// уникальный суффикс выбирается внутри, и архиву R4 нужно настоящее имя.
func remoteGetFileTo(base, secret, code string, key []byte, remotePath, localPath string, events <-chan remoteEvent) (string, error) {
	if strings.TrimSpace(localPath) == "" {
		dir, err := remoteIncomingDir()
		if err != nil {
			return "", err
		}
		localPath = filepath.Join(dir, filepath.Base(strings.TrimRight(remotePath, "/\\")))
	}
	dst := remoteUniquePath(localPath)
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", fmt.Errorf("локальный файл %s: %v", tmp, err)
	}
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}

	var offset, total int64
	var wantSum string
	first := true
	shown := false
	for {
		args, _ := json.Marshal(map[string]any{"path": remotePath, "offset": offset})
		id, serr := remoteSendCmd(base, secret, code, key, "get_file", args, false)
		if serr != nil {
			cleanup()
			return "", fmt.Errorf("кусок offset=%d не отправлен: %v", offset, serr)
		}
		res, ok := remoteWaitResult(events, id, 60*time.Second)
		if !ok {
			cleanup()
			return "", fmt.Errorf("гость не ответил на кусок offset=%d", offset)
		}
		if !res.OK {
			cleanup()
			return "", fmt.Errorf("%s", strings.TrimSpace(res.Out))
		}
		if first {
			first = false
			total = res.Total
			wantSum = res.SHA256
			if total > remoteMaxFileSize {
				cleanup()
				return "", fmt.Errorf("файл %d байт — больше лимита передачи %d", total, int64(remoteMaxFileSize))
			}
		}
		if res.Total != total {
			cleanup()
			return "", fmt.Errorf("размер файла изменился по дороге (%d → %d) — передача отменена", total, res.Total)
		}
		if res.Offset != offset {
			cleanup()
			return "", fmt.Errorf("гость отдал offset %d, ждали %d", res.Offset, offset)
		}
		chunk, derr := base64.StdEncoding.DecodeString(res.Data)
		if derr != nil {
			cleanup()
			return "", fmt.Errorf("кусок не base64: %v", derr)
		}
		if len(chunk) == 0 && !res.EOF {
			cleanup()
			return "", fmt.Errorf("пустой кусок без признака конца — передача прервана")
		}
		if _, werr := f.WriteAt(chunk, offset); werr != nil {
			cleanup()
			return "", fmt.Errorf("запись %s: %v", tmp, werr)
		}
		offset += int64(len(chunk))
		shown = remoteProgress(offset, total) || shown
		if res.EOF {
			break
		}
	}
	if shown {
		fmt.Println()
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("закрытие %s: %v", tmp, cerr)
	}
	if offset != total {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("получено %d из %d байт — файл не собран", offset, total)
	}
	if wantSum != "" {
		got, herr := remoteFileSum(tmp)
		if herr != nil {
			_ = os.Remove(tmp)
			return "", fmt.Errorf("сверка хеша: %v", herr)
		}
		if !strings.EqualFold(got, wantSum) {
			_ = os.Remove(tmp)
			return "", fmt.Errorf("sha256 не сошёлся (получено %s, у гостя %s) — файл не сохранён", got, wantSum)
		}
	}
	if rerr := os.Rename(tmp, dst); rerr != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("переименование %s: %v", tmp, rerr)
	}
	sum := wantSum
	if sum == "" {
		sum, _ = remoteFileSum(dst)
	}
	fmt.Printf("[OK] файл получен: %s (%s, sha256 %s)\n", dst, remoteHumanBytes(total), shortHex(sum))
	return dst, nil
}

// remotePutFile — положить файл на гостя кусками. Порядок тот же, что у run:
// первый кусок уходит БЕЗ approved, гость классифицирует запись и просит
// подтверждение; y спрашивается здесь; только после него куски пишутся.
// Файл на госте появляется под настоящим именем после сверки sha256.
func remotePutFile(reader *bufio.Reader, base, secret, code string, key []byte, localPath, remotePath string, events <-chan remoteEvent) (remoteResult, bool) {
	fi, err := os.Stat(localPath)
	if err != nil {
		fmt.Printf("[ERROR] локальный файл: %v\n", err)
		return remoteResult{}, false
	}
	if !fi.Mode().IsRegular() {
		fmt.Printf("[ERROR] %s — не обычный файл\n", localPath)
		return remoteResult{}, false
	}
	if fi.Size() > remoteMaxFileSize {
		fmt.Printf("[ERROR] файл %d байт — больше лимита передачи %d\n", fi.Size(), int64(remoteMaxFileSize))
		return remoteResult{}, false
	}
	data, rerr := os.ReadFile(localPath)
	if rerr != nil {
		fmt.Printf("[ERROR] чтение %s: %v\n", localPath, rerr)
		return remoteResult{}, false
	}
	sumRaw := sha256.Sum256(data)
	sum := hex.EncodeToString(sumRaw[:])
	total := int64(len(data))
	chunkAt := func(off int64) []byte {
		end := off + remoteChunkSize
		if end > total {
			end = total
		}
		return data[off:end]
	}
	mkArgs := func(off int64) json.RawMessage {
		c := chunkAt(off)
		m := map[string]any{
			"path":   remotePath,
			"offset": off,
			"total":  total,
			"data":   base64.StdEncoding.EncodeToString(c),
			"final":  off+int64(len(c)) == total,
			"sha256": sum,
		}
		raw, _ := json.Marshal(m)
		return raw
	}

	// Шаг 1: политика ГОСТЯ, без подтверждения. Пока его нет, гость не пишет
	// ни байта — ответ needs_approval ничего не меняет на диске.
	id, serr := remoteSendCmd(base, secret, code, key, "put_file", mkArgs(0), false)
	if serr != nil {
		fmt.Printf("[ERROR] команда не отправлена: %v\n", serr)
		return remoteResult{}, false
	}
	res, ok := remoteWaitResult(events, id, 60*time.Second)
	if !ok {
		return res, false
	}
	if !res.NeedsApproval {
		return res, true // отказ политики: покажется как обычный результат
	}
	fmt.Println("\n── требуется подтверждение оператора ──")
	fmt.Printf("Запись файла на гость: %s (%s, sha256 %s)\n", remotePath, remoteHumanBytes(total), shortHex(sum))
	fmt.Printf("Вердикт гостя: %s\n", res.Verdict)
	fmt.Println("Подтверждает только человек за этим терминалом: автоподтверждения нет.")
	if reader == nil || !confirm(reader, "      Записать файл на гость? (y/n): ", "y") {
		fmt.Println("      Отменено оператором: файл на госте не записан.")
		return remoteResult{ID: res.ID, Out: "оператор не подтвердил запись файла", Exit: -1, Verdict: res.Verdict}, true
	}

	// Шаг 2: куски с approved=true. Первый отправляем заново: пока не было
	// подтверждения, гость ничего не записал.
	var offset int64
	shown := false
	for {
		c := chunkAt(offset)
		id, serr := remoteSendCmd(base, secret, code, key, "put_file", mkArgs(offset), true)
		if serr != nil {
			fmt.Printf("[ERROR] кусок offset=%d не отправлен: %v\n", offset, serr)
			return remoteResult{}, false
		}
		// res/ok — те же переменные, что у шага подтверждения: иначе после
		// цикла наружу уходил результат «нужно подтверждение», а не итог записи.
		res, ok = remoteWaitResult(events, id, 60*time.Second)
		if !ok || !res.OK {
			if shown {
				fmt.Println()
			}
			return res, ok
		}
		offset += int64(len(c))
		shown = remoteProgress(offset, total) || shown
		if offset >= total {
			break
		}
	}
	if shown {
		fmt.Println()
	}
	return res, true
}

// remoteIncomingDir — куда складывать забранные файлы по умолчанию.
func remoteIncomingDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "fastagent", "incoming")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("каталог для принятых файлов: %v", err)
	}
	return dir, nil
}

// remoteUniquePath — не затирать то, что уже лежит: файл рядом с одноимённым
// получает суффикс -1, -2. Молча перезаписывать принятый с гостя файл нельзя.
func remoteUniquePath(p string) string {
	if _, err := os.Stat(p); err != nil {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 1; i < 100; i++ {
		cand := fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Stat(cand); err != nil {
			return cand
		}
	}
	return p
}

// remoteProgress — строка прогресса в одну строку терминала. Возвращает true,
// если что-то напечатала: вызывающий тогда сам добавит перевод строки в конце.
func remoteProgress(got, total int64) bool {
	if total <= 0 {
		return false
	}
	pct := got * 100 / total
	fmt.Printf("\r  передача: %s / %s (%d%%)", remoteHumanBytes(got), remoteHumanBytes(total), pct)
	return true
}

// remoteHumanBytes — размер по-человечески: 12.3 КиБ, 4.5 МиБ.
func remoteHumanBytes(n int64) string {
	const k = 1024
	switch {
	case n < k:
		return fmt.Sprintf("%d Б", n)
	case n < k*k:
		return fmt.Sprintf("%.1f КиБ", float64(n)/k)
	case n < k*k*k:
		return fmt.Sprintf("%.1f МиБ", float64(n)/(k*k))
	}
	return fmt.Sprintf("%.1f ГиБ", float64(n)/(k*k*k))
}

// remoteFileSum — sha256 локального файла для сверки с гостем.
func remoteFileSum(path string) (string, error) {
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

func remoteRunOne(base, secret, code string, key []byte, line string, events <-chan remoteEvent, arch *sessionArchive) int {
	kind, args, quit, err := parseRemoteCommand(line)
	if err != nil {
		fmt.Println(err)
		return 2
	}
	if quit {
		fmt.Println("[i] выходить некуда: команды нет.")
		return 2
	}
	// reader нужен только для y/n при run и put; если stdin не терминал,
	// confirm честно откажет — автоподтверждения в этом режиме нет.
	reader := bufio.NewReader(os.Stdin)
	if !remoteRunParsed(reader, base, secret, code, key, line, kind, args, events, arch) {
		return 1
	}
	return 0
}

// remoteRunParsed — одна разобранная команда консоли: передача файла идёт
// своим путём (несколько запросов-ответов), остальное — одной командой.
func remoteRunParsed(reader *bufio.Reader, base, secret, code string, key []byte,
	line, kind string, args json.RawMessage, events <-chan remoteEvent, arch *sessionArchive) bool {
	switch kind {
	case "save":
		// R4: архив пишется здесь, на стороне оператора, — гостю для этого не
		// нужно ничего, и закрывать сессию ради архива не надо.
		var a struct {
			Note string `json:"note"`
		}
		_ = json.Unmarshal(args, &a)
		pullGuestJournal(base, secret, code, key, events, arch)
		saveArchive(arch, a.Note)
		return true
	case "get_file":
		var a struct {
			Path  string `json:"path"`
			Local string `json:"local"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.Path == "" {
			fmt.Println("get: нужен путь на госте")
			return false
		}
		dst, gerr := remoteGetFileTo(base, secret, code, key, a.Path, a.Local, events)
		if gerr != nil {
			fmt.Printf("[!] %v\n", gerr)
			arch.Add(line, "get_file", remoteResult{OK: false, Exit: -1, Out: gerr.Error()})
			return false
		}
		arch.Add(line, "get_file", remoteResult{OK: true, Out: "получено: " + dst})
		return true
	case "put_file":
		var a struct {
			Path  string `json:"path"`
			Local string `json:"local"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.Path == "" || a.Local == "" {
			fmt.Println("put: нужен локальный файл и путь на госте")
			return false
		}
		res, ok := remotePutFile(reader, base, secret, code, key, a.Local, a.Path, events)
		if ok {
			remotePrintResult(res)
			arch.Add(line, "put_file", res)
		}
		return ok
	}
	res, ok := remoteSubmit(reader, base, secret, code, key, kind, args, events)
	if ok {
		remotePrintResult(res)
	}
	if ok || res.ID != "" {
		arch.Add(line, kind, res)
	}
	return ok
}

func remoteREPL(base, secret, code string, key []byte, events <-chan remoteEvent, arch *sessionArchive) int {
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
			// Журнал забираем ДО закрытия: после close гость уже не ответит, и
			// вердикты сессии пропали бы бесследно — ровно то, ради чего R4.
			pullGuestJournal(base, secret, code, key, events, arch)
			remoteCloseSession(base, secret, code)
			fmt.Println("[i] сессия закрыта.")
			return 0
		}
		if kind == "" {
			fmt.Println(consoleHelp)
			continue
		}
		remoteRunParsed(reader, base, secret, code, key, line, kind, args, events, arch)
	}
	pullGuestJournal(base, secret, code, key, events, arch)
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
	case "get", "download", "get_file":
		if len(fields) < 2 {
			return "", nil, false, fmt.Errorf("использование: get <путь на госте> [локальный путь]")
		}
		m := map[string]any{"path": fields[1]}
		if len(fields) >= 3 {
			m["local"] = fields[2]
		}
		return mk(m)
	case "put", "upload", "put_file":
		if len(fields) < 3 {
			return "", nil, false, fmt.Errorf("использование: put <локальный файл> <путь на госте>")
		}
		return mk(map[string]any{"local": fields[1], "path": fields[2]})
	case "journal", "log":
		// R4: хвост журнала гостя. Число строк — необязательное.
		m := map[string]any{}
		if rest := strings.TrimSpace(line[len(fields[0]):]); rest != "" {
			if n, aerr := strconv.Atoi(rest); aerr == nil && n > 0 {
				m["lines"] = n
			}
		}
		raw, _ := json.Marshal(m)
		return "journal", raw, false, nil
	case "save", "archive":
		// R4: архив сессии пишется локально. Заметка — остаток строки как есть:
		// это текст для людей, разбирать его на слова незачем.
		note := strings.TrimSpace(line[len(fields[0]):])
		raw, _ := json.Marshal(map[string]any{"note": note})
		return "save", raw, false, nil
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
	case "get", "download", "get_file":
		return "get_file"
	case "put", "upload", "put_file":
		return "put_file"
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
	var notPE []string
	for _, c := range candidates {
		if c == "" {
			continue
		}
		data, rerr := os.ReadFile(c)
		if rerr != nil {
			continue
		}
		// Клиент — Windows. Если билетом уйдёт ELF (linux-бинарник оператора),
		// Windows скажет «невозможно запустить это приложение на вашем ПК»
		// (0xc0000033), и оператор узнает об этом только по звонку клиента.
		if !isPEFile(data) {
			notPE = append(notPE, c)
			continue
		}
		dst := filepath.Join(dir, "fastagent-"+code+".exe")
		if werr := os.WriteFile(dst, data, 0o755); werr != nil {
			return "", werr
		}
		return dst, nil
	}
	if len(notPE) > 0 {
		return "", fmt.Errorf("файл-билет не Windows-программа (не PE): %v — клиент не запустит такой файл; нужен fastagent.exe (remote.ticket_exe)", notPE)
	}
	return "", fmt.Errorf("не найден exe для файла-билета (задайте remote.ticket_exe)")
}

// isPEFile — первая пара байт "MZ": то, что Windows согласится запустить.
func isPEFile(data []byte) bool { return len(data) >= 2 && data[0] == 'M' && data[1] == 'Z' }

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
