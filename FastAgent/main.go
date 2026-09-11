// FastAgent — быстрый портативный AI-агент для админской флешки.
//
// Отличия от Agent/main.go (старый агент не изменяется, работает параллельно):
//  1. СКОРОСТЬ: приоритет №1 — облачный DeepSeek по API-ключу (первый токен
//     обычно < 1 с), ответ читается стримингом (SSE) и печатается по мере
//     поступления. Локальная Ollama (gpu2 через Pi-шлюз) остаётся резервом:
//     включается автоматически, если облако недоступно (нет интернета, лимит).
//  2. РЕГУЛИРУЕМЫЙ УРОВЕНЬ ДОСТУПА (L0..L3): агент не просто советует команды,
//     он их выполняет — но строго в рамках выбранного уровня:
//     L0 "chat"  — команды не выполняются вообще (безопасно на чужом ПК);
//     L1 "read"  — только чтение (белый список read-only команд), без спроса;
//     L2 "fix"   — чтение + безопасные починки из белого списка (каждая
//     подтверждается y/n и пишется в журнал действий);
//     L3 "admin" — любые команды, но подтверждение обязательно, а для
//     «опасных» (format/diskpart/reg delete/...) — двойное.
//  3. ЖУРНАЛ ДЕЙСТВИЙ: каждое выполнение пишется в Data/logs/actions_<дата>.log
//     (JSON Lines) — видно, что и на каком уровне запускалось.
//
// Зависимостей нет: только стандартная библиотека Go.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Конфигурация
// ---------------------------------------------------------------------------

// Provider — источник модели. Порядок в списке = приоритет (fallback вниз).
type Provider struct {
	Name            string       `json:"name"`
	Type            string       `json:"type"` // пока только "openai" (/v1/chat/completions)
	BaseURL         string       `json:"base_url"`
	APIKey          string       `json:"api_key"`
	Model           string       `json:"model"`
	ReasoningEffort string       `json:"reasoning_effort,omitempty"` // "none" отключает thinking (Ollama 0.33+, DeepSeek)
	Thinking        *ThinkingOpt `json:"thinking,omitempty"`         // {"type":"disabled"} — DeepSeek: ответ на ~0.4 с раньше
	MaxTokens       int          `json:"max_tokens,omitempty"`       // переопределяет общий лимит
}

// ThinkingOpt — параметр "thinking" для провайдеров DeepSeek-совместимого API.
// Проверено на api.deepseek.com: {"type":"disabled"} убирает reasoning целиком
// (0.71 с до первого токена против ~1.4 с и 80 чанков размышлений).
type ThinkingOpt struct {
	Type string `json:"type"` // "disabled" | "enabled"
}

type Config struct {
	AccessLevel          int        `json:"access_level"`            // 0..3
	Stream               bool       `json:"stream"`                  // SSE-стриминг ответа
	AutoFollowup         bool       `json:"auto_followup"`           // отдавать вывод команды модели
	ShowReasoning        bool       `json:"show_reasoning"`          // печатать "размышления" модели
	MaxTokens            int        `json:"max_tokens"`              // лимит ответа
	Temperature          float64    `json:"temperature"`             // 0 = детерминированно
	FirstTokenTimeoutSec int        `json:"first_token_timeout_sec"` // ожидание первого токена
	TotalTimeoutSec      int        `json:"total_timeout_sec"`       // общий таймаут запроса
	HistoryLimit         int        `json:"history_limit"`           // сколько сообщений истории держать
	WindowsEncoding      string     `json:"windows_encoding"`        // auto|cp866|cp1251|utf8|off — вывод команд Windows
	Providers            []Provider `json:"providers"`
}

func defaultConfig() Config {
	return Config{
		AccessLevel:          0,
		Stream:               true,
		AutoFollowup:         true,
		ShowReasoning:        false,
		MaxTokens:            900,
		Temperature:          0,
		FirstTokenTimeoutSec: 25,
		TotalTimeoutSec:      180,
		HistoryLimit:         10,
		WindowsEncoding:      "auto",
	}
}

// Message — сообщение в формате OpenAI-совместимого API.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ---------------------------------------------------------------------------
// Уровни доступа
// ---------------------------------------------------------------------------

const (
	L0Chat  = 0
	L1Read  = 1
	L2Fix   = 2
	L3Admin = 3
)

var levelLabels = map[int]string{
	L0Chat:  "L0/chat  — только диалог, команды не выполняются",
	L1Read:  "L1/read  — только чтение (белый список), без подтверждения",
	L2Fix:   "L2/fix   — чтение + безопасные починки (подтверждение y/n)",
	L3Admin: "L3/admin — любые команды (опасные — двойное подтверждение)",
}

type Verdict int

const (
	VReadOnly   Verdict = iota // безопасное чтение (L1+)
	VNeedsYes                  // требует подтверждения y/n (L2+)
	VDangerous                 // требует двойного подтверждения (только L3)
	VForbidden                 // запрещено на этом уровне
	VNotAllowed                // не в белом списке (нужен L3)
)

func (v Verdict) String() string {
	switch v {
	case VReadOnly:
		return "чтение"
	case VNeedsYes:
		return "изменение (подтверждение)"
	case VDangerous:
		return "ОПАСНО (двойное подтверждение)"
	case VNotAllowed:
		return "нет в белом списке (нужен L3)"
	default:
		return "запрещено"
	}
}

var (
	// Группа 1: инструменты, у которых после имени обязателен пробел или конец строки.
	// Якорь (\s|$) обязателен: без него `w` совпадёт с `wscript`,
	// `tr` — с `truncate`, `file` — с `filetool` (реальная дыра, найдена на репетиции).
	reReadTools = regexp.MustCompile(`^(ipconfig|systeminfo|whoami|hostname|netstat|tasklist|ping|tracert|pathping|nslookup|arp|getmac|type|dir|tree|where|ver|vol|echo|gpresult|driverquery|uptime|uname|df|free|ls|cat|ss|journalctl|lscpu|lspci|lsblk|dmidecode|sensors|dmesg|nmcli|ethtool|systemd-analyze|which|whereis|id|who|last|w|ps|vmstat|iostat|hostnamectl|timedatectl|localectl|printenv|env|ifconfig|iwconfig|mount|grep|egrep|fgrep|findstr|more|less|head|tail|sort|uniq|wc|cut|tr|nl|du|stat|file|locate|find|dnsclientcache)(\s|$)`)

	reReadSafe = []*regexp.Regexp{
		reReadTools,
		regexp.MustCompile(`^route\s+print`), regexp.MustCompile(`^ip\s+(-[a-z0-9]+\s+)*(a|addr|address|r|route|link|l|neigh|rule|maddr)`),
		regexp.MustCompile(`^fsutil\s+(fsinfo|dirty|volume)`), regexp.MustCompile(`^chkdsk\s*$`),
		regexp.MustCompile(`^chkdsk\s+[a-z]:\s*/scan`), // онлайн-сканирование без изменений
		regexp.MustCompile(`^sfc\s+/verifyonly`), regexp.MustCompile(`^dism\s+/online\s+/cleanup-image\s+/scanhealth`),
		regexp.MustCompile(`^netsh\s+(wlan\s+show|interface\s+show|advfirewall\s+show|firewall\s+show|int\s+ip\s+show)`),
		regexp.MustCompile(`^sc\s+query`), regexp.MustCompile(`^schtasks\s+/query`),
		regexp.MustCompile(`^reg\s+query`), regexp.MustCompile(`^wmic\s`),
		regexp.MustCompile(`^powercfg\s+/(a|query|list)`), regexp.MustCompile(`^date\s+/t`),
		regexp.MustCompile(`^systemctl\s+(status|is-active|show)`), regexp.MustCompile(`^resolvectl\s+status`),
		regexp.MustCompile(`^smartctl\s+-[AaHh]`), regexp.MustCompile(`^crontab\s+-l`),
		// top только в batch-режиме: интерактивный top заблокирует агента
		regexp.MustCompile(`^top\s+-b`),
		regexp.MustCompile(`^net\s+(view|share)\s*$`), regexp.MustCompile(`^net\s+view\s`),
		// wevtutil qe/gl/el — чтение журналов; cl (очистка) вынесен в опасные
		regexp.MustCompile(`^wevtutil\s+(qe|gl|el)\b`),
		// PowerShell read-only глаголы
		regexp.MustCompile(`^(get|test|measure|select|show|resolve|compare|format-list|out-string)-`),
	}

	reFixAllowed = []*regexp.Regexp{
		regexp.MustCompile(`^ipconfig\s+/(flushdns|release|renew|registerdns)`),
		regexp.MustCompile(`^netsh\s+(winsock|int\s+ip|int\s+ipv4|interface)\s+reset`),
		regexp.MustCompile(`^netsh\s+int\s+ip\s+set\s+(address|dns)`),
		regexp.MustCompile(`^netsh\s+advfirewall\s+(firewall\s+)?set`),
		regexp.MustCompile(`^(restart|start|stop)-service\s`),
		regexp.MustCompile(`^sc\s+(start|stop|config)\s`),
		regexp.MustCompile(`^net\s+(start|stop)\s`),
		regexp.MustCompile(`^taskkill\s`),
		regexp.MustCompile(`^sfc\s+/scannow`),
		regexp.MustCompile(`^dism\s+/online\s+/cleanup-image\s+/restorehealth`),
		regexp.MustCompile(`^gpupdate(\s+/force)?\s*$`),
		regexp.MustCompile(`^chkdsk\s+[a-z]:\s*/[fr]`),
		regexp.MustCompile(`^clear-dnsclientservercache`), regexp.MustCompile(`^clear-recyclebin`),
		regexp.MustCompile(`^optimize-volume`), regexp.MustCompile(`^defrag\s`),
		regexp.MustCompile(`^powercfg\s+(-h\s+(on|off)|/setactive)`),
		regexp.MustCompile(`^w32tm\s+/resync`), regexp.MustCompile(`^nbtstat\s+-R`),
		regexp.MustCompile(`^sc\s+failure`),
		// Linux
		regexp.MustCompile(`^systemctl\s+(restart|start|stop|reload)\s`),
		regexp.MustCompile(`^(apt|apt-get)\s+(update|upgrade|-f\s+install)`),
		regexp.MustCompile(`^systemd-resolve\s+--flush-caches`), regexp.MustCompile(`^resolvectl\s+flush-caches`),
		regexp.MustCompile(`^ip\s+(addr|link)\s+(add|del|set|flush)`),
		regexp.MustCompile(`^drop_caches|^sync(\s|$)`),
	}

	reDanger = []*regexp.Regexp{
		// ВАЖНО: не `\bformat\b` — иначе PowerShell `Format-Table` в конвейере
		// считается опасным и read-only запросы журнала блокируются (найдено на репетиции).
		regexp.MustCompile(`(^|[;&|]\s*)format\s+[a-z]:`), regexp.MustCompile(`(^|[;&|]\s*)format\s+/`),
		regexp.MustCompile(`\bformat\.com\b`), regexp.MustCompile(`^diskpart`),
		regexp.MustCompile(`^bcdedit`), regexp.MustCompile(`^reg\s+(delete|add|import)`),
		regexp.MustCompile(`\bdel\s+/[fsq]`), regexp.MustCompile(`^(rd|rmdir)\s+/s`),
		regexp.MustCompile(`^remove-item\s.*-recurse`), regexp.MustCompile(`^rm\s+-[rf]{1,2}\s`),
		regexp.MustCompile(`^(takeown|icacls)\s.*(/grant|/f)`), regexp.MustCompile(`^cipher\s+/w`),
		regexp.MustCompile(`^vssadmin\s+delete`), regexp.MustCompile(`^wbadmin\s+delete`),
		regexp.MustCompile(`^wevtutil\s+cl`), // очистка журнала = уничтожение следов
		regexp.MustCompile(`^set-executionpolicy`), regexp.MustCompile(`^(new|add)-(localuser|localgroupmember)`),
		regexp.MustCompile(`^set-localuser`), regexp.MustCompile(`^enable-psremoting`),
		regexp.MustCompile(`^winrm(\s|$)`), regexp.MustCompile(`^schtasks\s+/create`),
		regexp.MustCompile(`^sc\s+delete`), regexp.MustCompile(`^wmic\s.*\bdelete\b`),
		regexp.MustCompile(`^(shutdown|restart-computer|stop-computer)`),
		regexp.MustCompile(`^net\s+user\s+\S+\s+\S+`),       // смена/создание учётки
		regexp.MustCompile(`^(mkfs|dd\s+if=|shred|wipefs)`), // Linux
		regexp.MustCompile(`^rm\s+-rf\s+/(\s|$)`), regexp.MustCompile(`^chmod\s+777\s+/`),
		regexp.MustCompile(`^useradd|^usermod|^passwd\s`), regexp.MustCompile(`^iptables\s+-F`),
		regexp.MustCompile(`^crontab\s+-r`), regexp.MustCompile(`^killall\s`),
		// find умеет запускать команды и удалять файлы
		regexp.MustCompile(`^(find|locate)\s.*\s-(exec|execdir|delete|ok)\b`),
		regexp.MustCompile(`\|\s*(bash|sh|zsh|cmd|powershell|pwsh|iex|invoke-expression)\b`), // конвейер в интерпретатор
		regexp.MustCompile(`^sed\s+-i`), regexp.MustCompile(`^awk\s.*>\s*\S`), regexp.MustCompile(`^xargs\s`),
	}
)

// unwrapShell снимает обёртки `powershell -c "..."` / `pwsh -Command "..."`.
// Возвращает внутреннюю команду и признак «непрозрачная нагрузка»
// (-EncodedCommand / -e): её нельзя разобрать, поэтому она всегда опасна.
func unwrapShell(c string) (inner string, opaque bool) {
	if !strings.HasPrefix(c, "powershell") && !strings.HasPrefix(c, "pwsh") {
		return "", false
	}
	rest := strings.TrimSpace(c[strings.IndexByte(c, ' ')+1:])
	// Флаги проверяем только ДО -c/-Command: внутри кавычек может встретиться
	// что угодно (например `grep -e foo`), это не делает команду непрозрачной.
	for _, tok := range strings.Fields(rest) {
		lt := strings.Trim(tok, `"'`)
		if lt == "-c" || lt == "/c" || strings.HasPrefix(lt, "-command") {
			break
		}
		if lt == "-e" || lt == "-enc" || strings.HasPrefix(lt, "-encodedcommand") {
			return "", true
		}
	}
	for _, flag := range []string{"-c ", "-command ", "/c "} {
		if i := strings.Index(rest, flag); i >= 0 {
			in := strings.TrimSpace(rest[i+len(flag):])
			in = strings.Trim(in, `"'`)
			return in, false
		}
	}
	return "", false
}

// classify — центральная точка политики: что делать с командой на уровне level.
// Порядок проверок ВАЖЕН: сначала «опасное», затем «изменяющее», и только потом
// «чтение». Иначе `ipconfig /flushdns` поймается общим правилом `^ipconfig`
// и будет выполнен без подтверждения (регрессия закрыта тестом TestClassify).
func classify(cmd string, level int) Verdict {
	return classifyDepth(cmd, level, 0)
}

func classifyDepth(cmd string, level, depth int) Verdict {
	c := strings.ToLower(strings.TrimSpace(cmd))
	c = strings.TrimPrefix(c, "cmd /c ")
	c = strings.TrimPrefix(c, "cmd.exe /c ")
	// Повышение прав не отменяет политику: классифицируем саму команду
	// (`sudo ip addr` = чтение, `sudo rm -rf /` = опасно). Пароль sudo
	// неинтерактивно не запросится — команда просто упадёт.
	for _, p := range []string{"sudo -n ", "sudo ", "doas ", "runas "} {
		c = strings.TrimPrefix(c, p)
	}
	if c == "" {
		return VForbidden
	}

	// Обёртки разворачиваем и классифицируем внутреннюю команду по тем же правилам:
	// модель любит отдавать `powershell -c "Get-Service ..."`, и это не должно
	// блокироваться на L1, а `powershell -c "Remove-Item -Recurse"` — должно.
	if depth < 2 {
		if inner, opaque := unwrapShell(c); opaque {
			if level >= L3Admin {
				return VDangerous
			}
			return VForbidden
		} else if inner != "" && inner != c {
			return classifyDepth(inner, level, depth+1)
		}
	}

	for _, re := range reDanger {
		if re.MatchString(c) {
			if level >= L3Admin {
				return VDangerous
			}
			return VForbidden
		}
	}
	for _, re := range reFixAllowed {
		if re.MatchString(c) {
			if level >= L2Fix {
				return VNeedsYes
			}
			if level >= L1Read {
				return VNotAllowed
			}
			return VForbidden
		}
	}
	for _, re := range reReadSafe {
		if re.MatchString(c) {
			if level >= L1Read {
				return VReadOnly
			}
			return VForbidden
		}
	}
	// Незнакомая команда: на L3 — с подтверждением, ниже — нельзя.
	if level >= L3Admin {
		return VNeedsYes
	}
	return VNotAllowed
}

// ---------------------------------------------------------------------------
// Пути и состояние
// ---------------------------------------------------------------------------

var (
	baseDir     string
	dataDir     string
	sessionsDir string
	reportsDir  string
	logsDir     string
	configFile  string
	actionsLog  string
)

func initPaths() {
	exe, err := os.Executable()
	if err != nil {
		exe, _ = filepath.Abs(".")
	}
	baseDir = filepath.Dir(filepath.Dir(exe))
	if _, err := os.Stat(filepath.Join(baseDir, "Data")); err != nil {
		// запуск не со флешки (например, из /tmp при отладке)
		baseDir, _ = filepath.Abs(".")
		if _, err := os.Stat(filepath.Join(baseDir, "Data")); err != nil {
			baseDir = filepath.Dir(filepath.Dir(exe))
		}
	}
	dataDir = filepath.Join(baseDir, "Data")
	sessionsDir = filepath.Join(dataDir, "sessions-fast")
	reportsDir = filepath.Join(dataDir, "reports")
	logsDir = filepath.Join(dataDir, "logs")
	configFile = filepath.Join(dataDir, "config.fast.json")
	actionsLog = filepath.Join(logsDir, "actions_"+time.Now().Format("2006-01-02")+".log")

	for _, d := range []string{sessionsDir, reportsDir, logsDir} {
		_ = os.MkdirAll(d, 0o755)
	}
}

func loadConfig() Config {
	cfg := defaultConfig()
	data, err := os.ReadFile(configFile)
	if err != nil {
		fmt.Printf("[!] Нет %s — беру значения по умолчанию (уровень L0, провайдеров нет).\n", configFile)
		fmt.Println("    Образец: Data/config.fast.example.json")
		return cfg
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Printf("[ERROR] %s: %v\n", configFile, err)
		os.Exit(2)
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 900
	}
	if cfg.FirstTokenTimeoutSec == 0 {
		cfg.FirstTokenTimeoutSec = 25
	}
	if cfg.TotalTimeoutSec == 0 {
		cfg.TotalTimeoutSec = 180
	}
	if cfg.HistoryLimit == 0 {
		cfg.HistoryLimit = 10
	}
	return cfg
}

// ---------------------------------------------------------------------------
// Журнал действий
// ---------------------------------------------------------------------------

func logAction(level int, cmd string, verdict Verdict, approved bool, exitCode int, out string) {
	entry := map[string]any{
		"ts":        time.Now().Format(time.RFC3339),
		"level":     level,
		"cmd":       cmd,
		"verdict":   verdict.String(),
		"approved":  approved,
		"exit_code": exitCode,
		"output":    truncate(out, 2000),
		"host":      hostname(),
	}
	line, _ := json.Marshal(entry)
	f, err := os.OpenFile(actionsLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

// truncate обрезает вывод до n БАЙТ, но строго по границе руны: иначе на стыке
// появлялся «битый» символ (U+FFFD) — в журнале и в контексте модели.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("…[обрезано %d симв.]", utf8.RuneCountInString(s)-utf8.RuneCountInString(s[:cut]))
}

// ---------------------------------------------------------------------------
// HTTP-клиент: стриминг (SSE) + обычный запрос
// ---------------------------------------------------------------------------

type openAIRequest struct {
	Model           string       `json:"model"`
	Messages        []Message    `json:"messages"`
	Stream          bool         `json:"stream,omitempty"`
	MaxTokens       int          `json:"max_tokens,omitempty"`
	Temperature     *float64     `json:"temperature,omitempty"`
	ReasoningEffort string       `json:"reasoning_effort,omitempty"`
	Thinking        *ThinkingOpt `json:"thinking,omitempty"`
}

type openAIChunk struct {
	Choices []struct {
		Delta struct {
			Content    string `json:"content"`
			Reasoning  string `json:"reasoning_content"`
			Reasoning2 string `json:"reasoning"`
		} `json:"delta"`
		Message struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

var httpClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  false,
	},
}

// Статистика последнего ответа для строки [v]: сколько символов ушло в
// "размышления" и сколько времени они заняли (по умолчанию скрыты).
var (
	lastReasoningChars int
	lastReasoningDur   time.Duration
)

func endpoint(p Provider) string {
	base := strings.TrimRight(p.BaseURL, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

// chatStream отправляет запрос и стримит ответ. Возвращает текст ответа и время
// до первого токена. onDelta вызывается на каждый фрагмент (печать в реальном времени).
func chatStream(p Provider, msgs []Message, cfg Config, onDelta func(string)) (string, time.Duration, error) {
	temp := cfg.Temperature
	body := openAIRequest{
		Model:           p.Model,
		Messages:        msgs,
		Stream:          cfg.Stream,
		MaxTokens:       pickInt(p.MaxTokens, cfg.MaxTokens),
		Temperature:     &temp,
		ReasoningEffort: p.ReasoningEffort,
		Thinking:        p.Thinking,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", 0, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TotalTimeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(p), bytes.NewReader(payload))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	req.Header.Set("Accept", "text/event-stream")

	// Таймер «первого токена»: если модель молчит дольше N секунд — рвём запрос
	// и уходим на следующий провайдер (важно для быстрого fallback).
	firstToken := time.AfterFunc(time.Duration(cfg.FirstTokenTimeoutSec)*time.Second, cancel)

	start := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		firstToken.Stop()
		return "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		firstToken.Stop()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if !cfg.Stream {
		firstToken.Stop()
		var chunk openAIChunk
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err := json.Unmarshal(raw, &chunk); err != nil {
			return "", 0, err
		}
		if chunk.Error != nil {
			return "", 0, fmt.Errorf("API: %s", chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 {
			return "", 0, fmt.Errorf("ответ без choices")
		}
		text := strings.TrimSpace(chunk.Choices[0].Message.Content)
		if text == "" {
			// Ответа нет — отдаем размышления, чтобы оператор не видел пустоту.
			text = strings.TrimSpace(chunk.Choices[0].Message.Reasoning)
			if text != "" {
				text = "[reasoning] " + text
			}
		}
		if text == "" {
			return "", 0, fmt.Errorf("пустой ответ модели")
		}
		return text, 0, nil
	}

	// Стриминг. ВАЖНО: content и reasoning_content — разные потоки.
	// DeepSeek/deepseek-flash сначала отдаёт сотни чанков reasoning_content
	// (по-английски!), и если печатать их вживую, оператор видит «шёпот модели»
	// вместо ответа, а ttft считается неверно. Печатаем ТОЛЬКО content;
	// размышления копим и пускаем в дело лишь если content так и не пришёл
	// (или если оператор включил show_reasoning).
	var content, reasoning strings.Builder
	var ttft, ttftReason time.Duration
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var chunk openAIChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			firstToken.Stop()
			return content.String(), ttft, fmt.Errorf("API: %s", chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		d := chunk.Choices[0].Delta

		if piece := firstNonEmpty(d.Reasoning, d.Reasoning2); piece != "" {
			if ttftReason == 0 {
				ttftReason = time.Since(start)
				firstToken.Stop() // провайдер жив: снимаем таймер «первого токена»
			}
			reasoning.WriteString(piece)
			if cfg.ShowReasoning && onDelta != nil {
				onDelta(piece)
			}
		}
		if d.Content != "" {
			if ttft == 0 {
				ttft = time.Since(start)
				firstToken.Stop()
			}
			content.WriteString(d.Content)
			if onDelta != nil {
				onDelta(d.Content)
			}
		}
	}
	firstToken.Stop()
	if err := scanner.Err(); err != nil && content.Len() == 0 && reasoning.Len() == 0 {
		return "", 0, err
	}
	if content.Len() == 0 {
		if r := strings.TrimSpace(reasoning.String()); r != "" {
			// Всё ушло в размышления — отдаем их, но без стриминга в консоль.
			lastReasoningChars = len([]rune(r))
			lastReasoningDur = ttftReason
			return "[reasoning] " + r, ttftReason, nil
		}
		return "", 0, fmt.Errorf("пустой ответ модели")
	}
	lastReasoningChars = len([]rune(reasoning.String()))
	lastReasoningDur = ttftReason
	return content.String(), ttft, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func pickInt(a, b int) int {
	if a > 0 {
		return a
	}
	return b
}

// sendMessage — fallback по провайдерам: первый успешный побеждает.
func sendMessage(cfg Config, msgs []Message, onDelta func(string)) (string, Provider, time.Duration, error) {
	var errs []string
	for i, p := range cfg.Providers {
		if strings.TrimSpace(p.Model) == "" || strings.TrimSpace(p.BaseURL) == "" {
			continue
		}
		text, ttft, err := chatStream(p, msgs, cfg, onDelta)
		if err == nil {
			return text, p, ttft, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", p.Name, err))
		if i < len(cfg.Providers)-1 {
			fmt.Printf("\n[fallback] %s недоступен (%v) → следующий провайдер\n", p.Name, err)
		}
	}
	return "", Provider{}, 0, fmt.Errorf("все провайдеры недоступны: %s", strings.Join(errs, " | "))
}

// ---------------------------------------------------------------------------
// Выполнение команд на гостевом ПК
// ---------------------------------------------------------------------------

func runCommand(cmd string, timeout time.Duration) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var c *exec.Cmd
	var tmpFile string
	if runtime.GOOS == "windows" {
		if isPowerShellCommand(cmd) {
			c = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", cmd)
		} else {
			// Нативные утилиты (systeminfo, ipconfig, wmic, sc, netsh, findstr)
			// запускаем через cmd.exe, переключая кодовую страницу консоли:
			// под chcp 65001 они теряют кириллицу, подменяя её на '?'.
			f, err := os.CreateTemp("", "fastagent-*.cmd")
			if err != nil {
				return "", -1, err
			}
			tmpFile = f.Name()
			if _, err := f.WriteString(buildCmdFile(cmd, detectedConsoleCP(), 65001)); err != nil {
				f.Close()
				return "", -1, err
			}
			f.Close()
			c = exec.CommandContext(ctx, "cmd", "/c", tmpFile)
		}
	} else {
		c = exec.CommandContext(ctx, "/bin/sh", "-c", cmd)
	}
	if tmpFile != "" {
		defer os.Remove(tmpFile)
	}
	c.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
	out, err := c.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
		if ctx.Err() == context.DeadlineExceeded {
			return decodeConsoleBytes(out) + "\n[timeout] команда прервана по таймауту", code, nil
		}
	}
	return decodeConsoleBytes(out), code, nil
}

var reCmdBlock = regexp.MustCompile("(?ms)```(?:cmd|bat|powershell|ps1|bash|sh|shell|console)?\\s*\\n(.*?)```")

// extractCommands достаёт команды из блоков кода вида ```cmd ... ```.
func extractCommands(answer string) []string {
	var cmds []string
	for _, m := range reCmdBlock.FindAllStringSubmatch(answer, -1) {
		for _, line := range strings.Split(m[1], "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "rem ") {
				continue
			}
			if strings.HasPrefix(line, "::") {
				continue
			}
			cmds = append(cmds, line)
		}
	}
	return cmds
}

func confirm(reader *bufio.Reader, prompt string, word string) bool {
	fmt.Print(prompt)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(line), word)
}

var reYesNo = regexp.MustCompile(`(?ms)^\s*(?:команда|command)\s*:\s*(.+)$`)

// ---------------------------------------------------------------------------
// Сессии
// ---------------------------------------------------------------------------

func saveSession(file, id string, history []Message) {
	payload := map[string]any{
		"session_id": id,
		"updated_at": time.Now().Format(time.RFC3339),
		"level":      currentLevel,
		"history":    history,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err == nil {
		_ = os.Rename(tmp, file)
	}
}

var currentLevel int

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	cfg := Config{}
	levelFlag := flag.Int("level", -1, "уровень доступа 0..3 (переопределяет config)")
	cfgFlag := flag.String("config", "", "путь к config.fast.json")
	diagFlag := flag.Bool("diag", false, "сразу собрать диагностику гостевого ПК (нужен L1+)")
	flag.Parse()

	initPaths()
	if *cfgFlag != "" {
		configFile = *cfgFlag
		actionsLog = filepath.Join(logsDir, "actions_"+time.Now().Format("2006-01-02")+".log")
	}
	cfg = loadConfig()
	setEncodingMode(cfg.WindowsEncoding)
	ensureUTF8Console()
	if *levelFlag >= 0 && *levelFlag <= 3 {
		cfg.AccessLevel = *levelFlag
	}
	currentLevel = cfg.AccessLevel

	reader := bufio.NewReader(os.Stdin)

	fmt.Println("===================================================")
	fmt.Println("        FASTAGENT — быстрый агент (DeepSeek-first)")
	fmt.Println("===================================================")
	if len(cfg.Providers) == 0 {
		fmt.Println("[ERROR] В конфиге нет провайдеров. Возьмите Data/config.fast.example.json")
		os.Exit(2)
	}
	fmt.Printf("Провайдеры (приоритет сверху вниз):\n")
	for i, p := range cfg.Providers {
		fmt.Printf("  [%d] %s — %s (%s)\n", i+1, p.Name, p.Model, p.BaseURL)
	}
	fmt.Printf("Уровень доступа: %s\n", levelLabels[currentLevel])
	fmt.Printf("Стриминг: %v | авто-продолжение по выводу команды: %v | журнал: %s\n",
		cfg.Stream, cfg.AutoFollowup, filepath.Base(actionsLog))
	if runtime.GOOS == "windows" {
		cpName := "cp866"
		switch detectedConsoleCP() {
		case 1251:
			cpName = "cp1251"
		case 65001:
			cpName = "utf8"
		}
		fmt.Printf("Кодировка вывода команд: %s (windows_encoding=%s)\n", cpName, encMode)
	}
	fmt.Println("Команды: /level N — сменить уровень | /diag — собрать диагностику | /report — отчёт |")
	fmt.Println("         /follow on|off — авто-продолжение | /clear — очистить контекст | exit — выход")
	fmt.Println()

	sessionID := time.Now().Format("2006-01-02_15-04-05") + "_" + hostname()
	sessionFile := filepath.Join(sessionsDir, sessionID+".json")

	var history []Message

	if *diagFlag && currentLevel >= L1Read {
		history = append(history, Message{Role: "user", Content: diagnosticsPrompt()})
	}

	for {
		fmt.Print("\nВы > ")
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}
		low := strings.ToLower(input)

		switch {
		case low == "exit" || low == "quit" || low == "выход":
			saveSession(sessionFile, sessionID, history)
			fmt.Println("\n[OK] Сессия сохранена. Выход.")
			return

		case low == "/help":
			fmt.Println("L0 чат | L1 чтение | L2 безопасные починки | L3 полный доступ")
			fmt.Println("/level N, /diag, /report, /follow on|off, /clear, exit")
			continue

		case strings.HasPrefix(low, "/level"):
			parts := strings.Fields(low)
			if len(parts) == 1 {
				fmt.Printf("Текущий уровень: %s\n", levelLabels[currentLevel])
				continue
			}
			n, err := strconv.Atoi(parts[1])
			if err != nil || n < 0 || n > 3 {
				fmt.Println("[ERROR] Уровень должен быть 0..3")
				continue
			}
			if n > currentLevel {
				fmt.Printf("[!] Повышение доступа %s → %s подтверждаете? Введите yes: ", levelLabels[currentLevel], levelLabels[n])
				if !confirm(reader, "", "yes") {
					fmt.Println("Отменено.")
					continue
				}
			}
			currentLevel = n
			cfg.AccessLevel = n
			fmt.Printf("[OK] Уровень доступа: %s\n", levelLabels[n])
			continue

		case low == "/follow on":
			cfg.AutoFollowup = true
			fmt.Println("[OK] Авто-продолжение включено.")
			continue
		case low == "/follow off":
			cfg.AutoFollowup = false
			fmt.Println("[OK] Авто-продолжение выключено.")
			continue

		case low == "/clear":
			history = nil
			fmt.Println("[OK] Контекст очищен.")
			continue

		case low == "/diag":
			if currentLevel < L1Read {
				fmt.Println("[!] Нужен уровень L1+ (сейчас L0). Соберите снимок вручную или поднимите уровень.")
				continue
			}
			snap, _, _ := runCommand(readOnlyDiag(), 60*time.Second)
			logAction(currentLevel, "diag-snapshot", VReadOnly, true, 0, snap)
			histFile := filepath.Join(logsDir, "fast-sysinfo_"+sessionID+".txt")
			_ = os.WriteFile(histFile, []byte(snap), 0o644)
			history = append(history, Message{Role: "user", Content: "Данные диагностики гостевого ПК:\n" + truncate(snap, 6000)})
			fmt.Printf("[OK] Снимок собран: %s (%d байт)\n", histFile, len(snap))
			continue

		case low == "/report":
			history = append(history, Message{Role: "user", Content: "Сформируй итоговый технический отчёт на русском в Markdown: проблема, что нашли, что сделали, статус, рекомендации. Без блоков кода с командами."})
			report, p, ttft, err := streamAnswer(cfg, history)
			if err != nil {
				fmt.Printf("[ERROR] %v\n", err)
				history = history[:len(history)-1]
				continue
			}
			history = append(history, Message{Role: "assistant", Content: report})
			path := filepath.Join(reportsDir, "REPORT_FAST_"+sessionID+".md")
			_ = os.WriteFile(path, []byte(report), 0o644)
			fmt.Printf("\n[OK] Отчёт: %s (%s, ttft %.2fs)\n", path, p.Name, ttft.Seconds())
			saveSession(sessionFile, sessionID, history)
			continue
		}

		history = append(history, Message{Role: "user", Content: input})
		answer, provider, ttft, err := streamAnswer(cfg, history)
		if err != nil {
			fmt.Printf("\n[ERROR] %v\n", err)
			history = history[:len(history)-1]
			continue
		}
		history = append(history, Message{Role: "assistant", Content: answer})
		reasoningNote := ""
		if lastReasoningChars > 0 && !cfg.ShowReasoning {
			reasoningNote = fmt.Sprintf(" | размышлений %d симв. (%.2f с, скрыты)", lastReasoningChars, lastReasoningDur.Seconds())
		}
		fmt.Printf("\n[v] %s | ttft %.2f с | ответ %d симв. | уровень L%d%s\n",
			provider.Name, ttft.Seconds(), len(answer), currentLevel, reasoningNote)

		// Выполнение команд, предложенных моделью (в рамках уровня доступа).
		cmds := extractCommands(answer)
		for _, cmd := range cmds {
			res := handleCommand(reader, cfg, cmd)
			if !res.handled {
				// Модель обязана знать, что команда НЕ выполнена и почему: иначе
				// она считает её успешной и/или предлагает тот же вариант снова.
				history = append(history, Message{Role: "user",
					Content: fmt.Sprintf("Команда `%s` НЕ выполнена: %s.", cmd, res.note)})
				if cfg.AutoFollowup {
					follow, _, _, ferr := streamAnswer(cfg, history)
					if ferr == nil {
						history = append(history, Message{Role: "assistant", Content: follow})
					} else {
						fmt.Printf("\n[ERROR] продолжение не удалось: %v\n", ferr)
						history = history[:len(history)-1]
					}
				}
				continue
			}
			// В историю уходит РЕАЛЬНЫЙ код возврата и длительность — раньше здесь
			// был жёстко зашитый «код 0», из-за чего модель докладывала об успехе
			// при коде 1 и пустом выводе (найдено в третьем полевом прогоне).
			report := fmt.Sprintf("Вывод команды `%s` (код %d, %.2f с):\n%s",
				cmd, res.code, res.dur.Seconds(), truncate(res.out, 4000))
			if strings.TrimSpace(res.out) == "" {
				report += "(вывод пустой)"
			} else if res.code != 0 {
				report += fmt.Sprintf("\n(обрати внимание: код возврата %d — команда завершилась с ошибкой)", res.code)
			}
			history = append(history, Message{Role: "user", Content: report})
			if cfg.AutoFollowup {
				follow, _, _, ferr := streamAnswer(cfg, history)
				if ferr == nil {
					history = append(history, Message{Role: "assistant", Content: follow})
				} else {
					fmt.Printf("\n[ERROR] продолжение не удалось: %v\n", ferr)
					history = history[:len(history)-1]
				}
			}
		}

		saveSession(sessionFile, sessionID, history)
	}
}

// streamAnswer — единая точка стриминга с печатью «по мере поступления».
func streamAnswer(cfg Config, history []Message) (string, Provider, time.Duration, error) {
	msgs := buildMessages(cfg, history)
	fmt.Print("\nAI > ")
	text, provider, ttft, err := sendMessage(cfg, msgs, func(piece string) {
		fmt.Print(piece)
	})
	fmt.Println()
	if err != nil {
		return "", provider, 0, err
	}
	return text, provider, ttft, nil
}

// buildMessages: короткий системный промпт с текущей политикой доступа +
// подрезанная история (скорость важнее «памяти» всей переписки).
func buildMessages(cfg Config, history []Message) []Message {
	msgs := []Message{{Role: "system", Content: systemPrompt()}}
	if len(history) > cfg.HistoryLimit {
		history = history[len(history)-cfg.HistoryLimit:]
	}
	return append(msgs, history...)
}

func systemPrompt() string {
	var allowed string
	switch currentLevel {
	case L0Chat:
		allowed = "Команды запускать НЕЛЬЗЯ (уровень L0). Отвечай текстом и объясняй шаги."
	case L1Read:
		allowed = "Разрешено предлагать ТОЛЬКО команды чтения (уровень L1): ipconfig, systeminfo, Get-*, netstat, tasklist, nslookup, reg query и т.п."
	case L2Fix:
		allowed = "Разрешены команды чтения и безопасные починки (уровень L2): ipconfig /flushdns, netsh winsock reset, restart-service, sfc /scannow, dism restorehealth. Каждая будет подтверждена оператором."
	default:
		allowed = "Разрешены любые команды (уровень L3), но оператор подтверждает каждую; опасные требуют двойного подтверждения."
	}
	return "Ты — карманный AI-инженер для выездной диагностики Windows/Linux. Отвечай кратко и по делу. " +
		"Команду для запуска на гостевом ПК давай ОДНИМ блоком кода (```cmd ... ``` или ```bash ... ```), по одной команде в строке. " +
		"Если команда для этого уровня запрещена — так и скажи. " + allowed +
		" Всегда сверяйся со строкой `-> код N` и с блоком «Вывод команды … (код N)»: код != 0 или пустой вывод " +
		"означают, что данных нет — в этом случае НЕ утверждай, что всё сработало, а назови вероятную причину " +
		"(нет прав, фильтр не нашёл совпадений, сервис не запущен) и предложи другую команду. " +
		"Русская Windows выводит ЛОКАЛИЗОВАННЫЕ подписи полей: в systeminfo это «Имя ОС», «Версия ОС», «Тип системы», " +
		"поэтому не фильтруй вывод по английским подписям — `findstr /C:\"OS Name\"` на русской системе не найдёт ничего. " +
		"Для сведений об ОС и железе бери команды, не зависящие от локали: " +
		"`wmic os get Caption,Version,BuildNumber,OSArchitecture /value`, " +
		"`Get-CimInstance Win32_OperatingSystem | Select-Object Caption,Version,BuildNumber,OSArchitecture | Format-List`, " +
		"`ver`, `systeminfo` (без фильтра); для дисков — `wmic logicaldisk get DeviceID,Size,FreeSpace,VolumeName /value`."
}

func diagnosticsPrompt() string {
	return "Собери экспресс-диагностику (только чтение): ОС, CPU, RAM, диски, сеть, свежие ошибки журнала. " +
		"Дай блок команд для сбора данных."
}

// readOnlyDiag — снимок системы только read-only командами.
func readOnlyDiag() string {
	if runtime.GOOS == "windows" {
		// Подписи полей локализованы, поэтому в снимок идут и команды, не
		// зависящие от языка системы (WMI/CIM отдают одни и те же имена полей).
		return "wmic os get Caption,Version,BuildNumber,OSArchitecture /value; ver; systeminfo; " +
			"wmic logicaldisk get DeviceID,Size,FreeSpace,VolumeName /value; " +
			"ipconfig /all; Get-PSDrive -PSProvider FileSystem | Select-Object Name,Used,Free; " +
			"Get-EventLog -LogName System -EntryType Error -Newest 15 | Select-Object TimeGenerated,Source,Message | Format-List"
	}
	return "uname -a; uptime; free -h; df -h; ip -br addr; journalctl -p err -n 15 --no-pager 2>/dev/null | tail -20"
}

// cmdResult — итог обработки команды. Модели нужен РЕАЛЬНЫЙ код возврата и
// причина отказа: иначе она считает невыполненную команду успешной (был баг:
// в историю уходил жёстко зашитый «код 0», и агент докладывал «код 0» при коде 1).
type cmdResult struct {
	handled bool
	out     string
	code    int
	dur     time.Duration
	note    string // причина отказа, когда handled == false
}

// handleCommand — политика + подтверждение + запуск + журнал.
func handleCommand(reader *bufio.Reader, cfg Config, cmd string) cmdResult {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return cmdResult{note: "пустая команда"}
	}
	verdict := classify(cmd, currentLevel)

	switch verdict {
	case VForbidden:
		if currentLevel == L0Chat {
			fmt.Printf("\n[BLOCK] L0: команда не выполняется: %s\n", cmd)
		} else {
			fmt.Printf("\n[BLOCK] Команда запрещена на уровне L%d: %s\n", currentLevel, cmd)
			fmt.Println("        Если она действительно нужна — поднимите уровень: /level 3 (для опасных — двойное подтверждение).")
		}
		logAction(currentLevel, cmd, verdict, false, -1, "")
		return cmdResult{note: fmt.Sprintf("запрещена политикой уровня L%d", currentLevel)}

	case VNotAllowed:
		fmt.Printf("\n[BLOCK] Команды нет в белом списке уровня L%d: %s\n", currentLevel, cmd)
		fmt.Println("        Разрешить может только уровень L3 (/level 3).")
		logAction(currentLevel, cmd, verdict, false, -1, "")
		return cmdResult{note: fmt.Sprintf("нет в белом списке уровня L%d (нужен L3)", currentLevel)}

	case VReadOnly:
		fmt.Printf("\n[cmd] %s  (%s)\n", cmd, verdict)
	case VNeedsYes, VDangerous:
		fmt.Printf("\n[cmd] %s  (%s)\n", cmd, verdict)
		if verdict == VDangerous {
			fmt.Println("      ВНИМАНИЕ: команда может изменить/удалить данные или настройки системы.")
			if !confirm(reader, "      Запустить? Введите yes для подтверждения: ", "yes") {
				fmt.Println("      Отменено оператором.")
				logAction(currentLevel, cmd, verdict, false, -1, "")
				return cmdResult{note: "оператор не подтвердил запуск"}
			}
		} else if !confirm(reader, "      Запустить? (y/n): ", "y") {
			fmt.Println("      Отменено оператором.")
			logAction(currentLevel, cmd, verdict, false, -1, "")
			return cmdResult{note: "оператор отказался"}
		}
	}

	start := time.Now()
	out, code, _ := runCommand(cmd, 120*time.Second)
	dur := time.Since(start)
	fmt.Printf("      -> код %d, %.2f с\n", code, dur.Seconds())
	if strings.TrimSpace(out) == "" {
		// Пустой вывод — это тоже сигнал: команда не дала данных (фильтр не нашёл
		// совпадений, нет прав, ошибка). Явно проговариваем, чтобы модель не
		// докладывала об успехе.
		if code == 0 {
			fmt.Println("      [i] вывод пуст при коде 0 — данных нет (например, фильтр ничего не нашёл).")
		} else {
			fmt.Printf("      [i] вывод пуст при коде %d — успех не подтверждён.\n", code)
		}
	} else {
		fmt.Printf("%s\n", strings.TrimRight(truncate(out, 4000), "\n"))
	}
	logAction(currentLevel, cmd, verdict, true, code, out)
	return cmdResult{handled: true, out: out, code: code, dur: dur}
}

var _ = sort.Strings // (зарезервировано под сортировку артефактов сессий)
