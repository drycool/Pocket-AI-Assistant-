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
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// scenario — один регламент. Имя файла — <name>.json в каталоге scenarios/.
type scenario struct {
	Name   string          `json:"name"`
	Title  string          `json:"title"`
	Checks []scenarioCheck `json:"checks"`
	Fixes  []scenarioFix   `json:"fixes"`
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
	Commands     []string       `json:"commands"`
	Rules        []scenarioRule `json:"rules"`
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
type scenarioFix struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Confirm    string   `json:"confirm"`
	NeedsAdmin bool     `json:"needs_admin"`
	Commands   []string `json:"commands"`
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
	} else if !strings.ContainsAny(name, "/\\") {
		path = filepath.Join(scenarioDir(), name)
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
	for i := range sc.Checks {
		c := &sc.Checks[i]
		if strings.TrimSpace(c.ID) == "" || len(c.Commands) == 0 {
			return nil, fmt.Errorf("сценарий %s: проверка %d без id или команд", path, i+1)
		}
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
	return &sc, nil
}

// scenarioNames — что лежит рядом: для подсказки, если сценарий не найден.
func scenarioNames() []string {
	entries, err := os.ReadDir(scenarioDir())
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") && e.Name() != "manifest.json" {
			names = append(names, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Strings(names)
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
