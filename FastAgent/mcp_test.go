package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain — двойная роль: обычные тесты и «сервер MCP» для subprocess-теста.
// Тот же бинарник запускается с флагом -mcp-test-server и говорит по stdio,
// поэтому клиент проверяется целиком, без npx и чужих зависимостей.
func TestMain(m *testing.M) {
	for _, a := range os.Args[1:] {
		switch a {
		case "-mcp-test-server":
			runMCPTestServer()
			return
		case "-mcp-test-hang":
			// Сервер, который «завис»: читает, но не отвечает.
			time.Sleep(60 * time.Second)
			return
		}
	}
	os.Exit(m.Run())
}

func TestSanitizeMCPName(t *testing.T) {
	cases := map[string]string{
		"mcp__filesystem__read_file": "mcp__filesystem__read_file",
		"Mcp Filesystem.Read":        "mcp_filesystem_read",
		"":                           "tool",
	}
	for in, want := range cases {
		if got := sanitizeMCPName(in); got != want {
			t.Errorf("sanitizeMCPName(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

func TestMCPClientRoundTrip(t *testing.T) {
	c, err := startMCPClient(MCPServerConfig{
		Name: "test", Command: os.Args[0], Args: []string{"-mcp-test-server"}, TimeoutSec: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.kill()
	if err := c.initialize(); err != nil {
		t.Fatal(err)
	}
	refs, err := c.listTools()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("ожидались 2 инструмента, получено %d: %#v", len(refs), refs)
	}
	names := map[string]bool{}
	for _, r := range refs {
		names[r.Orig] = true
		if !strings.HasPrefix(r.Exposed, "mcp__test__") {
			t.Errorf("имя без префикса: %q", r.Exposed)
		}
	}
	if !names["echo"] || !names["add"] {
		t.Fatalf("нет ожидаемых инструментов: %#v", names)
	}
}

func TestInitMCPRegistersAndCalls(t *testing.T) {
	cfg := Config{MCP: MCPConfig{Servers: []MCPServerConfig{{
		Name: "test", Command: os.Args[0], Args: []string{"-mcp-test-server"}, TimeoutSec: 5,
	}}}}
	initMCP(cfg)
	defer closeMCP()

	if lookupMCPTool("mcp__test__echo") == nil {
		t.Fatal("mcp__test__echo не зарегистрирован")
	}
	out, err := callMCPTool("mcp__test__echo", map[string]any{"text": "привет"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "echo: привет") {
		t.Fatalf("ответ: %q", out)
	}

	var hasMCP bool
	for _, d := range toolDefsForLevel(L3Admin) {
		if d.Function.Name == "mcp__test__echo" {
			hasMCP = true
		}
	}
	if !hasMCP {
		t.Fatal("MCP-инструмент не попал в декларации L3")
	}
	// На L2 внешних инструментов быть не должно: они не классифицированы.
	for _, d := range toolDefsForLevel(L2Fix) {
		if strings.HasPrefix(d.Function.Name, "mcp__") {
			t.Fatalf("MCP-инструмент виден на L2: %s", d.Function.Name)
		}
	}
}

func TestMCPTimeoutDoesNotHang(t *testing.T) {
	c, err := startMCPClient(MCPServerConfig{
		Name: "hang", Command: os.Args[0], Args: []string{"-mcp-test-hang"}, TimeoutSec: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.kill()
	start := time.Now()
	err = c.initialize()
	if err == nil {
		t.Fatal("ожидалась ошибка таймаута")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "таймаут") {
		t.Fatalf("ожидался таймаут, получено: %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("клиент ждал слишком долго: %s", d)
	}
}
