package main

// Этап 9: встроенный сервер как у llamafile — «запустил один файл, открылся
// интерфейс». Но с честными границами, которых у llamafile нет и быть не может
// (он не исполняет чужие команды на чужом ПК):
//
//   - сервер слушает ТОЛЬКО 127.0.0.1 по умолчанию;
//   - без токена доступ только с loopback;
//   - в HTTP-режиме команды НЕ выполняются. Ни через /api/chat, ни через
//     /v1/chat/completions. Иначе любой в той же сети получил бы исполнение
//     команд на гостевом ПК — а он чужой.
//   - диагностика (/api/diag) — только чтение и только при уровне L1+.
//
// /v1/chat/completions сделан OpenAI-совместимым, чтобы к агенту можно было
// подключить Page Assist или Open WebUI, как к llamafile, и не привязываться
// к нашему UI.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed ui.html
var uiHTML []byte

type serveState struct {
	cfg   Config
	level int
	token string
	mu    sync.Mutex
	hist  []Message
}

func newServeState(cfg Config, level int, token string) *serveState {
	return &serveState{cfg: cfg, level: level, token: token}
}

func (s *serveState) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.guard(s.handleIndex))
	mux.HandleFunc("/api/chat", s.guard(s.handleChat))
	mux.HandleFunc("/api/diag", s.guard(s.handleDiag))
	mux.HandleFunc("/api/report", s.guard(s.handleReport))
	mux.HandleFunc("/v1/chat/completions", s.guard(s.handleOpenAI))
	return mux
}

// guard — авторизация. Токен, если задан, обязателен для всех маршрутов;
// без токена пускаем только loopback (иначе «локальный» сервер на деле
// открыт всей сети).
func (s *serveState) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" {
			auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if strings.TrimSpace(auth) != s.token && r.URL.Query().Get("token") != s.token {
				writeJSON(w, http.StatusUnauthorized, map[string]string{
					"error": "нужен токен: Authorization: Bearer <token> (или ?token=)",
				})
				return
			}
		} else if !isLoopback(r.RemoteAddr) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "без токена сервер доступен только с localhost; задайте --token",
			})
			return
		}
		next(w, r)
	}
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeJSON(r *http.Request, v any, limit int64) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, limit)).Decode(v)
}

func (s *serveState) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(uiHTML)
}

// chat ответа модели без выполнения команд. cfg.Stream=false принудительно:
// в HTTP-ответе SSE-поток не нужен, ответ отдаётся целиком.
func (s *serveState) answer(msgs []Message) (string, Provider, time.Duration, error) {
	cfg := s.cfg
	cfg.Stream = false
	return sendMessage(cfg, msgs, nil)
}

func (s *serveState) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST"})
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := decodeJSON(r, &req, 1<<20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "пустое сообщение"})
		return
	}

	s.mu.Lock()
	hist := append(append([]Message{}, s.hist...), Message{Role: "user", Content: msg})
	s.mu.Unlock()

	answer, p, ttft, err := s.answer(buildMessages(s.cfg, hist))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	s.mu.Lock()
	s.hist = append(s.hist, Message{Role: "user", Content: msg}, Message{Role: "assistant", Content: answer})
	if n := s.cfg.HistoryLimit; n > 0 && len(s.hist) > n {
		s.hist = s.hist[len(s.hist)-n:]
	}
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"answer":   answer,
		"provider": p.Name,
		"ttft_ms":  ttft.Milliseconds(),
		"chars":    len(answer),
	})
}

func (s *serveState) handleDiag(w http.ResponseWriter, r *http.Request) {
	if s.level < L1Read {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("диагностика требует L1+, сейчас %s", levelLabels[s.level]),
		})
		return
	}
	snap, _, _ := runCommand(readOnlyDiag(), 60*time.Second)
	logAction(s.level, "http-diag-snapshot", VReadOnly, true, 0, snap)
	writeJSON(w, http.StatusOK, map[string]any{"diag": snap, "chars": len(snap)})
}

func (s *serveState) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST"})
		return
	}
	s.mu.Lock()
	hist := append(append([]Message{}, s.hist...),
		Message{Role: "user", Content: "Сформируй итоговый технический отчёт на русском в Markdown: проблема, что нашли, что сделали, статус, рекомендации. Без блоков кода с командами."})
	s.mu.Unlock()

	report, p, _, err := s.answer(buildMessages(s.cfg, hist))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	path := filepath.Join(reportsDir, "REPORT_FAST_HTTP_"+time.Now().Format("2006-01-02_15-04-05")+".md")
	if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.mu.Lock()
	s.hist = append(s.hist, Message{Role: "user", Content: "Сформируй отчёт"}, Message{Role: "assistant", Content: report})
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "provider": p.Name, "chars": len(report)})
}

// handleOpenAI — OpenAI-совместимый /v1/chat/completions.
// stream:true поддержан одним финальным чанком (не посимвольный стриминг):
// клиент получает корректный SSE и ответ, а мы не врём в документации.
func (s *serveState) handleOpenAI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST"})
		return
	}
	var req struct {
		Model    string    `json:"model"`
		Messages []Message `json:"messages"`
		Stream   bool      `json:"stream"`
	}
	if err := decodeJSON(r, &req, 8<<20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(req.Messages) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "messages пуст"})
		return
	}
	msgs := append([]Message{{Role: "system", Content: systemPrompt()}}, req.Messages...)
	answer, _, _, err := s.answer(msgs)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	model := req.Model
	if model == "" {
		model = "fastagent"
	}
	id := fmt.Sprintf("chatcmpl-fastagent-%d", time.Now().UnixNano())

	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		chunk := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model,
			"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{"role": "assistant", "content": answer},
				"finish_reason": "stop",
			}},
		}
		raw, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", raw)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "object": "chat.completion", "created": time.Now().Unix(), "model": model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": answer},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	})
}

// runServe запускает встроенный интерфейс. Слушаем только loopback: это
// инструмент на чужом ПК, а не сервис для сети. --host не добавлен намеренно.
func runServe(cfg Config, level, port int, token string) {
	if level < 0 || level > 3 {
		level = 0
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           newServeState(cfg, level, token).handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Println("===================================================")
	fmt.Println("        FASTAGENT — встроенный интерфейс (--serve)")
	fmt.Println("===================================================")
	fmt.Printf("Откройте в Firefox Portable: http://%s\n", addr)
	if token == "" {
		fmt.Println("[i] Токен не задан: сервер принимает только localhost.")
	} else {
		fmt.Println("[i] Доступ по токену: Authorization: Bearer <token> (или ?token=).")
	}
	fmt.Println("[i] В HTTP-режиме команды НЕ выполняются — только чат и диагностика чтением.")
	fmt.Println("[i] OpenAI-совместимый endpoint: /v1/chat/completions. Ctrl+C — остановить.")
	if err := srv.ListenAndServe(); err != nil {
		fmt.Printf("[ERROR] сервер: %v\n", err)
		os.Exit(1)
	}
}
