package main

// Этап 10: декларативные инструменты.
//
// У llamafile (и вообще у современных локальных агентов) правильно сделано то,
// что модель НЕ парсит вывод оболочки и не «угадывает» команду по тексту. Она
// возвращает СТРУКТУРНЫЙ вызов: имя инструмента + аргументы. Хост проверяет
// аргументы по схеме, применяет политику уровня, исполняет и кладёт результат
// обратно в диалог. Здесь ровно так и устроено.
//
// Почему это, а не только ```-блоки с командами. Регулярка по ```-блоку
// (extractCommands) — хрупкий путь: модель может оформить блок иначе, добавить
// пояснение внутри, дать две команды в строке. Схема убирает парсинг: либо
// пришёл валидный вызов, либо нет, и это видно ДО исполнения. При этом старый
// путь не удалён: команды оболочки остаются, но теперь у модели есть выбор —
// декларативный инструмент или команда, как у llamafile.
//
// Границы безопасности (это решение, а не «не успели»):
//   - read_file / grep_search / file_glob_search — только L1+ (чтение);
//   - write_file — только L2+ и с подтверждением оператора;
//   - MCP-инструменты — только L3 и с подтверждением: классифицировать чужой
//     инструмент по его описанию невозможно, поэтому он не получает «чтение»
//     по умолчанию;
//   - секреты (ключи, .ssh, shadow) не читаются и не пишутся вообще;
//   - запись в системные каталоги (Windows/Program Files, /etc, /usr…) запрещена;
//   - размеры и время ограничены, иначе один grep по C:\ съест сессию.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	toolMaxReadBytes  = 64 << 10 // больше модели за один раз не нужно
	toolMaxReadFile   = 4 << 20  // файл больше — отказ (для логов есть grep)
	toolMaxReadLines  = 200      // строк по умолчанию
	toolMaxReadLimit  = 2000     // строк максимум за вызов
	toolMaxWriteBytes = 1 << 20  // 1 МиБ на запись
	toolGrepMaxFile   = 2 << 20  // отдельный файл в grep
	toolGrepMaxFiles  = 20000    // файлов на один поиск
	toolGrepMaxHits   = 500      // совпадений максимум
	toolGlobMaxFiles  = 500      // результатов glob максимум
	toolTimeout       = 15 * time.Second
)

// ---------------------------------------------------------------------------
// Типы: вызов инструмента и его декларация (OpenAI tools API)
// ---------------------------------------------------------------------------

// ToolCall — вызов инструмента. Это ровно тот формат, который приходит в
// assistant.tool_calls (стримом — кусками по index) и уходит обратно в API.
type ToolCall struct {
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-строка, как в API
}

// ToolDef — элемент массива tools в запросе к модели.
type ToolDef struct {
	Type     string         `json:"type"`
	Function ToolDefinition `json:"function"`
}

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolSpec — локальная запись реестра. Level — минимальный уровень доступа,
// на котором инструмент вообще показывается модели.
type ToolSpec struct {
	Name        string
	Description string
	Schema      string
	Level       int
}

var toolRegistry = []ToolSpec{
	{
		Name: "read_file",
		Description: "Прочитать текстовый файл на этом ПК. Возвращает строки с номерами. " +
			"Для бинарных и очень больших файлов откажет — используйте grep_search.",
		Level: L1Read,
		Schema: `{"type":"object","properties":{` +
			`"path":{"type":"string","description":"путь к файлу (абсолютный или относительно текущего каталога)"},` +
			`"offset":{"type":"integer","description":"с какой строки начать, 1-based (по умолчанию 1)"},` +
			`"limit":{"type":"integer","description":"сколько строк вернуть (по умолчанию 200, максимум 2000)"}},` +
			`"required":["path"]}`,
	},
	{
		Name: "grep_search",
		Description: "Искать регулярное выражение в текстовых файлах каталога рекурсивно. " +
			"Возвращает file:line: text. Пропускает .git, бинарники и секреты.",
		Level: L1Read,
		Schema: `{"type":"object","properties":{` +
			`"pattern":{"type":"string","description":"регулярное выражение (RE2)"},` +
			`"path":{"type":"string","description":"каталог поиска (по умолчанию текущий)"},` +
			`"glob":{"type":"string","description":"фильтр имён, напр. *.log"},` +
			`"ignore_case":{"type":"boolean","description":"без учёта регистра"},` +
			`"max_results":{"type":"integer","description":"совпадений максимум (по умолчанию 50, максимум 500)"}},` +
			`"required":["pattern"]}`,
	},
	{
		Name:        "file_glob_search",
		Description: "Найти файлы по glob-шаблону (поддерживает **/). Возвращает пути.",
		Level:       L1Read,
		Schema: `{"type":"object","properties":{` +
			`"pattern":{"type":"string","description":"шаблон, напр. **/*.log или Data/*.json"},` +
			`"path":{"type":"string","description":"каталог поиска (по умолчанию текущий)"},` +
			`"max_results":{"type":"integer","description":"файлов максимум (по умолчанию 100, максимум 500)"}},` +
			`"required":["pattern"]}`,
	},
	{
		Name: "quick_probe",
		Description: "Быстрый снимок этого ПК за один заход (~10 с, только чтение): " +
			"ОС, память, свободное место, IP и DNS, слушающие порты, установленные соединения, " +
			"ошибки System/Application за сутки, защита, ожидание перезагрузки. " +
			"Формат key=value. Начинайте диагностику с него, а diag — когда нужно глубже.",
		Level:  L1Read,
		Schema: `{"type":"object","properties":{}}`,
	},
	{
		Name: "write_file",
		Description: "Записать текстовый файл (создать или заменить). Требует подтверждения оператора. " +
			"Системные каталоги и секреты запрещены.",
		Level: L2Fix,
		Schema: `{"type":"object","properties":{` +
			`"path":{"type":"string","description":"путь к файлу"},` +
			`"content":{"type":"string","description":"полное новое содержимое (до 1 МиБ)"}},` +
			`"required":["path","content"]}`,
	},
}

// toolDefsForLevel — что именно видит модель на текущем уровне. Это и есть
// «декларативность»: список инструментов фильтруется политикой, а не тем, что
// модель «пообещала» не вызывать лишнее.
func toolDefsForLevel(level int) []ToolDef {
	var defs []ToolDef
	for _, t := range toolRegistry {
		if level < t.Level {
			continue
		}
		defs = append(defs, ToolDef{Type: "function", Function: ToolDefinition{
			Name: t.Name, Description: t.Description, Parameters: json.RawMessage(t.Schema),
		}})
	}
	if level >= L3Admin {
		defs = append(defs, mcpToolDefs()...)
	}
	return defs
}

func isKnownTool(name string) bool {
	for _, t := range toolRegistry {
		if t.Name == name {
			return true
		}
	}
	return lookupMCPTool(name) != nil
}

// toolSummary — одна строка для шапки: что именно доступно на этом уровне.
func toolSummary(level int) string {
	var names []string
	for _, t := range toolRegistry {
		if level >= t.Level {
			names = append(names, t.Name)
		}
	}
	if level >= L3Admin && len(mcpIndex) > 0 {
		names = append(names, fmt.Sprintf("MCP-инструментов %d", len(mcpIndex)))
	}
	return strings.Join(names, ", ")
}

// classifyTool — политика для инструментов. Отдельно от classify() команд:
// у инструмента нет текста команды, но есть тип операции, и его уровень
// фиксирован. write_file — изменение (VNeedsYes), MCP — чужое (только L3).
func classifyTool(name string, level int) Verdict {
	switch name {
	case "read_file", "grep_search", "file_glob_search", "quick_probe":
		if level >= L1Read {
			return VReadOnly
		}
		return VForbidden
	case "write_file":
		if level >= L2Fix {
			return VNeedsYes
		}
		if level >= L1Read {
			return VNotAllowed
		}
		return VForbidden
	}
	if lookupMCPTool(name) != nil {
		if level >= L3Admin {
			return VNeedsYes
		}
		if level >= L1Read {
			return VNotAllowed
		}
		return VForbidden
	}
	return VNotAllowed
}

// ---------------------------------------------------------------------------
// Разбор аргументов и исполнение
// ---------------------------------------------------------------------------

type toolResult struct {
	ID   string
	Name string
	OK   bool
	Out  string // результат для модели (текст)
	Note string // причина отказа
	Dur  time.Duration
}

// ModelJSON — то, что уходит в историю сообщением role=tool. JSON, а не просто
// текст: модель должна видеть ok/error и не считать отказ успехом.
func (r toolResult) ModelJSON() string {
	m := map[string]any{"ok": r.OK, "tool": r.Name}
	if r.OK {
		m["output"] = r.Out
	} else {
		m["error"] = r.Note
	}
	m["elapsed_ms"] = r.Dur.Milliseconds()
	raw, _ := json.Marshal(m)
	return string(raw)
}

func parseToolArgs(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("аргументы не JSON: %w", err)
	}
	return m, nil
}

func argString(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}

func argInt(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		var n int
		if _, err := fmt.Sscan(v, &n); err == nil {
			return n
		}
	}
	return def
}

func argBool(args map[string]any, key string) bool {
	b, _ := args[key].(bool)
	return b
}

// runTool — политика + подтверждение + исполнение + журнал. Идентично
// handleCommand для shell-команд, но на входе структурный вызов.
func runTool(call ToolCall, reader lineReader) toolResult {
	name := strings.TrimSpace(call.Function.Name)
	res := toolResult{ID: call.ID, Name: name}
	if name == "" {
		res.Note = "пустое имя инструмента"
		return res
	}
	args, err := parseToolArgs(call.Function.Arguments)
	if err != nil {
		res.Note = err.Error()
		return res
	}

	verdict := classifyTool(name, currentLevel)
	fmt.Printf("\n[tool] %s %s  (%s)\n", name, shortToolArgs(call.Function.Arguments), verdict)
	switch verdict {
	case VForbidden:
		res.Note = fmt.Sprintf("инструмент запрещён на уровне L%d", currentLevel)
	case VNotAllowed:
		res.Note = fmt.Sprintf("инструмент требует более высокого уровня (L%d)", currentLevel)
	case VNeedsYes:
		if reader == nil {
			res.Note = "нужно подтверждение оператора, но сессия неинтерактивная"
		} else if strings.HasPrefix(name, "mcp__") {
			if !confirm(reader, "      Выполнить внешний MCP-инструмент? (y/n): ", "y") {
				res.Note = "оператор отказался"
			}
		} else if !confirm(reader, "      Записать файл? (y/n): ", "y") {
			res.Note = "оператор отказался"
		}
	}
	if res.Note != "" {
		fmt.Printf("      [BLOCK] %s\n", res.Note)
		logAction(currentLevel, "tool:"+name, verdict, false, -1, res.Note)
		return res
	}

	start := time.Now()
	out, xerr := execTool(name, args)
	res.Dur = time.Since(start)
	if xerr != nil {
		res.Note = xerr.Error()
		fmt.Printf("      [ERROR] %s\n", res.Note)
		logAction(currentLevel, "tool:"+name, verdict, true, 1, res.Note)
		return res
	}
	res.OK = true
	res.Out = truncate(out, toolMaxReadBytes)
	fmt.Printf("      -> OK, %d симв., %.2f с\n", len([]rune(res.Out)), res.Dur.Seconds())
	fmt.Printf("%s\n", strings.TrimRight(truncate(res.Out, 2000), "\n"))
	logAction(currentLevel, "tool:"+name, verdict, true, 0, res.Out)
	return res
}

// execTool — собственно реализация. MCP обрабатывается отдельно (mcp.go).
func execTool(name string, args map[string]any) (string, error) {
	switch name {
	case "read_file":
		return toolReadFile(args)
	case "grep_search":
		return toolGrepSearch(args)
	case "file_glob_search":
		return toolFileGlobSearch(args)
	case "quick_probe":
		return toolQuickProbe(args)
	case "write_file":
		return toolWriteFile(args)
	}
	if t := lookupMCPTool(name); t != nil {
		return callMCPTool(name, args)
	}
	return "", fmt.Errorf("неизвестный инструмент %q", name)
}

// shortToolArgs — для консоли: аргументы без шума (полный JSON бывает длинным).
func shortToolArgs(raw string) string {
	raw = strings.ReplaceAll(raw, "\n", " ")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	return truncate(raw, 160)
}

// toolReadFile — чтение текстового файла с номерами строк.
func toolReadFile(args map[string]any) (string, error) {
	p := argString(args, "path")
	if p == "" {
		return "", errors.New("не задан path")
	}
	abs, err := toolAbsPath(p)
	if err != nil {
		return "", err
	}
	if isSecretPath(abs) {
		return "", errors.New("это файл с секретами (ключи/пароли) — чтение через инструмент запрещено")
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("файл не найден: %v", err)
	}
	if fi.IsDir() {
		return "", errors.New("это каталог; для списка файлов используйте file_glob_search")
	}
	if fi.Size() > toolMaxReadFile {
		return "", fmt.Errorf("файл %d байт — больше лимита %d; используйте grep_search или read_file с offset",
			fi.Size(), int64(toolMaxReadFile))
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("чтение: %v", err)
	}
	head := raw
	if len(head) > 8000 {
		head = head[:8000]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return "", errors.New("файл бинарный (содержит NUL) — текстовое чтение не даст данных")
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	offset := argInt(args, "offset", 1)
	if offset < 1 {
		offset = 1
	}
	limit := argInt(args, "limit", toolMaxReadLines)
	if limit < 1 {
		limit = toolMaxReadLines
	}
	if limit > toolMaxReadLimit {
		limit = toolMaxReadLimit
	}

	var b strings.Builder
	shown := 0
	total := 0
	for i, line := range lines {
		ln := i + 1
		if ln < offset || strings.TrimSpace(line) == "" && ln == len(lines) && i > 0 && line == "" {
			// пустая последняя строка от финального \n — не показываем
			continue
		}
		if ln < offset {
			continue
		}
		total++
		if shown >= limit {
			continue
		}
		if b.Len() >= toolMaxReadBytes {
			break
		}
		fmt.Fprintf(&b, "%d: %s\n", ln, clipRunes(strings.TrimRight(line, "\r"), 500))
		shown++
	}
	if shown == 0 {
		return "", fmt.Errorf("строк начиная с %d нет (в файле %d строк)", offset, len(lines))
	}
	if total > shown || len(raw) > toolMaxReadBytes {
		fmt.Fprintf(&b, "\n… показано %d строк; файл %d байт, %d строк. Дальше — offset=%d или grep_search.",
			shown, len(raw), len(lines), offset+shown)
	}
	return b.String(), nil
}

// toolGrepSearch — поиск регуляркой по текстовым файлам каталога.
func toolGrepSearch(args map[string]any) (string, error) {
	pattern := argString(args, "pattern")
	if pattern == "" {
		return "", errors.New("не задан pattern")
	}
	if argBool(args, "ignore_case") {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("регулярное выражение: %v", err)
	}
	root := argString(args, "path")
	if root == "" {
		root = "."
	}
	abs, err := toolAbsPath(root)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("каталог поиска: %v", err)
	} else if !fi.IsDir() {
		return "", errors.New("path должен быть каталогом (для файла используйте read_file)")
	}
	glob := argString(args, "glob")
	maxHits := argInt(args, "max_results", 50)
	if maxHits < 1 {
		maxHits = 50
	}
	if maxHits > toolGrepMaxHits {
		maxHits = toolGrepMaxHits
	}

	var hits []string
	filesScanned := 0
	deadline := time.Now().Add(toolTimeout)
	stopped := ""
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil // недоступный каталог/файл не повод падать
		}
		if time.Now().After(deadline) {
			stopped = "таймаут"
			return filepath.SkipAll
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "target", "__pycache__":
				return filepath.SkipDir
			}
			return nil
		}
		filesScanned++
		if filesScanned > toolGrepMaxFiles {
			stopped = "лимит файлов"
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(abs, p)
		rel = filepath.ToSlash(rel)
		if glob != "" && !globMatch(rel, glob) && !globMatch(d.Name(), glob) {
			return nil
		}
		if isSecretPath(p) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() > toolGrepMaxFile {
			return nil
		}
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		head := raw
		if len(head) > 8000 {
			head = head[:8000]
		}
		if bytes.IndexByte(head, 0) >= 0 {
			return nil
		}
		for i, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimRight(line, "\r")
			if !re.MatchString(line) {
				continue
			}
			hits = append(hits, fmt.Sprintf("%s:%d: %s", rel, i+1, clipRunes(line, 300)))
			if len(hits) >= maxHits {
				stopped = "лимит совпадений"
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("обход: %v", err)
	}
	if len(hits) == 0 {
		return fmt.Sprintf("совпадений нет (файлов просмотрено: %d, pattern=%q, glob=%q)", filesScanned, pattern, glob), nil
	}
	out := strings.Join(hits, "\n")
	if stopped != "" {
		out += fmt.Sprintf("\n… поиск остановлен (%s): файлов просмотрено %d, совпадений %d.", stopped, filesScanned, len(hits))
	}
	return out, nil
}

// toolFileGlobSearch — поиск файлов по glob (с поддержкой **/).
func toolFileGlobSearch(args map[string]any) (string, error) {
	pattern := argString(args, "pattern")
	if pattern == "" {
		return "", errors.New("не задан pattern")
	}
	root := argString(args, "path")
	if root == "" {
		root = "."
	}
	abs, err := toolAbsPath(root)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("каталог поиска: %v", err)
	} else if !fi.IsDir() {
		return "", errors.New("path должен быть каталогом")
	}
	maxResults := argInt(args, "max_results", 100)
	if maxResults < 1 {
		maxResults = 100
	}
	if maxResults > toolGlobMaxFiles {
		maxResults = toolGlobMaxFiles
	}

	var found []string
	deadline := time.Now().Add(toolTimeout)
	stopped := ""
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if time.Now().After(deadline) {
			stopped = "таймаут"
			return filepath.SkipAll
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(abs, p)
		rel = filepath.ToSlash(rel)
		if !globMatch(rel, pattern) && !globMatch(d.Name(), pattern) {
			return nil
		}
		found = append(found, rel)
		if len(found) >= maxResults {
			stopped = "лимит результатов"
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("обход: %v", err)
	}
	if len(found) == 0 {
		return fmt.Sprintf("файлов не найдено (pattern=%q в %s)", pattern, abs), nil
	}
	sort.Strings(found)
	out := strings.Join(found, "\n")
	if stopped != "" {
		out += fmt.Sprintf("\n… остановлено (%s): показано %d.", stopped, len(found))
	}
	return out, nil
}

// toolWriteFile — запись целиком, атомарно (tmp + rename), с запретом
// системных каталогов и секретов.
func toolWriteFile(args map[string]any) (string, error) {
	p := argString(args, "path")
	if p == "" {
		return "", errors.New("не задан path")
	}
	content, _ := args["content"].(string)
	if content == "" && args["content"] == nil {
		return "", errors.New("не задан content")
	}
	if len(content) > toolMaxWriteBytes {
		return "", fmt.Errorf("содержимое %d байт — больше лимита %d", len(content), toolMaxWriteBytes)
	}
	abs, err := toolAbsPath(p)
	if err != nil {
		return "", err
	}
	if isSecretPath(abs) {
		return "", errors.New("путь относится к секретам (ключи/пароли) — запись запрещена")
	}
	if isProtectedWritePath(abs) {
		return "", errors.New("системный каталог — запись запрещена политикой")
	}
	dir := filepath.Dir(abs)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("каталог %s не существует — создайте его вручную", dir)
	}
	tmp := abs + ".tmp-fastagent"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("запись: %v", err)
	}
	if err := os.Rename(tmp, abs); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("переименование: %v", err)
	}
	return fmt.Sprintf("записано %d байт в %s", len(content), abs), nil
}

// ---------------------------------------------------------------------------
// Пути и защита
// ---------------------------------------------------------------------------

func toolAbsPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("пустой путь")
	}
	if strings.ContainsRune(p, 0) {
		return "", errors.New("NUL в пути")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("путь: %v", err)
	}
	return filepath.Clean(abs), nil
}

var reSecretName = regexp.MustCompile(`(?i)(^id_(rsa|dsa|ecdsa|ed25519)|\.pem$|\.key$|\.p12$|\.pfx$|^shadow$|^gshadow$|^master\.key$|^credentials$)`)

// isSecretPath — запрет на чтение/запись заведомо секретных файлов. Это не
// полноценный DLP: список узкий и намеренно консервативный (лучше отказать в
// чтении лишнего, чем отдать модели приватный ключ).
func isSecretPath(abs string) bool {
	slashed := filepath.ToSlash(abs)
	base := path.Base(slashed)
	if reSecretName.MatchString(base) {
		return true
	}
	for _, part := range []string{"/.ssh/", "/.gnupg/", "/.aws/"} {
		if strings.Contains(slashed, part) {
			return true
		}
	}
	return false
}

// isProtectedWritePath — куда писать нельзя никогда, даже на L2 с
// подтверждением. Чтение этих мест разрешено (диагностика), запись — нет.
func isProtectedWritePath(abs string) bool {
	low := strings.ToLower(filepath.ToSlash(abs))
	if runtime.GOOS == "windows" {
		vol := strings.ToLower(filepath.VolumeName(abs))
		if strings.TrimRight(low, "/") == strings.TrimRight(strings.ToLower(vol), "\\") {
			return true // корень диска
		}
		for _, root := range []string{"c:/windows", "c:/program files", "c:/programdata",
			"c:/$recycle.bin", "c:/system volume information"} {
			if low == root || strings.HasPrefix(low, root+"/") {
				return true
			}
		}
		return false
	}
	if low == "/" {
		return true
	}
	for _, root := range []string{"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64",
		"/boot", "/sys", "/proc", "/dev", "/var/lib"} {
		if low == root || strings.HasPrefix(low, root+"/") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Glob с поддержкой **/ и разбор конверта
// ---------------------------------------------------------------------------

// globMatch — filepath.Match по имени или по относительному пути с поддержкой
// `**` (stdlib её не умеет, а модуль ради этого тянуть не хочется).
func globMatch(rel, pattern string) bool {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "./")
	pattern = strings.TrimPrefix(filepath.ToSlash(pattern), "./")
	return matchGlobSegs(strings.Split(rel, "/"), strings.Split(pattern, "/"))
}

func matchGlobSegs(rel, pat []string) bool {
	if len(pat) == 0 {
		return len(rel) == 0
	}
	if pat[0] == "**" {
		if matchGlobSegs(rel, pat[1:]) {
			return true
		}
		if len(rel) == 0 {
			return false
		}
		return matchGlobSegs(rel[1:], pat)
	}
	if len(rel) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], rel[0])
	if err != nil || !ok {
		return false
	}
	return matchGlobSegs(rel[1:], pat[1:])
}

var reToolEnvelope = regexp.MustCompile("(?ms)```(?:tool|tool_call|json|function)\\s*\\n(.*?)```")

// parseToolEnvelope — запасной путь, если провайдер не умеет tools API:
// модель возвращает один конверт ```tool {"name":…,"arguments":{…}}```.
// Принимаем только известные имена: обычный JSON-пример в ответе не должен
// внезапно превратиться в исполнение.
func parseToolEnvelope(text string) (ToolCall, bool) {
	for _, m := range reToolEnvelope.FindAllStringSubmatch(text, -1) {
		var obj map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(m[1])), &obj) != nil {
			continue
		}
		name := firstNonEmpty(
			anyString(obj["name"]),
			anyString(obj["tool"]),
			anyString(obj["function"]),
		)
		if name == "" || !isKnownTool(name) {
			continue
		}
		var args any
		if v, ok := obj["arguments"]; ok {
			args = v
		} else if v, ok := obj["args"]; ok {
			args = v
		}
		raw, _ := json.Marshal(args)
		return ToolCall{
			ID:       fmt.Sprintf("call_%d", time.Now().UnixNano()),
			Type:     "function",
			Function: ToolFunction{Name: name, Arguments: string(raw)},
		}, true
	}
	return ToolCall{}, false
}

func clipRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---------------------------------------------------------------------------
// Цикл инструментов
// ---------------------------------------------------------------------------

type turnOptions struct {
	reader   lineReader // nil — подтверждения невозможны
	maxSteps int
}

// runAgentTurn — один ход диалога с инструментами. Возвращает историю с
// результатами, финальный текст модели и метаданные. Инструменты вызываются
// до maxSteps раз: каждая итерация — ответ модели + исполнение её вызовов.
func runAgentTurn(cfg Config, history []Message, opt turnOptions) ([]Message, string, Provider, time.Duration, error) {
	if opt.maxSteps <= 0 {
		opt.maxSteps = 6
	}
	tools := toolDefsForLevel(currentLevel)
	seen := map[string]int{}
	var answer string
	var provider Provider
	var ttft time.Duration

	for step := 0; step < opt.maxSteps; step++ {
		a, calls, p, t, err := streamAnswer(cfg, history, tools)
		if err != nil {
			return history, "", p, t, err
		}
		answer, provider, ttft = a, p, t

		if len(calls) == 0 {
			if env, ok := parseToolEnvelope(a); ok {
				calls = []ToolCall{env}
			}
		}
		history = append(history, Message{Role: "assistant", Content: a, ToolCalls: calls})
		if len(calls) == 0 {
			return history, answer, provider, ttft, nil
		}

		for _, call := range calls {
			key := call.Function.Name + "|" + strings.TrimSpace(call.Function.Arguments)
			var res toolResult
			if seen[key] > 0 {
				// Тот же вызов с теми же аргументами: модель зациклилась.
				// Возвращаем отказ, чтобы она сменила тактику, а не долбилась.
				res = toolResult{ID: call.ID, Name: call.Function.Name,
					Note: "повторный вызов с теми же аргументами отклонён: результат уже есть выше"}
				fmt.Printf("\n[tool] %s — повтор отклонён\n", call.Function.Name)
			} else {
				res = runTool(call, opt.reader)
			}
			seen[key]++
			history = append(history, Message{Role: "tool", ToolCallID: call.ID, Content: res.ModelJSON()})
		}
	}
	return history, answer, provider, ttft,
		fmt.Errorf("модель не завершила работу за %d шагов с инструментами", opt.maxSteps)
}

// toolSystemPrompt — кусок системного промпта про инструменты. Инструкция
// нужна и для tools API (модель видит декларации), и для запасного конверта.
func toolSystemPrompt() string {
	return "Инструменты. Для чтения файлов и поиска используй инструменты read_file, grep_search, " +
		"file_glob_search (структурный вызов), а не команды оболочки: так безопаснее и точнее. " +
		"write_file меняет файл и требует подтверждения оператора — предлагай его только когда " +
		"оператор просит что-то сохранить. Если инструменты недоступны (нет tools API), верни РОВНО " +
		"один блок:\n```tool\n{\"name\":\"read_file\",\"arguments\":{\"path\":\"…\"}}\n```\n" +
		"и агент выполнит его, а результат придёт следующим сообщением. Не выдумывай содержимое " +
		"файлов: если инструмент не дал данных — так и скажи. "
}
