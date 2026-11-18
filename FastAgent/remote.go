// remote.go — удалённая диагностика, этап R1 (см. REMOTE-DIAG.md).
//
// Инверсия направления: на гостевой ПК попадает ОДИН exe, он сам звонит на
// брокер (pi5), а оператор работает в своём терминале. Здесь — общая часть:
// типы, HMAC-подпись команд и брокер (реле), который не знает ключа подписи
// и поэтому не может подделать команду.
//
// Границы R1: только чтение — diag, read_file, grep_search, file_glob_search.
// write_file и run в R1 не выполняются: на госте любой другой kind отклоняется
// политикой (classifyTool), а не «обещанием» оператора.
package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	remoteCodeLen       = 6
	remotePollMaxWait   = 50 * time.Second
	remotePollDefault   = 25 * time.Second
	remoteMaxResultSize = 64 << 10 // 64 КБ на результат: этого хватает на вывод команд
	remoteEventsBuffer  = 32
)

// Сроки — переменные, а не константы: тесты подменяют их короткими.
var (
	remoteMaxLifetime = 2 * time.Hour
	remoteIdleTimeout = 15 * time.Minute
	remotePairWindow  = 15 * time.Minute
)

// RemoteConfig — блок "remote" в config.fast.json. Пусто = режим не настроен.
type RemoteConfig struct {
	BrokerURL string `json:"broker_url"` // куда звонит гость (https://pi5.tail823290.ts.net/fa)
	Secret    string `json:"secret"`     // секрет операторских ручек брокера
	TicketExe string `json:"ticket_exe"` // какой exe отдавать файлом-билетом
}

// remoteCmd — команда оператора гостю. Подпись считается ПОСЛЕ сериализации
// args: подписывается именно то, что уйдёт на гость, а не «примерно то».
type remoteCmd struct {
	ID   string          `json:"id"`
	Kind string          `json:"kind"`
	Args json.RawMessage `json:"args,omitempty"`
	Sig  string          `json:"sig"`
	TS   int64           `json:"ts"`
	// Approved — «оператор подтвердил» для R2: ставится в true только после
	// y/n в консоли оператора и входит в подпись. Гость без него изменение не
	// выполняет, поэтому сам брокер подтверждение подделать не может.
	Approved bool `json:"approved,omitempty"`
}

// remoteResult — ответ гостя. OK=false означает отказ политики или ошибку
// инструмента; Out в этом случае содержит причину.
type remoteResult struct {
	ID   string `json:"id"`
	OK   bool   `json:"ok"`
	Out  string `json:"out,omitempty"`
	Exit int    `json:"exit"`
	MS   int64  `json:"ms"`
	// NeedsApproval — R2: гость посчитал команду «изменяющей» и ждёт y/n
	// оператора; Verdict объясняет, что именно он подтверждает.
	NeedsApproval bool   `json:"needs_approval,omitempty"`
	Verdict       string `json:"verdict,omitempty"`
}

// remoteEvent — то, что брокер отдаёт оператору по SSE.
type remoteEvent struct {
	Type       string        `json:"type"` // paired|result|closed
	GuestToken string        `json:"guest_token,omitempty"`
	Result     *remoteResult `json:"result,omitempty"`
	Error      string        `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// Ключ и подпись
// ---------------------------------------------------------------------------

// remoteDeriveKey выводит сессионный ключ из кода сессии и guest-токена.
// Код знает только оператор (из имени файла), guest-токен выдаётся брокером
// при паре; брокер видит оба, поэтому его защита — не тайна ключа, а то, что
// он не умеет выдать себя за оператора перед гостем: команду, отправленную в
// обход подписи, гость отвергает.
func remoteDeriveKey(code, guestToken string) []byte {
	mac := hmac.New(sha256.New, []byte(code))
	mac.Write([]byte("fastagent-remote-v1"))
	mac.Write([]byte(guestToken))
	return mac.Sum(nil)
}

// remoteCanonical — то, что подписывается. Разделитель "|" безопасен: id —
// hex, kind — из фиксированного набора, ts — число, args — JSON.
func remoteCanonical(id, kind string, args json.RawMessage, ts int64, approved bool) string {
	return fmt.Sprintf("%s|%s|%s|%d|%t", id, kind, string(args), ts, approved)
}

func remoteSign(key []byte, id, kind string, args json.RawMessage, ts int64, approved bool) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(remoteCanonical(id, kind, args, ts, approved)))
	return hex.EncodeToString(mac.Sum(nil))
}

// remoteVerify — проверка подписи на госте. hmac.Equal, а не ==: сравнение
// побайтовое и без раннего выхода.
// remoteVerify — проверка подписи на госте. Approved входит в подпись: иначе
// реле могло бы «дописать» подтверждение к чужой команде.
func remoteVerify(key []byte, c remoteCmd) bool {
	want := remoteSign(key, c.ID, c.Kind, c.Args, c.TS, c.Approved)
	return hmac.Equal([]byte(want), []byte(c.Sig))
}

// randHex — n случайных байт в hex. crypto/rand, а не math/rand: токены и коды
// сессий подбирать не должны.
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Отказ криптогенератора не «лечим» тишиной: сессия без нормального
		// кода — это дыра, поэтому падаем громко.
		panic("crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// Брокер: сессии
// ---------------------------------------------------------------------------

type remoteSession struct {
	mu         sync.Mutex
	code       string
	opToken    string
	guestToken string
	paired     bool
	closed     bool
	created    time.Time
	lastSeen   time.Time
	attempts   int
	queue      []remoteCmd
	results    map[string]remoteResult
	events     chan remoteEvent
}

func (s *remoteSession) expired(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return true
	}
	if now.Sub(s.created) > remoteMaxLifetime {
		return true
	}
	return now.Sub(s.lastSeen) > remoteIdleTimeout
}

// pushEvent — не блокируется: SSE-читатель может отставать, а гость ждать не
// должен. Потерянное событие оператор увидит в /fa/status.
func (s *remoteSession) pushEvent(ev remoteEvent) {
	select {
	case s.events <- ev:
	default:
	}
}

func (s *remoteSession) takeCmd() (remoteCmd, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return remoteCmd{}, false
	}
	c := s.queue[0]
	s.queue = s.queue[1:]
	return c, true
}

type remoteBroker struct {
	mu       sync.Mutex
	sessions map[string]*remoteSession
	secret   string
	ticket   string
	now      func() time.Time
}

func newRemoteBroker(secret, ticket string) *remoteBroker {
	return &remoteBroker{
		sessions: make(map[string]*remoteSession),
		secret:   secret,
		ticket:   ticket,
		now:      time.Now,
	}
}

func (b *remoteBroker) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/fa/session", b.handleSession)
	mux.HandleFunc("/fa/status", b.handleStatus)
	mux.HandleFunc("/fa/pair", b.handlePair)
	mux.HandleFunc("/fa/poll", b.handlePoll)
	mux.HandleFunc("/fa/result", b.handleResult)
	mux.HandleFunc("/fa/cmd", b.handleCmd)
	mux.HandleFunc("/fa/events", b.handleEvents)
	mux.HandleFunc("/fa/close", b.handleClose)
	mux.HandleFunc("/fa/ticket/", b.handleTicket)
	return mux
}

// cleanup выкидывает просроченные сессии. Вызывается на каждом операторском
// запросе: отдельный фоновой сборщик не нужен, а так он не мешает тестам.
func (b *remoteBroker) cleanup() {
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	for code, s := range b.sessions {
		if s.expired(now) {
			delete(b.sessions, code)
		}
	}
}

func (b *remoteBroker) find(code string) *remoteSession {
	key := strings.ToLower(strings.TrimSpace(code))
	b.mu.Lock()
	s := b.sessions[key]
	b.mu.Unlock()
	if s != nil && s.expired(b.now()) {
		b.mu.Lock()
		delete(b.sessions, key)
		b.mu.Unlock()
		return nil
	}
	return s
}

func (b *remoteBroker) findGuest(token string) *remoteSession {
	if token == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.sessions {
		s.mu.Lock()
		ok := s.guestToken != "" && hmac.Equal([]byte(s.guestToken), []byte(token))
		s.mu.Unlock()
		if ok && !s.expired(b.now()) {
			return s
		}
	}
	return nil
}

// authOperator — операторские ручки. Секрет обязателен, если брокер слушает
// не loopback (проверяется при старте): публичный Funnel не должен позволять
// создавать сессии кому угодно.
func (b *remoteBroker) authOperator(r *http.Request) bool {
	if b.secret == "" {
		return isLoopback(r.RemoteAddr)
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if strings.TrimSpace(got) == b.secret {
		return true
	}
	return strings.TrimSpace(r.Header.Get("X-FA-Secret")) == b.secret
}

func bearerToken(r *http.Request) string {
	return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

func (b *remoteBroker) newCode() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for {
		code := randHex(remoteCodeLen / 2)[:remoteCodeLen]
		if _, taken := b.sessions[code]; !taken {
			return code
		}
	}
}

// ---------------------------------------------------------------------------
// Брокер: ручки
// ---------------------------------------------------------------------------

// handleSession создаёт сессию и возвращает одноразовый op-токен.
func (b *remoteBroker) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "только POST"})
		return
	}
	if !b.authOperator(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "нужен секрет брокера"})
		return
	}
	b.cleanup()
	now := b.now()
	code := b.newCode()
	s := &remoteSession{
		code:     code,
		opToken:  randHex(16),
		created:  now,
		lastSeen: now,
		results:  make(map[string]remoteResult),
		events:   make(chan remoteEvent, remoteEventsBuffer),
	}
	b.mu.Lock()
	b.sessions[code] = s
	b.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"code": code, "op_token": s.opToken})
}

// handleStatus — запасной путь для оператора: SSE мог отстать или порваться.
func (b *remoteBroker) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !b.authOperator(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "нужен секрет брокера"})
		return
	}
	s := b.find(r.URL.Query().Get("code"))
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "сессия не найдена"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"code":    s.code,
		"paired":  s.paired,
		"closed":  s.closed,
		"pending": len(s.queue),
	})
}

// handlePair — гость называет код и получает guest-токен. Окно 15 минут,
// повторная пара запрещена (одна сессия — одна пара), промахи считаются.
func (b *remoteBroker) handlePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "только POST"})
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &req, 1<<12); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ожидался JSON {\"code\":\"…\"}"})
		return
	}
	s := b.find(req.Code)
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "сессия не найдена или истекла"})
		return
	}
	now := b.now()
	s.mu.Lock()
	if s.paired || s.closed || now.Sub(s.created) > remotePairWindow {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "сессия недоступна для пары"})
		return
	}
	s.guestToken = randHex(16)
	s.paired = true
	s.attempts = 0
	s.lastSeen = now
	token := s.guestToken
	s.mu.Unlock()
	s.pushEvent(remoteEvent{Type: "paired", GuestToken: token})
	writeJSON(w, http.StatusOK, map[string]string{"guest_token": token})
}

// handlePoll — длинный поллинг. Пока команд нет, держим до wait секунд;
// 204 — это нормальный ответ «пока ничего», а не ошибка.
func (b *remoteBroker) handlePoll(w http.ResponseWriter, r *http.Request) {
	s := b.findGuest(bearerToken(r))
	if s == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "нужен guest-токен"})
		return
	}
	wait := remotePollDefault
	if v := r.URL.Query().Get("wait"); v != "" {
		var sec int
		if _, err := fmt.Sscanf(v, "%d", &sec); err == nil && sec >= 0 {
			wait = time.Duration(sec) * time.Second
		}
	}
	if wait > remotePollMaxWait {
		wait = remotePollMaxWait
	}
	deadline := b.now().Add(wait)
	for {
		if c, ok := s.takeCmd(); ok {
			s.mu.Lock()
			s.lastSeen = b.now()
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, c)
			return
		}
		s.mu.Lock()
		closed := s.closed
		s.lastSeen = b.now()
		s.mu.Unlock()
		if closed {
			writeJSON(w, http.StatusGone, map[string]string{"error": "сессия закрыта"})
			return
		}
		if !b.now().Before(deadline) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// handleResult — гость присылает итог. Размер ограничен, чтобы чужой exe не
// забил память брокера гигабайтным выводом.
func (b *remoteBroker) handleResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "только POST"})
		return
	}
	s := b.findGuest(bearerToken(r))
	if s == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "нужен guest-токен"})
		return
	}
	var res remoteResult
	if err := decodeJSON(r, &res, remoteMaxResultSize*2); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "битый JSON результата"})
		return
	}
	res.Out = truncate(res.Out, remoteMaxResultSize)
	s.mu.Lock()
	s.results[res.ID] = res
	s.lastSeen = b.now()
	s.mu.Unlock()
	s.pushEvent(remoteEvent{Type: "result", Result: &res})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleCmd — оператор ставит команду. Брокер её НЕ подписывает и не разбирает
// (кроме проверки обязательных полей): подпись сделана на стороне оператора.
func (b *remoteBroker) handleCmd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "только POST"})
		return
	}
	if !b.authOperator(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "нужен секрет брокера"})
		return
	}
	var req struct {
		Code string    `json:"code"`
		Cmd  remoteCmd `json:"cmd"`
	}
	if err := decodeJSON(r, &req, 1<<20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ожидался JSON {\"code\",\"cmd\"}"})
		return
	}
	s := b.find(req.Code)
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "сессия не найдена"})
		return
	}
	if req.Cmd.ID == "" || req.Cmd.Kind == "" || req.Cmd.Sig == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "нужны id, kind и sig"})
		return
	}
	s.mu.Lock()
	if !s.paired {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "гость ещё не подключился"})
		return
	}
	if s.closed {
		s.mu.Unlock()
		writeJSON(w, http.StatusGone, map[string]string{"error": "сессия закрыта"})
		return
	}
	s.queue = append(s.queue, req.Cmd)
	s.lastSeen = b.now()
	s.mu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

// handleEvents — SSE для консоли оператора.
func (b *remoteBroker) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !b.authOperator(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "нужен секрет брокера"})
		return
	}
	s := b.find(r.URL.Query().Get("code"))
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "сессия не найдена"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "SSE не поддерживается"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case ev := <-s.events:
			raw, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", raw)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// handleClose — закрыть сессию может любая сторона.
func (b *remoteBroker) handleClose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "только POST"})
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	_ = decodeJSON(r, &req, 1<<12)
	s := b.find(req.Code)
	if s == nil {
		s = b.findGuest(bearerToken(r))
	}
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "сессия не найдена"})
		return
	}
	// Закрывать вправе оператор (по секрету) или гость (по своему токену).
	guest := b.findGuest(bearerToken(r)) == s
	if !guest && !b.authOperator(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "нет прав на закрытие"})
		return
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.pushEvent(remoteEvent{Type: "closed"})
	writeJSON(w, http.StatusOK, map[string]string{"status": "closed"})
}

// handleTicket — файл-билет: exe с кодом в имени. Отдаём только для живой
// сессии: иначе это «раздача бинаря всем желающим» (открытый вопрос §10).
func (b *remoteBroker) handleTicket(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/fa/ticket/")
	code := strings.TrimSuffix(rest, ".exe")
	if code == rest || b.find(code) == nil {
		http.NotFound(w, r)
		return
	}
	if b.ticket == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "на брокере не настроен ticket_exe"})
		return
	}
	f, err := os.Open(b.ticket)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "ticket_exe не читается"})
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "fastagent-"+code+".exe"))
	http.ServeContent(w, r, "fastagent-"+code+".exe", time.Time{}, f)
}

// runBroker — запуск брокера. Публикация наружу (Tailscale Funnel) требует
// секрета: иначе любой в интернете создавал бы сессии.
func runBroker(addr, secret, ticket string) int {
	host, _, err := splitHostPort(addr)
	if err != nil {
		fmt.Printf("[ERROR] адрес брокера: %v\n", err)
		return 2
	}
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if secret == "" && !loopback {
		fmt.Println("[ERROR] брокер слушает не localhost, а секрет не задан — отказ.")
		fmt.Println("        Задайте -broker-secret: иначе создавать сессии сможет любой.")
		return 2
	}
	if ticket == "" {
		fmt.Println("[!] ticket_exe не задан: файл-билет отдаваться не будет (сессию всё равно можно создать).")
	}
	b := newRemoteBroker(secret, ticket)
	srv := &http.Server{
		Addr:              addr,
		Handler:           b.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Println("===================================================")
	fmt.Println("        FASTAGENT — брокер удалённой диагностики (R1)")
	fmt.Println("===================================================")
	fmt.Printf("Слушаю: http://%s/fa/*\n", addr)
	if secret == "" {
		fmt.Println("[i] Секрет не задан: операторские ручки — только с localhost.")
	} else {
		fmt.Println("[i] Оператор: Authorization: Bearer <broker-secret>.")
	}
	fmt.Println("[i] Гость: -link, только чтение (diag/read_file/grep_search/file_glob_search).")
	if err := srv.ListenAndServe(); err != nil {
		fmt.Printf("[ERROR] брокер: %v\n", err)
		return 1
	}
	return 0
}

// splitHostPort — маленькая обёртка, чтобы не тянуть net в этот файл ради
// одной строки и не конфликтовать с serve.go.
func splitHostPort(addr string) (host, port string, err error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", "", fmt.Errorf("ожидался host:port, получено %q", addr)
	}
	return addr[:i], addr[i+1:], nil
}
