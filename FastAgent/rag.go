package main

// Слой знаний FastAgent: Jev-роутер (FTS5 + материал) и LightRAG-граф.
//
// Зачем это в бинарнике, а не в scripts/rag_query.bat. У llamafile правильно
// разделено: у модели нет долговременной памяти, память — отдельный сервис,
// к которому модель ходит по HTTP. В admin-stick такой сервис уже есть (Jev),
// но field-путь к нему был батником, который требует curl и jq на ГОСТЕВОМ ПК.
// На чужом ноутбуке ни того, ни другого может не быть — и «память» молча
// перестаёт работать. Здесь тот же запрос делает сам агент: только net/http,
// ноль внешних утилит, ноль установки.
//
// Граф (LightRAG) — не автоматический fallback: замерено 22.09.2026 на
// Espero-мануале, что граф медленнее (37 с против 10 с) и на этом запросе
// ответил про другую тему. Поэтому он включается только явной командой
// `/rag graph` и никогда «сам, потому что роутер промолчал».

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	ragDefaultTimeout = 90 * time.Second  // роутер: exact FTS ~мс, синтез ~10 с
	ragGraphTimeout   = 150 * time.Second // граф: десятки секунд
)

// RagConfig — адреса слоя знаний. Пустой url = памяти нет, и агент честно
// говорит об этом, а не притворяется, что «ничего не нашлось».
type RagConfig struct {
	URL        string `json:"url"`                 // напр. http://192.168.11.111:8030
	GraphURL   string `json:"graph_url,omitempty"` // напр. http://192.168.11.111:8020
	Execute    bool   `json:"execute"`             // true: ответ синтезирует модель роутера
	TimeoutSec int    `json:"timeout_sec,omitempty"`
}

// RagAnswer — то, что реально пришло, вместе с решением роутера: миллисекунды и
// degraded не менее важны, чем текст, иначе «пусто» не отличить от «медленно».
type RagAnswer struct {
	Text       string
	Strategy   string
	ElapsedMS  int64
	Degraded   bool
	Fallback   string
	Source     string // "router" | "graph"
	DecisionID string
}

// MetaLine — одна строка для консоли и истории: видно, откуда ответ и почём.
func (a RagAnswer) MetaLine() string {
	parts := []string{"источник=" + a.Source}
	if a.Strategy != "" {
		parts = append(parts, "strategy="+a.Strategy)
	}
	if a.ElapsedMS > 0 {
		parts = append(parts, fmt.Sprintf("elapsed=%dms", a.ElapsedMS))
	}
	if a.Degraded {
		parts = append(parts, "degraded=true")
	}
	if a.Fallback != "" {
		parts = append(parts, "fallback="+a.Fallback)
	}
	if a.DecisionID != "" {
		parts = append(parts, "decision_id="+a.DecisionID)
	}
	return strings.Join(parts, " ")
}

// queryRag задаёт вопрос слою знаний. forceGraph — только по явной просьбе.
func queryRag(ctx context.Context, cfg RagConfig, question string, forceGraph bool) (RagAnswer, error) {
	q := strings.TrimSpace(question)
	if q == "" {
		return RagAnswer{}, errors.New("пустой вопрос")
	}

	base, source := cfg.URL, "router"
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = ragDefaultTimeout
	}
	if forceGraph {
		base, source = cfg.GraphURL, "graph"
		timeout = ragGraphTimeout
	}
	if strings.TrimSpace(base) == "" {
		if forceGraph {
			return RagAnswer{}, errors.New("rag.graph_url не задан в config.fast.json")
		}
		return RagAnswer{}, errors.New("rag.url не задан в config.fast.json — слой знаний не настроен")
	}

	body := map[string]any{"query": q}
	if forceGraph {
		body["mode"] = "hybrid"
	} else {
		body["execute"] = cfg.Execute
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return RagAnswer{}, err
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost,
		strings.TrimRight(base, "/")+"/query", bytes.NewReader(payload))
	if err != nil {
		return RagAnswer{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return RagAnswer{}, fmt.Errorf("слой знаний недоступен (%s): %w", base, err)
	}
	defer resp.Body.Close()

	raw, err := readAllLimited(resp.Body, 4<<20)
	if err != nil {
		return RagAnswer{}, fmt.Errorf("чтение ответа: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return RagAnswer{}, fmt.Errorf("слой знаний вернул HTTP %d: %s",
			resp.StatusCode, truncate(string(raw), 300))
	}

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return RagAnswer{}, fmt.Errorf("разбор ответа: %w", err)
	}

	ans := RagAnswer{Source: source, DecisionID: anyString(m["decision_id"])}
	// При execute=false роутер отдаёт материал в context, при execute=true —
	// синтез в agent_response. Пустой агентский ответ не должен прятать материал.
	ans.Text = firstNonEmpty(
		anyString(m["agent_response"]),
		anyString(m["answer"]),
		anyString(m["response"]),
		anyString(m["context"]),
		anyString(m["context_preview"]),
	)
	ans.ElapsedMS = anyInt64(m["elapsed_ms"])
	ans.Degraded = anyBool(m["degraded"])
	ans.Fallback = anyString(m["fallback_reason"])

	if rd, ok := m["routing_decision"].(map[string]any); ok {
		ans.Strategy = anyString(rd["strategy"])
	}
	if ans.Strategy == "" {
		ans.Strategy = anyString(m["strategy"])
	}
	if ans.Text == "" {
		return ans, fmt.Errorf("слой знаний ответил пусто (strategy=%s, degraded=%v)",
			ans.Strategy, ans.Degraded)
	}
	return ans, nil
}

// FeedbackAnswer — что роутер ответил на вердикт. Вердикт — единственная
// настоящая разметка этой системы: роутер не может судить свой ответ сам, а
// оператор, который видел справку, может.
type FeedbackAnswer struct {
	Verdict       string
	Recorded      bool
	KnownDecision bool
	Verdicts      int
	Previous      string
}

// postJevFeedback отправляет вердикт по decision_id. Источник — human: команду
// печатает человек за терминалом, а не модель. query уходит вместе с вердиктом,
// потому что в журнале решений хранится только хеш вопроса, и без него вердикт
// читателю ничего не скажет.
func postJevFeedback(cfg RagConfig, decisionID, verdict, query, comment string) (FeedbackAnswer, error) {
	decisionID = strings.TrimSpace(decisionID)
	if decisionID == "" {
		return FeedbackAnswer{}, errors.New("нет decision_id: вердикт ставится по ответу /rag")
	}
	switch verdict {
	case "accepted", "partial", "rejected":
	default:
		return FeedbackAnswer{}, fmt.Errorf("вердикт %q не подходит: accepted | partial | rejected", verdict)
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	if base == "" {
		return FeedbackAnswer{}, errors.New("rag.url не задан в config.fast.json — некуда отправлять вердикт")
	}
	payload, err := json.Marshal(map[string]any{
		"decision_id": decisionID,
		"verdict":     verdict,
		"source":      "human",
		"comment":     comment,
		"query":       query,
	})
	if err != nil {
		return FeedbackAnswer{}, err
	}
	cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, base+"/feedback", bytes.NewReader(payload))
	if err != nil {
		return FeedbackAnswer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return FeedbackAnswer{}, fmt.Errorf("вердикт не отправлен (%s): %w", base, err)
	}
	defer resp.Body.Close()
	raw, err := readAllLimited(resp.Body, 64<<10)
	if err != nil {
		return FeedbackAnswer{}, fmt.Errorf("чтение ответа: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return FeedbackAnswer{}, fmt.Errorf("роутер вернул HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return FeedbackAnswer{}, fmt.Errorf("разбор ответа: %w", err)
	}
	return FeedbackAnswer{
		Verdict:       anyString(m["verdict"]),
		Recorded:      anyBool(m["recorded"]),
		KnownDecision: anyBool(m["known_decision"]),
		Verdicts:      int(anyInt64(m["verdicts_for_decision"])),
		Previous:      anyString(m["previous_verdict"]),
	}, nil
}

// --- маленькие разборщики JSON: поля приходят то строкой, то числом ---

func anyString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprint(t)
	case bool:
		return fmt.Sprint(t)
	case nil:
		return ""
	default:
		return ""
	}
}

func anyInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case string:
		var n int64
		fmt.Sscan(t, &n)
		return n
	case nil:
		return 0
	default:
		return 0
	}
}

func anyBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// readAllLimited читает не больше limit байт: ответ слоя знаний — это текст,
// а не повод съесть всю память процесса, если сервис вдруг отдаст поток.
func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, limit))
}
