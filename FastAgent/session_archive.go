// session_archive.go — R4: архив сессии удалённой диагностики.
//
// Зачем это вообще нужно. Смысл этапа R4 в записке (REMOTE-DIAG.md §7) — «чтобы
// следующий такой случай был дешевле»: удалённая сессия сама по себе исчезает
// вместе с терминалом, а всё ценное в ней — что спросили, что ответил гость,
// какие вердикты вынесла политика и чем это кончилось. Здесь эта картина
// собирается детерминированно, без модели: консоль записывает каждый шаг, гость
// отдаёт свой журнал (там approved и вердикт, которых оператор не видит), и в
// конце получаются два файла.
//
//   Data/logs/remote-<код>-<дата>.txt   — полный текст сессии для человека
//   <память>/projects/remote-<код>-<дата>.md — уроки для Jev (только по явной команде)
//
// Уроки — не «мнение модели о сессии»: это таблица шагов, отказы и журнал гостя
// плюс заметка оператора. Домысливать выводы здесь нечего, а то, что домыслено,
// в общей памяти живёт дольше, чем стоит.

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// archiveEntryMax — сколько вывода одной команды попадает в текст архива.
	// Гость и так режет ответ до 64 КиБ, но архив — это ещё и txt, который
	// открывают в блокноте; 24 КиБ на шаг хватает, а «…обрезано» остаётся
	// честной пометкой, что было больше.
	archiveEntryMax = 24 << 10
	// archiveJournalLines — сколько строк журнала гостя просить.
	archiveJournalLines = 200
	// archiveLessonFailures — сколько отказов попадает в уроки отдельным списком.
	archiveLessonFailures = 40
)

// Параметры импорта уроков. Задаются флагами оператора; пустой каталог памяти
// означает «только txt, в общую память ничего не пишем» — это значение по
// умолчанию там, где Jev нет вовсе (Windows-гость, чужая машина).
var (
	archiveMemoryDir string // корпус памяти (JEV_MEMORY_DIR)
	archiveReindex   bool   // переиндексировать FTS5 и вектора после записи
	archiveJevDir    string // каталог Jev со scripts/
	archiveReindexT  = 20 * time.Minute
)

// archiveEntry — один шаг сессии: что напечатал оператор и что вернул гость.
type archiveEntry struct {
	At   time.Time
	Line string        // строка консоли как есть
	Kind string        // вид команды (diag/read_file/run/get_file/…)
	Res  *remoteResult // nil у локальной пометки
	Note string        // для локальных действий («архив сохранён»)
}

// sessionArchive — растущая запись сессии. Пишется она в конце (и по /save),
// поэтому стоимость шага — только append.
type sessionArchive struct {
	Code         string
	Broker       string
	Started      time.Time
	Entries      []archiveEntry
	GuestHost    string // host из журнала гостя
	GuestLog     string // путь журнала гостя
	GuestJournal string // последний хвост журнала (без шапки host/path/lines)
	dirty        bool   // есть шаги после последнего сохранения
	lastNote     string // заметка последнего сохранения: выход из сессии не должен её терять
}

func newSessionArchive(code, broker string) *sessionArchive {
	return &sessionArchive{Code: code, Broker: broker, Started: time.Now()}
}

// Add — шаг с результатом гостя.
func (a *sessionArchive) Add(line, kind string, res remoteResult) {
	if a == nil {
		return
	}
	r := res
	a.Entries = append(a.Entries, archiveEntry{At: time.Now(), Line: line, Kind: kind, Res: &r})
	a.dirty = true
	if kind == "journal" && res.OK {
		a.takeGuestJournal(res.Out)
	}
}

// Note — локальное действие консоли (например, сохранение архива).
func (a *sessionArchive) Note(line, note string) {
	if a == nil {
		return
	}
	a.Entries = append(a.Entries, archiveEntry{At: time.Now(), Line: line, Note: note})
}

// takeGuestJournal — разобрать ответ гостя на journal: host=, path= и сам хвост.
// Хвост хранится в архиве отдельно, а не только как шаг сессии: save и quit
// забирают его служебно, и операторские шаги «Шагов: N» от этого расти не должны.
func (a *sessionArchive) takeGuestJournal(out string) {
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "host="):
			a.GuestHost = strings.TrimSpace(strings.TrimPrefix(line, "host="))
		case strings.HasPrefix(line, "path="):
			a.GuestLog = strings.TrimSpace(strings.TrimPrefix(line, "path="))
		}
	}
	if at := strings.Index(out, "\n---\n"); at >= 0 {
		a.GuestJournal = strings.TrimSpace(out[at+len("\n---\n"):])
	} else {
		a.GuestJournal = strings.TrimSpace(out)
	}
}

// journalText — последний хвост журнала гостя для раздела «журнал гостя».
func (a *sessionArchive) journalText() string {
	return a.GuestJournal
}

// counts — сколько шагов, сколько отказов и сколько файлов прошло.
func (a *sessionArchive) counts() (steps, refused, got, sent int) {
	for _, e := range a.Entries {
		if e.Res == nil {
			continue
		}
		steps++
		if !e.Res.OK {
			refused++
		}
		switch e.Kind {
		case "get_file":
			if e.Res.OK {
				got++
			}
		case "put_file":
			if e.Res.OK {
				sent++
			}
		}
	}
	return
}

func (a *sessionArchive) duration() time.Duration {
	if len(a.Entries) == 0 {
		return time.Since(a.Started)
	}
	return a.Entries[len(a.Entries)-1].At.Sub(a.Started)
}

// Text — полный архив сессии для человека.
func (a *sessionArchive) Text() string {
	var b strings.Builder
	steps, refused, got, sent := a.counts()

	fmt.Fprintln(&b, "FASTAGENT — АРХИВ СЕССИИ УДАЛЁННОЙ ДИАГНОСТИКИ")
	fmt.Fprintln(&b, strings.Repeat("=", 60))
	fmt.Fprintf(&b, "Сессия:       %s\n", a.Code)
	fmt.Fprintf(&b, "Брокер:       %s\n", a.Broker)
	if a.GuestHost != "" {
		fmt.Fprintf(&b, "Гость:        %s\n", a.GuestHost)
	}
	if a.GuestLog != "" {
		fmt.Fprintf(&b, "Журнал гостя: %s\n", a.GuestLog)
	}
	fmt.Fprintf(&b, "Начало:       %s\n", a.Started.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "Конец:        %s\n", a.Started.Add(a.duration()).Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "Длительность: %s\n", a.duration().Round(time.Second))
	fmt.Fprintf(&b, "Шагов:        %d (отказов и ошибок %d)\n", steps, refused)
	fmt.Fprintf(&b, "Файлов:       получено %d, отправлено %d\n", got, sent)

	fmt.Fprintln(&b, "\nЖУРНАЛ СЕССИИ")
	fmt.Fprintln(&b, strings.Repeat("-", 60))
	for _, e := range a.Entries {
		fmt.Fprintf(&b, "[%s] fa> %s\n", e.At.Format("15:04:05"), e.Line)
		if e.Res == nil {
			if e.Note != "" {
				fmt.Fprintf(&b, "  (%s)\n", e.Note)
			}
			continue
		}
		res := e.Res
		state := "ОК"
		if !res.OK {
			state = "ОТКАЗ/ОШИБКА"
		}
		fmt.Fprintf(&b, "  вид: %s | итог: %s | код %d | %.2f с", e.Kind, state, res.Exit, float64(res.MS)/1000)
		if res.Verdict != "" {
			fmt.Fprintf(&b, " | вердикт: %s", res.Verdict)
		}
		if res.NeedsApproval {
			fmt.Fprintf(&b, " | запрошено подтверждение")
		}
		b.WriteString("\n")
		out := strings.TrimRight(res.Out, "\n")
		if out != "" {
			shown, cut := truncateNote(out, archiveEntryMax)
			for _, line := range strings.Split(shown, "\n") {
				fmt.Fprintf(&b, "    %s\n", line)
			}
			if cut {
				fmt.Fprintf(&b, "    …[вывод обрезан в архиве до %d байт]\n", archiveEntryMax)
			}
		}
	}

	if j := a.journalText(); j != "" {
		fmt.Fprintln(&b, "\nЖУРНАЛ ГОСТЯ (вердикты и подтверждения)")
		fmt.Fprintln(&b, strings.Repeat("-", 60))
		b.WriteString(j)
		b.WriteString("\n")
	}
	if steps == 0 {
		fmt.Fprintln(&b, "\n[i] В сессии не было ни одной команды.")
	}
	return b.String()
}

// Lessons — уроки сессии в разметке памяти: заголовок первого уровня и
// разделы второго, потому что чанкер Jev режет документ по «## ».
func (a *sessionArchive) Lessons(note string) string {
	var b strings.Builder
	steps, refused, got, sent := a.counts()
	host := a.GuestHost
	if host == "" {
		host = "гость без имени"
	}
	fmt.Fprintf(&b, "# Сессия удалённой диагностики %s :: %s :: %s\n\n",
		a.Code, host, a.Started.Format("2006-01-02"))

	b.WriteString("## Что это\n")
	fmt.Fprintf(&b, "Автоматический архив консоли оператора `-remote new` и журнала гостя (R4).\n")
	fmt.Fprintf(&b, "Сессия %s, брокер %s, начало %s, длительность %s.\n",
		a.Code, a.Broker, a.Started.Format("2006-01-02 15:04:05"), a.duration().Round(time.Second))
	fmt.Fprintf(&b, "Шагов: %d, отказов и ошибок: %d, файлов получено: %d, отправлено: %d.\n",
		steps, refused, got, sent)
	if a.GuestHost != "" || a.GuestLog != "" {
		fmt.Fprintf(&b, "Гость: %s, журнал: %s.\n", a.GuestHost, a.GuestLog)
	}
	b.WriteString("Выводы здесь не додуманы: ниже шаги, отказы и журнал гостя как есть, ")
	b.WriteString("оценку даёт заметка оператора.\n")

	b.WriteString("\n## Ход сессии\n\n")
	b.WriteString("| время | команда | итог |\n|---|---|---|\n")
	for _, e := range a.Entries {
		if e.Res == nil {
			continue
		}
		state := "ок"
		if !e.Res.OK {
			state = "отказ"
		}
		line := strings.ReplaceAll(e.Line, "|", "\\|")
		fmt.Fprintf(&b, "| %s | `%s` | %s |\n", e.At.Format("15:04:05"), line, state)
	}

	b.WriteString("\n## Отказы и ошибки\n\n")
	var failures []string
	for _, e := range a.Entries {
		if e.Res == nil || e.Res.OK {
			continue
		}
		line := strings.TrimSpace(e.Res.Out)
		if line == "" {
			line = "(пустой ответ)"
		}
		if i := strings.IndexByte(line, '\n'); i >= 0 {
			line = line[:i]
		}
		failures = append(failures, fmt.Sprintf("- `%s` — %s", e.Line, truncate(line, 200)))
		if len(failures) >= archiveLessonFailures {
			break
		}
	}
	if len(failures) == 0 {
		b.WriteString("Отказов и ошибок не было.\n")
	} else {
		b.WriteString(strings.Join(failures, "\n"))
		b.WriteString("\n")
	}

	b.WriteString("\n## Журнал гостя (verdict / approved)\n\n")
	if j := a.journalText(); j != "" {
		fmt.Fprintf(&b, "```\n%s\n```\n", truncate(j, 4000))
	} else {
		b.WriteString("Журнал гостя в архив не попал (гость не отдал его до закрытия сессии).\n")
	}

	b.WriteString("\n## Заметка оператора\n\n")
	if strings.TrimSpace(note) == "" {
		b.WriteString("Не заполнена: оператор не добавил выводов к этой сессии.\n")
	} else {
		b.WriteString(strings.TrimSpace(note))
		b.WriteString("\n")
	}
	return b.String()
}

// WriteText — положить txt-архив в каталог журналов.
func (a *sessionArchive) WriteText(dir string) (string, error) {
	if a == nil {
		return "", fmt.Errorf("нет сессии")
	}
	if dir == "" {
		dir = "."
	}
	name := fmt.Sprintf("remote-%s-%s.txt", a.Code, a.Started.Format("20060102-150405"))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(a.Text()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// WriteLessons — положить уроки в корпус памяти.
func (a *sessionArchive) WriteLessons(dir, note string) (string, error) {
	if a == nil {
		return "", fmt.Errorf("нет сессии")
	}
	if dir == "" {
		return "", fmt.Errorf("каталог памяти не задан")
	}
	sub := filepath.Join(dir, "projects")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("remote-%s-%s.md", a.Code, a.Started.Format("20060102-150405"))
	path := filepath.Join(sub, name)
	if err := os.WriteFile(path, []byte(a.Lessons(note)), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// saveArchive — записать архив и, если задано, уроки; рассказать, что дальше.
// Возвращает true, если txt-архив действительно записан.
func saveArchive(a *sessionArchive, note string) bool {
	if a == nil {
		return false
	}
	// R5: контроль секретов ДО записи в общую память. Архив — это txt на диск
	// оператора, а уроки уходят в корпус, который читают все агенты; поэтому
	// при находке txt пишем (разбор нужен человеку), а в память — нет.
	findings := scanSecretsText("архив", []byte(a.Text()), configuredSecretRefs(sessionCfg))
	if strings.TrimSpace(archiveMemoryDir) != "" {
		findings = append(findings, scanSecretsText("уроки", []byte(a.Lessons(note)), configuredSecretRefs(sessionCfg))...)
	}
	txt, err := a.WriteText(logsDir)
	if err != nil {
		fmt.Printf("[!] архив не записан: %v\n", err)
		return false
	}
	a.dirty = false
	a.lastNote = note
	a.Note("/save", "архив записан: "+txt)
	fmt.Printf("[OK] архив сессии: %s\n", txt)
	if len(findings) > 0 {
		fmt.Println("[!] ОПАСНО: в архиве найдены секреты — в память не пишем:")
		for _, f := range findings {
			fmt.Println("    " + f)
		}
		fmt.Println("    Уберите секреты из вывода и повторите отчёт.")
		return true
	}
	fmt.Println("[OK] секретов в архиве и уроках не найдено.")

	if strings.TrimSpace(archiveMemoryDir) == "" {
		fmt.Println("[i] Уроки в память не пишутся: каталог памяти не задан (-memory-dir).")
		return true
	}
	md, merr := a.WriteLessons(archiveMemoryDir, note)
	if merr != nil {
		fmt.Printf("[!] уроки не записаны: %v\n", merr)
		return true
	}
	fmt.Printf("[OK] уроки для Jev: %s\n", md)
	if archiveReindex {
		reindexMemory()
	} else {
		fmt.Println("[i] Чтобы память увидела этот файл, переиндексируйте её:")
		fmt.Println("    python3 " + filepath.Join(archiveJevDir, "scripts", "index_feeds.py") + " --feed memory")
		fmt.Println("    python3 " + filepath.Join(archiveJevDir, "scripts", "build_memory_vectors.py") + " --namespace memory")
		fmt.Println("    (или тот же -save с флагом -reindex)")
	}
	return true
}

// reindexMemory — пересобрать FTS5 и вектора памяти. Это команды на машине
// оператора, поэтому их результат печатается как есть: «выполнено» без вывода
// было бы обещанием, а не фактом.
func reindexMemory() {
	steps := [][]string{
		{filepath.Join(archiveJevDir, "scripts", "index_feeds.py"), "--feed", "memory"},
		{filepath.Join(archiveJevDir, "scripts", "build_memory_vectors.py"), "--namespace", "memory"},
	}
	for _, args := range steps {
		fmt.Printf("[i] запускаю: python3 %s\n", strings.Join(args, " "))
		cmd := exec.Command("python3", args...)
		cmd.Dir = archiveJevDir
		out, err := runWithTimeout(cmd, archiveReindexT)
		text := strings.TrimSpace(string(out))
		if text != "" {
			for _, line := range strings.Split(text, "\n") {
				fmt.Println("    " + line)
			}
		}
		if err != nil {
			fmt.Printf("[!] %s: %v\n", filepath.Base(args[0]), err)
			return
		}
	}
	fmt.Println("[OK] память переиндексирована (FTS5 + вектора).")
}

// runWithTimeout — как exec.Command.Run, но с потолком: индексация векторов
// может растянуться, а консоль оператора не должна зависнуть навсегда.
func runWithTimeout(cmd *exec.Cmd, timeout time.Duration) ([]byte, error) {
	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		out, err := cmd.Output()
		done <- result{out, err}
	}()
	select {
	case r := <-done:
		return r.out, r.err
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil, fmt.Errorf("превышен таймаут %s", timeout)
	}
}

// truncateNote — обрезать с честной пометкой: возвращает текст и признак того,
// что что-то отрезано.
func truncateNote(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	cut := s[:n]
	// Режем по границе руны, иначе в архив попадёт половина UTF-8 символа.
	// Именно последняя руна: байт-начало без своих продолжений — такой же
	// битый хвост, как и висячее продолжение (проверено тестом: «я» на 21-м
	// байте оставляла 0xD1).
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut, true
}

// tailLines — последние n строк журнала: гость отдаёт хвост, а не весь файл.
func tailLines(text string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
