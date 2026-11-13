package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Тесты слоя знаний. Проверяем не «что-то пришло», а контракт с Jev:
// какие поля уходят в запрос и как разбирается ответ при execute=true/false
// и в режиме графа. Формат ответа снят с живого роутера, не выдуман.

func TestQueryRagRouterExecute(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"decision_id":      "abc123",
			"agent_response":   "Сбросьте кэш DNS командой ipconfig /flushdns.",
			"routing_decision": map[string]any{"strategy": "exact_fts"},
			"elapsed_ms":       float64(770),
			"degraded":         false,
		})
	}))
	defer srv.Close()

	ans, err := queryRag(context.Background(), RagConfig{URL: srv.URL, Execute: true}, "как сбросить DNS", false)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if got["query"] != "как сбросить DNS" {
		t.Errorf("в запрос ушёл не тот вопрос: %v", got["query"])
	}
	if got["execute"] != true {
		t.Errorf("execute не передан: %v", got["execute"])
	}
	if ans.Text != "Сбросьте кэш DNS командой ipconfig /flushdns." {
		t.Errorf("текст: %q", ans.Text)
	}
	if ans.Strategy != "exact_fts" || ans.ElapsedMS != 770 || ans.DecisionID != "abc123" || ans.Source != "router" {
		t.Errorf("метаданные: %+v", ans)
	}
	if ans.Degraded {
		t.Error("degraded не должен быть true")
	}
}

func TestQueryRagMaterialWhenNoAgentResponse(t *testing.T) {
	// execute=false: роутер отдаёт материал в context, а agent_response пуст.
	// Если брать только agent_response, «память» будет притворяться пустой.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"agent_response":   "",
			"context":          "Фрагмент: ipconfig /flushdns очищает кэш.",
			"routing_decision": map[string]any{"strategy": "exact_fts"},
			"decision_id":      "d42",
		})
	}))
	defer srv.Close()

	ans, err := queryRag(context.Background(), RagConfig{URL: srv.URL, Execute: false}, "вопрос", false)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if !strings.Contains(ans.Text, "ipconfig /flushdns") {
		t.Errorf("не взят материал из context: %q", ans.Text)
	}
}

func TestQueryRagGraphMode(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": "Синтез графа.",
		})
	}))
	defer srv.Close()

	ans, err := queryRag(context.Background(), RagConfig{URL: "http://unused", GraphURL: srv.URL}, "вопрос", true)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if got["mode"] != "hybrid" {
		t.Errorf("граф вызван без mode=hybrid: %v", got["mode"])
	}
	if ans.Text != "Синтез графа." || ans.Source != "graph" {
		t.Errorf("ответ графа: %+v", ans)
	}
}

func TestQueryRagErrors(t *testing.T) {
	cases := []struct {
		name string
		cfg  RagConfig
		q    string
		fg   bool
		want string
	}{
		{"нет url", RagConfig{}, "вопрос", false, "rag.url"},
		{"нет graph_url", RagConfig{URL: "http://x"}, "вопрос", true, "rag.graph_url"},
		{"пустой вопрос", RagConfig{URL: "http://x"}, "   ", false, "пустой вопрос"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := queryRag(context.Background(), c.cfg, c.q, c.fg)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("ожидали ошибку про %q, получили %v", c.want, err)
			}
		})
	}
}

func TestQueryRagHTTPErrorAndEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "bad") {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	_, err := queryRag(context.Background(), RagConfig{URL: srv.URL + "/bad"}, "вопрос", false)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("ожидали ошибку HTTP 500, получили %v", err)
	}

	_, err = queryRag(context.Background(), RagConfig{URL: srv.URL}, "вопрос", false)
	if err == nil || !strings.Contains(err.Error(), "пусто") {
		t.Fatalf("ожидали ошибку про пустой ответ, получили %v", err)
	}
}

func TestRagMetaLine(t *testing.T) {
	a := RagAnswer{Source: "router", Strategy: "exact_fts", ElapsedMS: 770, Degraded: true, Fallback: "fts_fallback", DecisionID: "x1"}
	line := a.MetaLine()
	for _, want := range []string{"источник=router", "strategy=exact_fts", "elapsed=770ms", "degraded=true", "fallback=fts_fallback", "decision_id=x1"} {
		if !strings.Contains(line, want) {
			t.Errorf("в метастроке нет %q: %s", want, line)
		}
	}
}
