// scenario.go — R5: регламент сессии оператора.
//
// Зачем. В живом выезде оператор сидит в AnyDesk с клиентом и не имеет времени
// набирать `sudo`, `powershell -c` и пути к журналам. Регламент должен быть
// предсказуемым: список проверок и починок, который читается из JSON рядом с
// бинарём и запускается из меню. Сценарий — это данные, а не код: новая
// проверка добавляется текстом, без пересборки.
//
// Сценарий описывает:
//   checks — что проверить (одна-две команды чтения, правила «проблема если»);
//   fixes  — что можно починить (список команд, одно подтверждение оператора).
//
// Правила проверки намеренно простые: регулярное выражение и «проблема, если
// найдено/не найдено». Модель здесь не участвует — оценка детерминированная,
// иначе «положительный результат» на экране стал бы мнением модели.

package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// scenario — один регламент. Имя файла — <name>.json в каталоге scenarios/;
// кейсы по клиентам лежат в подкаталогах-разделах: scenarios/medoc/<кейс>.json.
type scenario struct {
	Name         string             `json:"name"`
	Title        string             `json:"title"`
	Section      string             `json:"section,omitempty"`       // medoc, network, …
	SectionTitle string             `json:"section_title,omitempty"` // «МЕДОК», «Сеть», …
	Checks       []scenarioCheck    `json:"checks"`
	Fixes        []scenarioFix      `json:"fixes"`
	Practice     []scenarioPractice `json:"practice,omitempty"`
	// Knowledge — паспорт прожитого случая. Он ставится ПОСЛЕ работы, когда
	// причина найдена и починка проверена; у предполётного чек-листа его нет.
	Knowledge *scenarioKnowledge `json:"knowledge,omitempty"`
}

// scenarioKnowledge — по чему сценарий узнаётся при рецидиве: машина, дата,
// симптом. Это не «красивое описание», а ключ поиска: если через месяц та же
// станция снова не видит сервер, оператор должен увидеть, что этот случай уже
// был, и что именно тогда помогло.
type scenarioKnowledge struct {
	Host      string `json:"host,omitempty"` // имя компьютера
	Date      string `json:"date,omitempty"` // 2026-11-19
	Symptom   string `json:"symptom"`        // словами клиента
	RootCause string `json:"root_cause,omitempty"`
	Fix       string `json:"fix,omitempty"`
	Verified  bool   `json:"verified,omitempty"` // починка проверена на месте
	Session   string `json:"session,omitempty"`  // архив сессии remote-<код>-<дата>
}

// scenarioCheck — одна проверка. Команды выполняются на госте по политике
// гостя: только чтение, без подтверждения.
type scenarioCheck struct {
	ID           string         `json:"id"`
	Title        string         `json:"title"`
	Priority     int            `json:"priority"`      // 1 — важное, 2 — среднее, 3 — справочное
	TimeoutSec   int            `json:"timeout_sec"`   // 0 — 120 с
	IgnoreExit   bool           `json:"ignore_exit"`   // dir по пустому каталогу возвращает 1
	EmptyProblem bool           `json:"empty_problem"` // пустой вывод — это проблема
	Hint         string         `json:"hint,omitempty"`
	Commands     []string       `json:"commands"`
	Rules        []scenarioRule `json:"rules"`
}

// scenarioPractice — практика решения: текст для оператора, который знает этот
// класс проблем. Показывается после диагностики, только когда есть проблемы;
// when ограничивает показ проверками, которые провалились.
type scenarioPractice struct {
	When  []string `json:"when,omitempty"`
	Title string   `json:"title"`
	Text  string   `json:"text"`
}

// scenarioRule — «проблема, если regex найден (или не найден)».
// {n} в сообщении заменяется числом совпадений.
type scenarioRule struct {
	Regex     string `json:"regex"`
	ProblemIf string `json:"problem_if"` // "found" (по умолчанию) или "absent"
	Message   string `json:"message"`
	OKMessage string `json:"ok_message"`
}

// scenarioFix — починка: оператор выбирает её номером, подтверждает один раз.
// recheck — проверки, которые нужно повторить сразу после успешной починки:
// «починили — убедились», а не «починили — и надеемся».
type scenarioFix struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Confirm    string   `json:"confirm"`
	NeedsAdmin bool     `json:"needs_admin"`
	Commands   []string `json:"commands"`
	Recheck    []string `json:"recheck,omitempty"`
}

// checkResult — итог проверки для экрана и отчёта.
type checkResult struct {
	ID       string
	Title    string
	Priority int
	OK       bool
	Message  string
	Output   string
}

// manifest — эталон файла, который уходит клиенту: размер и sha256.
// Пишется scripts/write-scenario-manifest.sh при сборке.
type manifest struct {
	GuestExe struct {
		Name   string `json:"name"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	} `json:"guest_exe"`
}

func scenarioDir() string { return filepath.Join(baseDir, "scenarios") }

// loadScenario читает сценарий по имени (без .json) или по полному пути.
func loadScenario(name string) (*scenario, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "standard"
	}
	path := name
	if !strings.HasSuffix(name, ".json") {
		path = filepath.Join(scenarioDir(), name+".json")
	} else if !filepath.IsAbs(name) {
		// «medoc/imap-ukrnet.json» — путь внутри scenarios/; относительный путь,
		// который существует в рабочем каталоге, уважаем как есть.
		if _, statErr := os.Stat(name); statErr != nil {
			name = filepath.Join(scenarioDir(), name)
		}
		path = name
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("сценарий %s: %v", path, err)
	}
	var sc scenario
	if err := json.Unmarshal(data, &sc); err != nil {
		return nil, fmt.Errorf("сценарий %s: %v", path, err)
	}
	if len(sc.Checks) == 0 {
		return nil, fmt.Errorf("сценарий %s: нет проверок", path)
	}
	if strings.TrimSpace(sc.Section) == "" {
		sc.Section = "standard"
	}
	if strings.TrimSpace(sc.SectionTitle) == "" {
		sc.SectionTitle = sc.Section
	}
	ids := make(map[string]bool, len(sc.Checks))
	for i := range sc.Checks {
		c := &sc.Checks[i]
		if strings.TrimSpace(c.ID) == "" || len(c.Commands) == 0 {
			return nil, fmt.Errorf("сценарий %s: проверка %d без id или команд", path, i+1)
		}
		if ids[c.ID] {
			return nil, fmt.Errorf("сценарий %s: проверка %s объявлена дважды", path, c.ID)
		}
		ids[c.ID] = true
		if c.Priority < 1 || c.Priority > 3 {
			c.Priority = 2
		}
		if c.TimeoutSec <= 0 {
			c.TimeoutSec = 120
		}
		for _, r := range c.Rules {
			if strings.TrimSpace(r.Regex) == "" {
				return nil, fmt.Errorf("сценарий %s: проверка %s: пустой regex", path, c.ID)
			}
			if _, err := regexp.Compile(r.Regex); err != nil {
				return nil, fmt.Errorf("сценарий %s: проверка %s: regex: %v", path, c.ID, err)
			}
		}
	}
	for i := range sc.Fixes {
		f := &sc.Fixes[i]
		if strings.TrimSpace(f.ID) == "" || len(f.Commands) == 0 {
			return nil, fmt.Errorf("сценарий %s: починка %d без id или команд", path, i+1)
		}
		for _, id := range f.Recheck {
			if !ids[id] {
				return nil, fmt.Errorf("сценарий %s: починка %s повторяет неизвестную проверку %q", path, f.ID, id)
			}
		}
	}
	if k := sc.Knowledge; k != nil {
		// Паспорт без симптома бесполезен: по нему случай и узнаётся.
		if strings.TrimSpace(k.Symptom) == "" {
			return nil, fmt.Errorf("сценарий %s: в knowledge не задан symptom", path)
		}
		if d := strings.TrimSpace(k.Date); d != "" {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				return nil, fmt.Errorf("сценарий %s: knowledge.date = %q, нужен формат 2006-01-02", path, d)
			}
		}
		// «Проверено» без причины и починки — это обещание, а не опыт.
		if k.Verified && (strings.TrimSpace(k.RootCause) == "" || strings.TrimSpace(k.Fix) == "") {
			return nil, fmt.Errorf("сценарий %s: knowledge.verified требует root_cause и fix", path)
		}
	}
	for i := range sc.Practice {
		p := &sc.Practice[i]
		if strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Text) == "" {
			return nil, fmt.Errorf("сценарий %s: практика %d без заголовка или текста", path, i+1)
		}
		for _, id := range p.When {
			if !ids[id] {
				return nil, fmt.Errorf("сценарий %s: практика %q ссылается на неизвестную проверку %q", path, p.Title, id)
			}
		}
	}
	return &sc, nil
}

// scenarioEntry — строка каталога: что оператор видит в выборе сценария.
type scenarioEntry struct {
	Name         string
	Title        string
	Section      string
	SectionTitle string
	Learned      bool // у кейса есть паспорт: это прожитый случай, а не чек-лист
}

// scenarioCatalog — все сценарии рядом с бинарём, включая подкаталоги-разделы
// (scenarios/medoc/*.json). Файл, который не загрузился, в каталог не попадает:
// битый кейс не должен ломать меню, но и молча исчезать из списка — тоже.
func scenarioCatalog() []scenarioEntry {
	root := scenarioDir()
	var out []scenarioEntry
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".json") || d.Name() == "manifest.json" {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		sc, lerr := loadScenario(rel)
		if lerr != nil {
			return nil
		}
		out = append(out, scenarioEntry{
			Name:         strings.TrimSuffix(filepath.ToSlash(rel), ".json"),
			Title:        sc.Title,
			Section:      sc.Section,
			SectionTitle: sc.SectionTitle,
			Learned:      sc.Knowledge != nil,
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Section != out[j].Section {
			return out[i].Section < out[j].Section
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// scenarioNames — что лежит рядом: для подсказки, если сценарий не найден.
func scenarioNames() []string {
	var names []string
	for _, e := range scenarioCatalog() {
		names = append(names, e.Name)
	}
	return names
}

// loadManifest — эталон для сверки файла клиента.
func loadManifest() (*manifest, error) {
	data, err := os.ReadFile(filepath.Join(scenarioDir(), "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("эталон не найден (%s): %v", filepath.Join(scenarioDir(), "manifest.json"), err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("эталон: %v", err)
	}
	if m.GuestExe.SHA256 == "" || m.GuestExe.Size <= 0 {
		return nil, fmt.Errorf("эталон: пустые size/sha256")
	}
	return &m, nil
}

// hits — сколько раз regex совпал в выводе.
func (r scenarioRule) hits(re *regexp.Regexp, out string) int {
	if re == nil {
		return 0
	}
	return len(re.FindAllStringIndex(out, -1))
}

// evaluate превращает вывод команды в вердикт проверки. Ошибка команды —
// тоже проблема: «команда не выполнилась» нельзя выдавать за «всё хорошо».
func (c scenarioCheck) evaluate(out string, allOK, denied bool) checkResult {
	res := checkResult{ID: c.ID, Title: c.Title, Priority: c.Priority, Output: out}
	var problems, oks []string
	if denied {
		problems = append(problems, "гость ответил «нужно подтверждение» — проверка не выполнена")
	}
	if !allOK && !c.IgnoreExit {
		problems = append(problems, "команда завершилась с ошибкой")
	}
	if c.EmptyProblem && strings.TrimSpace(out) == "" {
		problems = append(problems, "пустой вывод — данных нет")
	}
	for _, r := range c.Rules {
		re, err := regexp.Compile(r.Regex)
		if err != nil {
			continue
		}
		n := r.hits(re, out)
		problem := n > 0
		if r.ProblemIf == "absent" {
			problem = n == 0
		}
		if problem {
			msg := strings.TrimSpace(r.Message)
			if msg == "" {
				msg = "проверка не пройдена"
			}
			problems = append(problems, strings.ReplaceAll(msg, "{n}", fmt.Sprint(n)))
		} else if strings.TrimSpace(r.OKMessage) != "" {
			oks = append(oks, strings.ReplaceAll(r.OKMessage, "{n}", fmt.Sprint(n)))
		}
	}
	if len(problems) > 0 {
		res.OK = false
		res.Message = strings.Join(problems, "; ")
		return res
	}
	res.OK = true
	if len(oks) > 0 {
		res.Message = strings.Join(oks, "; ")
	} else {
		res.Message = "в порядке"
	}
	return res
}

// priorityLabel — как сказать про приоритет человеку.
func priorityLabel(p int) string {
	switch p {
	case 1:
		return "важно"
	case 2:
		return "средне"
	default:
		return "справочно"
	}
}

// sortedProblems — проблемы по приоритету: сначала то, из-за чего пришли.
func sortedProblems(results []checkResult) []checkResult {
	var out []checkResult
	for _, r := range results {
		if !r.OK {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out
}

// checkByID — проверка по идентификатору: починка адресует повторную проверку
// по id, а не по номеру в списке (номер меняется при правке сценария).
func (sc *scenario) checkByID(id string) (scenarioCheck, bool) {
	if sc == nil {
		return scenarioCheck{}, false
	}
	for _, c := range sc.Checks {
		if c.ID == id {
			return c, true
		}
	}
	return scenarioCheck{}, false
}

// scenarioContext — проверенные действия для системного промпта агента.
//
// Зачем не «просто рассказать про кейс». На выезде агент работает на чужой
// машине: памяти Jev там нет, и всё, что он знает о классе проблемы, — это
// команды, которые уже дали ответ в прошлых сессиях. Кейс и есть такой набор
// (проверки/починки/практика), но в промпт его надо положить коротко и
// буквально: команда → что она проверяет. Модель, которой дали список
// сработавших команд, не изобретает свою версию `ipconfig`.
func scenarioContext() string {
	sc := chatScenario
	if sc == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nПРОВЕРЕННЫЕ ДЕЙСТВИЯ: загружен кейс «%s» (%s). "+
		"Опирайся на эти команды; если они не подходят — объясни почему, а не заменяй молча.",
		sc.Title, sc.SectionTitle)
	for i, c := range sc.Checks {
		if i >= 15 {
			fmt.Fprintf(&b, "\n- …ещё %d проверок в кейсе", len(sc.Checks)-15)
			break
		}
		fmt.Fprintf(&b, "\n- проверка «%s»: %s", c.Title, truncate(strings.Join(c.Commands, " | "), 240))
		if c.Hint != "" {
			fmt.Fprintf(&b, " (подсказка: %s)", truncate(c.Hint, 160))
		}
	}
	for i, f := range sc.Fixes {
		if i >= 10 {
			break
		}
		fmt.Fprintf(&b, "\n- починка «%s»: %s", f.Title, truncate(strings.Join(f.Commands, " | "), 240))
		if len(f.Recheck) > 0 {
			fmt.Fprintf(&b, " (перепроверить: %s)", strings.Join(f.Recheck, ", "))
		}
	}
	if k := sc.Knowledge; k != nil {
		b.WriteString("\n- этот случай уже был: ")
		fmt.Fprintf(&b, "%s, %s — «%s»", firstNonEmpty(k.Host, "машина не указана"),
			firstNonEmpty(k.Date, "дата не указана"), k.Symptom)
		if k.RootCause != "" {
			fmt.Fprintf(&b, "; причина: %s", k.RootCause)
		}
		if k.Fix != "" {
			v := ""
			if k.Verified {
				v = " (проверено)"
			}
			fmt.Fprintf(&b, "; помогло: %s%s", k.Fix, v)
		}
	}
	return b.String()
}
