// operator_ui.go — R5: меню оператора удалённой сессии.
//
// Живой выезд выглядит так: оператор в AnyDesk, клиент рядом, файл уже передан
// и запущен. Время идёт на разговор с клиентом, а не на набор команд. Поэтому
// после пары консоль показывает меню и одно нажатие запускает регламент:
// старт (эталон и секреты), диагностику по сценарию, починки и отчёт.
//
// Правила интерфейса:
//   - каждый пункт — одно действие, результат печатается сразу («[OK] …»);
//   - экран не забивается выводом команд: в архив уходит всё, на экран — итог;
//   - стартовый блок нельзя пропустить: пока файл не сверен с эталоном и
//     ключи не проверены, диагностика и починки заблокированы;
//   - про секреты печатается только факт находки, никогда — значение.

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Флаги оператора: -plain оставляет старую консоль команд, -scenario выбирает
// регламент из scenarios/. Значения ставит main() до runRemote.
var (
	operatorPlain        bool
	operatorScenarioName string
	// sessionCfg — копия рабочего конфига для проверки секретов в архиве:
	// saveArchive зовётся из глубины консоли, где cfg не передаётся.
	sessionCfg Config
)

// opState — всё, что нужно меню: канал к гостю, архив и результаты проверок.
type opState struct {
	cfg    Config
	base   string
	secret string
	code   string
	key    []byte
	events <-chan remoteEvent
	arch   *sessionArchive
	reader *bufio.Reader
	sc     *scenario

	mandatoryOK bool
	unsafe      string
	results     []checkResult
	fixesDone   []string
}

// runOperatorUI — точка входа после пары. Возвращает код выхода консоли.
func runOperatorUI(cfg Config, base, secret, code string, key []byte, events <-chan remoteEvent, arch *sessionArchive) int {
	st := &opState{
		cfg: cfg, base: base, secret: secret, code: code,
		key: key, events: events, arch: arch,
		reader: bufio.NewReader(os.Stdin),
	}
	if name := strings.TrimSpace(operatorScenarioName); name != "" {
		sc, err := loadScenario(name)
		if err != nil {
			fmt.Printf("[!] %v\n", err)
			if names := scenarioNames(); len(names) > 0 {
				fmt.Printf("[i] рядом есть сценарии: %s\n", strings.Join(names, ", "))
			}
		} else {
			st.sc = sc
		}
	} else if sc, err := loadScenario("standard-windows"); err == nil {
		st.sc = sc
	}

	fmt.Println()
	fmt.Println("===========================================================")
	fmt.Printf("  СЕССИЯ %s · уровень L2 (удалённо)\n", code)
	if st.sc != nil {
		fmt.Printf("  Регламент: %s (проверок %d, починок %d)\n", st.sc.Title, len(st.sc.Checks), len(st.sc.Fixes))
	} else {
		fmt.Println("  Регламент не загружен: работает только отчёт и выход.")
	}
	fmt.Println("  Всё, что видно здесь, попадает в архив сессии.")
	fmt.Println("===========================================================")

	st.mandatoryOK = opStart(st)
	for {
		opPrintMenu(st)
		fmt.Print("\nВыбор: ")
		line, err := st.reader.ReadString('\n')
		if err != nil {
			return opQuit(st)
		}
		switch strings.TrimSpace(line) {
		case "1":
			st.mandatoryOK = opStart(st)
		case "2":
			if !st.mandatoryOK {
				fmt.Println("[!] Пока стартовый блок не пройден, диагностика заблокирована (пункт 1).")
				continue
			}
			opDiagnostics(st)
		case "3":
			if !st.mandatoryOK {
				fmt.Println("[!] Пока стартовый блок не пройден, починки заблокированы (пункт 1).")
				continue
			}
			opFixes(st)
		case "4":
			opReport(st)
		case "5", "q", "quit", "exit":
			return opQuit(st)
		case "", "?":
			// пустая строка — перепечатать меню
		default:
			fmt.Println("Введите номер 1–5.")
		}
	}
}

// opPrintMenu — короткая шапка меню: без сценария пункты 2–3 не работают.
func opPrintMenu(st *opState) {
	fmt.Println()
	fmt.Println(strings.Repeat("─", 59))
	fmt.Println(" 1  Старт: эталон файла, секреты, журнал гостя")
	if st.sc != nil {
		fmt.Printf(" 2  Диагностика: %s (%d проверок)\n", st.sc.Title, len(st.sc.Checks))
		fmt.Printf(" 3  Починки (%d)\n", len(st.sc.Fixes))
	} else {
		fmt.Println(" 2  Диагностика (сценарий не загружен)")
		fmt.Println(" 3  Починки (сценарий не загружен)")
	}
	fmt.Println(" 4  Отчёт: архив, уроки, контроль секретов")
	fmt.Println(" 5  Выход")
}

// opStart — обязательный блок. Возвращает false, если что-то не сошлось:
// тогда меню оставляет доступными только старт, отчёт и выход.
func opStart(st *opState) bool {
	fmt.Println()
	fmt.Println("── СТАРТ: обязательный блок " + strings.Repeat("─", 30))
	ok := true

	// 1. Эталон: тот ли файл ушёл клиенту.
	src := strings.TrimSpace(st.cfg.Remote.TicketExe)
	ticket := ""
	if src != "" {
		if p, err := remoteTicketFile(st.cfg, st.code); err == nil {
			ticket = p
		} else {
			ticket = src
		}
	}
	if ticket == "" {
		ticket = filepath.Join(baseDir, "FastAgent", "fastagent.exe")
		fmt.Println(" [--] -ticket-exe не задан: сверяется файл из репозитория.")
	}
	if vok, detail := verifyTicket(ticket); vok {
		fmt.Printf(" [OK] эталон файла: %s\n", detail)
	} else {
		fmt.Printf(" [!] эталон файла: %s\n", detail)
		ok = false
	}

	// 2. Секреты в том, что уходит клиенту.
	refs := configuredSecretRefs(st.cfg)
	if data, err := os.ReadFile(ticket); err != nil {
		fmt.Printf(" [!] секреты в файле клиента: файл не прочитан: %v\n", err)
		ok = false
	} else if found := scanSecretsBinary("файл клиента", data, refs); len(found) > 0 {
		for _, f := range found {
			fmt.Printf(" [!] секреты в файле клиента: %s\n", f)
		}
		ok = false
	} else {
		fmt.Printf(" [OK] секреты в файле клиента: не найдено (проверено ключей: %d)\n", len(refs))
	}

	// 3. Журнал гостя: где он, сколько весит и нет ли в нём ключей.
	if res, jok := remoteJournal(st.base, st.secret, st.code, st.key, st.events, archiveJournalLines); !jok || !res.OK {
		fmt.Println(" [!] журнал гостя: не получен (сессия закрывается?)")
		ok = false
	} else {
		st.arch.takeGuestJournal(res.Out)
		host, path, lines, size := parseJournalHeader(res.Out)
		found := scanSecretsText("журнал гостя", []byte(res.Out), refs)
		where := firstNonEmpty(host, "гость") + ": " + firstNonEmpty(path, "путь неизвестен")
		what := fmt.Sprintf("%s, строк %d, %s", where, lines, humanBytes(size))
		switch {
		case len(found) > 0:
			for _, f := range found {
				fmt.Printf(" [!] журнал гостя: %s\n", f)
			}
			ok = false
		case size <= 0:
			fmt.Printf(" [--] журнал гостя: %s (размер не отдан)\n", what)
		default:
			fmt.Printf(" [OK] журнал гостя: %s — чисто\n", what)
		}
	}

	// 4. Архив проверяется при отчёте: до сохранения его ещё нет.
	fmt.Println(" [--] контроль архива и уроков: выполняется при отчёте (пункт 4)")

	if !ok {
		st.unsafe = "стартовый блок не пройден"
		fmt.Println()
		fmt.Println(" [!] СТАРТ НЕ ПРОЙДЕН: диагностика и починки заблокированы.")
		fmt.Println("     Устраните причину и повторите пункт 1.")
	} else {
		st.unsafe = ""
	}
	return ok
}

// parseJournalHeader — разобрать шапку ответа журнала (host/path/lines/size).
func parseJournalHeader(out string) (host, path string, lines int, size int64) {
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "host="):
			host = strings.TrimSpace(strings.TrimPrefix(line, "host="))
		case strings.HasPrefix(line, "path="):
			path = strings.TrimSpace(strings.TrimPrefix(line, "path="))
		case strings.HasPrefix(line, "lines="):
			fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(line, "lines=")), "%d", &lines)
		case strings.HasPrefix(line, "size="):
			fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(line, "size=")), "%d", &size)
		}
		if line == "---" {
			break
		}
	}
	return
}

// opDiagnostics — прогнать сценарий и напечатать итог по приоритетам.
func opDiagnostics(st *opState) {
	if st.sc == nil {
		fmt.Println("[!] Сценарий не загружен: положите scenarios/*.json рядом с бинарём.")
		return
	}
	fmt.Printf("\n── ДИАГНОСТИКА: %s ──\n", st.sc.Title)
	st.results = nil
	for i, c := range st.sc.Checks {
		fmt.Printf("[%d/%d] %s … ", i+1, len(st.sc.Checks), c.Title)
		res := opRunCheck(st, c)
		st.results = append(st.results, res)
		if res.OK {
			fmt.Printf("[OK] %s\n", res.Message)
			continue
		}
		fmt.Printf("[!] %s\n", res.Message)
		opShowMatches(c, res.Output)
	}
	probs := sortedProblems(st.results)
	fmt.Printf("\n── ИТОГ: проверок %d, проблем %d ──\n", len(st.results), len(probs))
	if len(probs) == 0 {
		fmt.Println("  Замечаний нет: машина по этому регламенту в порядке.")
	}
	for _, p := range probs {
		fmt.Printf("  [%s] %s: %s\n", priorityLabel(p.Priority), p.Title, p.Message)
	}
	st.arch.Note("меню: диагностика", fmt.Sprintf("%d проверок, проблем %d", len(st.results), len(probs)))
}

// opRunCheck — выполнить команды проверки и оценить правила.
func opRunCheck(st *opState, c scenarioCheck) checkResult {
	out, allOK, denied, answered := st.runCommands(c.Commands, time.Duration(c.TimeoutSec)*time.Second)
	if !answered {
		res := checkResult{ID: c.ID, Title: c.Title, Priority: c.Priority,
			Message: "гость не ответил — проверка не выполнена"}
		st.arch.Add(c.Title, "run", remoteResult{OK: false, Exit: -1, Out: res.Message})
		return res
	}
	res := c.evaluate(out, allOK, denied)
	exit := 0
	if !allOK || denied {
		exit = 1
	}
	st.arch.Add(c.Title, "run", remoteResult{OK: res.OK, Exit: exit, Out: out})
	return res
}

// runCommands — отправить команды проверки по одной и собрать вывод.
func (st *opState) runCommands(cmds []string, timeout time.Duration) (out string, allOK, denied, answered bool) {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	var b strings.Builder
	allOK = true
	for _, cmd := range cmds {
		args, _ := json.Marshal(map[string]any{"command": cmd})
		id, err := remoteSendCmd(st.base, st.secret, st.code, st.key, "run", args, false)
		if err != nil {
			b.WriteString("команда не отправлена: " + err.Error() + "\n")
			return b.String(), false, false, true
		}
		res, ok := remoteWaitResult(st.events, id, timeout)
		if !ok {
			return b.String(), false, false, false
		}
		if res.NeedsApproval {
			return b.String(), false, true, true
		}
		if !res.OK {
			allOK = false
		}
		b.WriteString(res.Out)
		if !strings.HasSuffix(res.Out, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String(), allOK, false, true
}

// opShowMatches — несколько строк вывода, из-за которых проверка не прошла.
func opShowMatches(c scenarioCheck, out string) {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	shown := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fmt.Printf("      %s\n", truncate(line, 160))
		shown++
		if shown >= 5 {
			rest := len(lines) - shown
			if rest > 0 {
				fmt.Printf("      … ещё строк: %d (полностью — в архиве)\n", rest)
			}
			return
		}
	}
}

// opFixes — выбрать починку номером, подтвердить один раз, выполнить.
func opFixes(st *opState) {
	if st.sc == nil || len(st.sc.Fixes) == 0 {
		fmt.Println("[i] В сценарии нет починок.")
		return
	}
	fmt.Println("\n── ПОЧИНКИ ──")
	for i, f := range st.sc.Fixes {
		mark := ""
		if f.NeedsAdmin {
			mark += " [права администратора]"
		}
		if st.fixDone(f.ID) {
			mark += " (уже выполнялась)"
		}
		fmt.Printf(" %d  %s%s\n", i+1, f.Title, mark)
	}
	fmt.Println(" 0  назад")
	fmt.Print("Выбор: ")
	line, err := st.reader.ReadString('\n')
	if err != nil {
		return
	}
	n, perr := strconv.Atoi(strings.TrimSpace(line))
	if perr != nil || n < 0 || n > len(st.sc.Fixes) {
		fmt.Println("Нет такого пункта.")
		return
	}
	if n == 0 {
		return
	}
	f := st.sc.Fixes[n-1]
	fmt.Printf("\nПочинка: %s\n", f.Title)
	for _, cmd := range f.Commands {
		fmt.Printf("  $ %s\n", cmd)
	}
	if f.NeedsAdmin {
		fmt.Println("[i] Нужны права администратора на машине клиента: без них команда вернёт код 740.")
	}
	q := strings.TrimSpace(f.Confirm)
	if q == "" {
		q = "Выполнить?"
	}
	if !opConfirm(st.reader, q+" (y/n): ") {
		fmt.Println("Отменено оператором: на машине клиента ничего не изменилось.")
		st.arch.Note("починка: "+f.Title, "отменена оператором")
		return
	}
	// Подтверждение уже получено здесь, у оператора: команды уходят на гость с
	// approved=true. Политика гостя всё равно проверит каждую и вправе отказать.
	failed := false
	for _, cmd := range f.Commands {
		args, _ := json.Marshal(map[string]any{"command": cmd})
		id, serr := remoteSendCmd(st.base, st.secret, st.code, st.key, "run", args, true)
		if serr != nil {
			fmt.Printf("  [!] команда не отправлена: %v\n", serr)
			failed = true
			break
		}
		res, ok := remoteWaitResult(st.events, id, 45*time.Minute)
		if !ok {
			failed = true
			break
		}
		st.arch.Add(cmd, "run", res)
		if res.OK {
			fmt.Printf("  [OK] %s → код %d (%.1f с)\n", cmd, res.Exit, float64(res.MS)/1000)
		} else {
			fmt.Printf("  [!] %s → код %d: %s\n", cmd, res.Exit, truncate(firstLine(res.Out), 200))
			failed = true
		}
	}
	if !failed {
		st.fixesDone = append(st.fixesDone, f.ID)
		fmt.Println("\nПочинка выполнена. Если проблема была в проверке — повторите пункт 2.")
	} else {
		fmt.Println("\n[!] Починка завершилась с ошибкой — см. код и вывод выше.")
	}
}

func (st *opState) fixDone(id string) bool {
	for _, f := range st.fixesDone {
		if f == id {
			return true
		}
	}
	return false
}

// opReport — сохранить архив с контролем секретов и показать сводку.
func opReport(st *opState) {
	fmt.Println("\n── ОТЧЁТ ──")
	pullGuestJournal(st.base, st.secret, st.code, st.key, st.events, st.arch)
	if !saveArchive(st.arch, opAutoNote(st)) {
		fmt.Println("[!] Отчёт не записан.")
		return
	}
	probs := sortedProblems(st.results)
	fmt.Printf("\nПроблем: %d\n", len(probs))
	for _, p := range probs {
		fmt.Printf("  [%s] %s: %s\n", priorityLabel(p.Priority), p.Title, p.Message)
	}
	if len(st.fixesDone) > 0 {
		fmt.Printf("Выполнено починок: %d\n", len(st.fixesDone))
	}
	fmt.Println("\nЧто сказать клиенту:")
	for _, line := range strings.Split(opClientText(st), "\n") {
		fmt.Println("  " + line)
	}
}

// opAutoNote — заметка оператора без ручного ввода: факты сессии.
func opAutoNote(st *opState) string {
	var b strings.Builder
	if st.sc != nil {
		fmt.Fprintf(&b, "Сессия по регламенту «%s». ", st.sc.Title)
	}
	fmt.Fprintf(&b, "Проверок: %d, проблем: %d.", len(st.results), len(sortedProblems(st.results)))
	var titles []string
	for _, p := range sortedProblems(st.results) {
		titles = append(titles, p.Title)
	}
	if len(titles) > 0 {
		fmt.Fprintf(&b, " Проблемы: %s.", strings.Join(titles, "; "))
	}
	if len(st.fixesDone) > 0 {
		fmt.Fprintf(&b, " Выполнено починок: %s.", strings.Join(st.fixesDone, ", "))
	}
	return b.String()
}

// opClientText — что оператор говорит клиенту в конце: коротко и без жаргона.
func opClientText(st *opState) string {
	probs := sortedProblems(st.results)
	if len(probs) == 0 {
		return "Проверка закончена: замечаний нет.\n" +
			"Если проблема повторится — напишите, посмотрим глубже."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Проверка закончена. Нашлось замечаний: %d.\n", len(probs))
	fmt.Fprintf(&b, "Главное: %s.\n", probs[0].Message)
	if len(st.fixesDone) > 0 {
		fmt.Fprintf(&b, "Часть уже исправлена (%d), остальное - в отчёте.\n", len(st.fixesDone))
	} else {
		b.WriteString("Дальше - по отчёту: что можно исправить сейчас и что требует перезагрузки.\n")
	}
	return b.String()
}

// opQuit — журнал, закрытие, архив. Сессию закрываем до записи файла, чтобы
// гость вышел, но журнал уже был в руках (иначе вердикты пропадут).
func opQuit(st *opState) int {
	fmt.Println("\nЗавершаю сессию…")
	pullGuestJournal(st.base, st.secret, st.code, st.key, st.events, st.arch)
	remoteCloseSession(st.base, st.secret, st.code)
	if st.arch.dirty {
		saveArchive(st.arch, st.arch.lastNote)
	}
	fmt.Println("[i] сессия закрыта.")
	return 0
}

// opConfirm — подтверждение оператора: Enter не считается согласием.
func opConfirm(reader *bufio.Reader, prompt string) bool {
	fmt.Print(prompt)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "да", "д":
		return true
	}
	return false
}

// firstLine — первая непустая строка вывода.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// sortStrings — стабильная сортировка для отчётов и тестов.
func sortStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
