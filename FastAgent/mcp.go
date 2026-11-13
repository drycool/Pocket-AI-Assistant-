package main

// Этап 10: MCP-клиент (stdio).
//
// Зачем. Declarative tools внутри бинарника — это read_file/grep/glob/write_file,
// про которые агент знает заранее. MCP добавляет то, чего в бинарнике нет и не
// должно быть: внешние инструменты (filesystem, git, базы, браузер) подключаются
// отдельными процессами и не требуют пересборки агента. Ровно так это устроено
// в llamafile-мире: одно ядро, а инструменты — сменные.
//
// Границы, которые я выбрал сознательно:
//   - MCP-инструменты показываются модели ТОЛЬКО на L3 и требуют подтверждения:
//     мы не можем проклассифицировать чужой инструмент по его описанию, поэтому
//     «чтение по умолчанию» для них было бы обманом;
//   - ответ ограничен по размеру, вызов — по времени; зависший сервер
//     помечается нерабочим, а не подвешивает сессию;
//   - инструменты живут под именами mcp__<сервер>__<инструмент>: видно, откуда
//     пришёл вызов, без хождения по реестру.
//
// Транспорт — JSON-RPC 2.0 построчно через stdin/stdout, как в спецификации MCP
// (stdio transport). Протокол: initialize → notifications/initialized →
// tools/list (с пагинацией) → tools/call.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	mcpDefaultTimeout = 20 * time.Second
	mcpMaxResponse    = 256 << 10 // ответ инструмента, который попадает в историю
	mcpMaxStderr      = 8 << 10
)

// MCPConfig — блок "mcp" в config.fast.json.
type MCPConfig struct {
	Servers []MCPServerConfig `json:"servers"`
}

type MCPServerConfig struct {
	Name       string   `json:"name"`
	Command    string   `json:"command"`
	Args       []string `json:"args"`
	Env        []string `json:"env,omitempty"` // "KEY=VALUE"
	TimeoutSec int      `json:"timeout_sec,omitempty"`
}

type mcpClient struct {
	name    string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	out     *bufio.Reader
	stderr  *limitedBuffer
	mu      sync.Mutex
	nextID  int
	broken  bool
	timeout time.Duration
	tools   []*mcpToolRef
}

type mcpToolRef struct {
	Client      *mcpClient
	Exposed     string // mcp__server__tool
	Orig        string // имя на стороне сервера
	Description string
	Schema      json.RawMessage
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"` // уведомления (id нет)
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// limitedBuffer — stderr сервера: для диагностики, но без риска съесть память.
type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.max <= 0 {
		l.max = mcpMaxStderr
	}
	if l.buf.Len() < l.max {
		room := l.max - l.buf.Len()
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil // всегда «всё принято»: процесс не должен падать из-за логов
}

func (l *limitedBuffer) String() string { return strings.TrimSpace(l.buf.String()) }

var (
	mcpClients []*mcpClient
	mcpIndex   = map[string]*mcpToolRef{}
)

var reMCPName = regexp.MustCompile(`[^a-z0-9_]+`)

// sanitizeMCPName: имена инструментов в OpenAI API ограничены 64 символами
// [a-zA-Z0-9_-]. Приводим к нижнему регистру и подчёркиваниям.
func sanitizeMCPName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = reMCPName.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		s = "tool"
	}
	return s
}

// initMCP запускает серверы из конфига. Ошибки не фатальны: нет MCP — агент
// работает со встроенными инструментами, как раньше.
func initMCP(cfg Config) {
	if len(cfg.MCP.Servers) == 0 {
		return
	}
	for _, sc := range cfg.MCP.Servers {
		if strings.TrimSpace(sc.Command) == "" {
			fmt.Printf("[MCP] %s: не задан command — пропущен\n", sc.Name)
			continue
		}
		c, err := startMCPClient(sc)
		if err != nil {
			fmt.Printf("[MCP] %s: запуск: %v\n", sc.Name, err)
			continue
		}
		if err := c.initialize(); err != nil {
			fmt.Printf("[MCP] %s: инициализация: %v\n", sc.Name, err)
			c.kill()
			continue
		}
		refs, err := c.listTools()
		if err != nil {
			fmt.Printf("[MCP] %s: tools/list: %v\n", sc.Name, err)
			c.kill()
			continue
		}
		c.tools = refs
		mcpClients = append(mcpClients, c)
		for _, r := range refs {
			if _, exists := mcpIndex[r.Exposed]; exists {
				fmt.Printf("[MCP] %s: имя %q уже занято — инструмент пропущен\n", sc.Name, r.Exposed)
				continue
			}
			mcpIndex[r.Exposed] = r
		}
		fmt.Printf("[MCP] %s: инструментов %d (%s)\n", sc.Name, len(refs), sc.Command)
	}
	if n := len(mcpIndex); n > 0 {
		fmt.Printf("[MCP] всего внешних инструментов: %d (доступны только на L3 с подтверждением)\n", n)
	}
}

func closeMCP() {
	for _, c := range mcpClients {
		c.kill()
	}
	mcpClients = nil
	mcpIndex = map[string]*mcpToolRef{}
}

func startMCPClient(sc MCPServerConfig) (*mcpClient, error) {
	name := sc.Name
	if name == "" {
		name = sc.Command
	}
	cmd := exec.Command(sc.Command, sc.Args...)
	cmd.Env = append(os.Environ(), sc.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errBuf := &limitedBuffer{max: mcpMaxStderr}
	cmd.Stderr = errBuf
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	tmo := mcpDefaultTimeout
	if sc.TimeoutSec > 0 {
		tmo = time.Duration(sc.TimeoutSec) * time.Second
	}
	return &mcpClient{
		name:    name,
		cmd:     cmd,
		stdin:   stdin,
		out:     bufio.NewReaderSize(stdout, 256<<10),
		stderr:  errBuf,
		timeout: tmo,
	}, nil
}

// call — синхронный JSON-RPC запрос с таймаутом. Уведомления сервера
// (строки без id) пропускаются: они не ответ на наш запрос.
func (c *mcpClient) call(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken {
		return nil, errors.New("сервер MCP помечен нерабочим (таймаут/обрыв)")
	}
	id := c.nextID
	c.nextID++
	raw, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	if _, err := c.stdin.Write(append(raw, '\n')); err != nil {
		c.broken = true
		return nil, fmt.Errorf("запись: %w", err)
	}

	type result struct {
		raw json.RawMessage
		err error
	}
	ch := make(chan result, 1)
	go func() {
		for {
			line, err := c.out.ReadString('\n')
			if err != nil {
				ch <- result{err: err}
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var resp rpcResponse
			if json.Unmarshal([]byte(line), &resp) != nil {
				continue
			}
			if resp.Method != "" && len(resp.ID) == 0 {
				continue // notification
			}
			if string(resp.ID) != strconv.Itoa(id) {
				continue // ответ на другой запрос (или шум)
			}
			if resp.Error != nil {
				ch <- result{err: fmt.Errorf("MCP %s: %s (код %d)", method, resp.Error.Message, resp.Error.Code)}
				return
			}
			ch <- result{raw: resp.Result}
			return
		}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			c.broken = true
			if c.stderr.String() != "" {
				return nil, fmt.Errorf("%v [stderr: %s]", r.err, truncate(c.stderr.String(), 400))
			}
		}
		return r.raw, r.err
	case <-time.After(c.timeout):
		c.broken = true
		go c.kill() // освободить горутину чтения
		return nil, fmt.Errorf("MCP %s: таймаут %s", method, c.timeout)
	}
}

func (c *mcpClient) notify(method string, params any) {
	raw, _ := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
	_, _ = c.stdin.Write(append(raw, '\n'))
}

func (c *mcpClient) initialize() error {
	_, err := c.call("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "fastagent", "version": "1.0"},
	})
	if err != nil {
		return err
	}
	c.notify("notifications/initialized", map[string]any{})
	return nil
}

func (c *mcpClient) listTools() ([]*mcpToolRef, error) {
	var refs []*mcpToolRef
	cursor := ""
	for page := 0; page < 5; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.call("tools/list", params)
		if err != nil {
			return nil, err
		}
		var res struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("разбор tools/list: %w", err)
		}
		for _, t := range res.Tools {
			schema := t.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			refs = append(refs, &mcpToolRef{
				Client:      c,
				Orig:        t.Name,
				Exposed:     sanitizeMCPName("mcp__" + c.name + "__" + t.Name),
				Description: t.Description,
				Schema:      schema,
			})
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	// Обрезаем имена до лимита OpenAI (64).
	for _, r := range refs {
		if len(r.Exposed) > 64 {
			r.Exposed = clipRunes(r.Exposed, 64)
		}
	}
	return refs, nil
}

func (c *mcpClient) kill() {
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_, _ = c.cmd.Process.Wait()
}

// --- интеграция с реестром инструментов ---

func mcpToolDefs() []ToolDef {
	var defs []ToolDef
	for name, ref := range mcpIndex {
		desc := ref.Description
		if desc == "" {
			desc = "Внешний MCP-инструмент"
		}
		defs = append(defs, ToolDef{Type: "function", Function: ToolDefinition{
			Name: name, Description: desc, Parameters: ref.Schema,
		}})
	}
	return defs
}

func lookupMCPTool(name string) *mcpToolRef {
	return mcpIndex[name]
}

func callMCPTool(name string, args map[string]any) (string, error) {
	ref := mcpIndex[name]
	if ref == nil {
		return "", fmt.Errorf("MCP-инструмент %q не найден", name)
	}
	raw, err := ref.Client.call("tools/call", map[string]any{
		"name":      ref.Orig,
		"arguments": args,
	})
	if err != nil {
		return "", err
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("разбор результата: %w", err)
	}
	var b strings.Builder
	for _, part := range res.Content {
		if part.Text != "" {
			b.WriteString(part.Text)
			b.WriteString("\n")
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		out = string(raw)
	}
	out = truncate(out, mcpMaxResponse)
	if res.IsError {
		return "", fmt.Errorf("%s", out)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Тестовый MCP-сервер: тот же транспорт, но собранный здесь же. Он позволяет
// проверять клиент в subprocess-тесте без npx, сети и чужих зависимостей.
// ---------------------------------------------------------------------------

func runMCPTestServer() {
	in := bufio.NewReader(os.Stdin)
	write := func(v any) {
		raw, _ := json.Marshal(v)
		_, _ = os.Stdout.Write(append(raw, '\n'))
	}
	reply := func(id json.RawMessage, result any) {
		write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
	}
	for {
		line, err := in.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var req rpcRequest
		if json.Unmarshal([]byte(line), &req) != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			reply(json.RawMessage(strconv.Itoa(req.ID)), map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fastagent-test", "version": "1"},
			})
		case "notifications/initialized":
			// уведомление — без ответа
		case "tools/list":
			reply(json.RawMessage(strconv.Itoa(req.ID)), map[string]any{
				"tools": []any{
					map[string]any{
						"name":        "echo",
						"description": "Возвращает переданный текст",
						"inputSchema": map[string]any{
							"type":       "object",
							"properties": map[string]any{"text": map[string]any{"type": "string"}},
							"required":   []string{"text"},
						},
					},
					map[string]any{
						"name":        "add",
						"description": "Складывает два числа",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"a": map[string]any{"type": "number"},
								"b": map[string]any{"type": "number"},
							},
							"required": []string{"a", "b"},
						},
					},
				},
			})
		case "tools/call":
			m, _ := req.Params.(map[string]any)
			name, _ := m["name"].(string)
			args, _ := m["arguments"].(map[string]any)
			switch name {
			case "echo":
				text, _ := args["text"].(string)
				reply(json.RawMessage(strconv.Itoa(req.ID)), map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "echo: " + text}},
				})
			case "add":
				a, _ := args["a"].(float64)
				b, _ := args["b"].(float64)
				reply(json.RawMessage(strconv.Itoa(req.ID)), map[string]any{
					"content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("%g", a+b)}},
				})
			default:
				reply(json.RawMessage(strconv.Itoa(req.ID)), map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "unknown tool"}},
					"isError": true,
				})
			}
		default:
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      json.RawMessage(strconv.Itoa(req.ID)),
				"error":   map[string]any{"code": -32601, "message": "method not found"},
			})
		}
	}
}
