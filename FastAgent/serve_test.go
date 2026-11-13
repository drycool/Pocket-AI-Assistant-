package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubbedProvider поднимает фальшивый OpenAI-провайдер: он отвечает тем же
// контрактом, что DeepSeek/Ollama, поэтому путь sendMessage проверяется
// целиком, без сети и без ключей.
func stubbedProvider(t *testing.T, content string) (*httptest.Server, Config) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("запрос ушёл не туда: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"`+content+`"},"finish_reason":"stop"}]}`)
	}))
	cfg := Config{
		Stream:               false,
		MaxTokens:            100,
		FirstTokenTimeoutSec: 5,
		TotalTimeoutSec:      10,
		HistoryLimit:         10,
		Providers: []Provider{{
			Name: "stub", Type: "openai", BaseURL: up.URL, APIKey: "k", Model: "m",
		}},
	}
	return up, cfg
}

func TestServeChat(t *testing.T) {
	up, cfg := stubbedProvider(t, "pong")
	defer up.Close()

	srv := httptest.NewServer(newServeState(cfg, 1, "").handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/chat", "application/json",
		strings.NewReader(`{"message":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("код %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["answer"] != "pong" || out["provider"] != "stub" {
		t.Fatalf("ответ: %#v", out)
	}
}

func TestServeOpenAICompatible(t *testing.T) {
	up, cfg := stubbedProvider(t, "pong")
	defer up.Close()

	srv := httptest.NewServer(newServeState(cfg, 0, "").handler())
	defer srv.Close()

	body := `{"model":"m","messages":[{"role":"user","content":"ping"}]}`
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("код %d", resp.StatusCode)
	}
	var out struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Object != "chat.completion" || len(out.Choices) != 1 ||
		out.Choices[0].Message.Content != "pong" || out.Choices[0].Message.Role != "assistant" {
		t.Fatalf("ответ OpenAI-формата: %#v", out)
	}
}

func TestServeOpenAIStream(t *testing.T) {
	up, cfg := stubbedProvider(t, "pong")
	defer up.Close()

	srv := httptest.NewServer(newServeState(cfg, 0, "").handler())
	defer srv.Close()

	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"ping"}]}`
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	text := string(raw)
	if !strings.Contains(text, `"content":"pong"`) || !strings.Contains(text, "data: [DONE]") {
		t.Fatalf("SSE-ответ: %q", text)
	}
}

// Без токена не-loopback обязан получить 403: сервер на чужом ПК не должен
// быть исполняющей поверхностью для всей сети.
func TestServeNonLoopbackWithoutToken(t *testing.T) {
	up, cfg := stubbedProvider(t, "pong")
	defer up.Close()

	st := newServeState(cfg, 1, "")
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		bytes.NewReader([]byte(`{"message":"ping"}`)))
	req.RemoteAddr = "10.0.0.5:12345"
	rec := httptest.NewRecorder()
	st.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ожидали 403, получили %d: %s", rec.Code, rec.Body.String())
	}
}

func TestServeTokenRequired(t *testing.T) {
	up, cfg := stubbedProvider(t, "pong")
	defer up.Close()

	srv := httptest.NewServer(newServeState(cfg, 1, "secret").handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/chat", "application/json", strings.NewReader(`{"message":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("без токена ожидали 401, получили %d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/chat", strings.NewReader(`{"message":"ping"}`))
	req.Header.Set("Authorization", "Bearer secret")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("с токеном ожидали 200, получили %d", resp2.StatusCode)
	}
}

func TestServeDiagForbiddenAtL0(t *testing.T) {
	st := newServeState(Config{}, 0, "")
	req := httptest.NewRequest(http.MethodGet, "/api/diag", nil)
	rec := httptest.NewRecorder()
	st.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("L0: ожидали 403, получили %d", rec.Code)
	}
}
