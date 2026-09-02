package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/J-York/QoderProxy/internal/config"
	"github.com/J-York/QoderProxy/internal/credential"
	"github.com/J-York/QoderProxy/internal/pool"
	"github.com/J-York/QoderProxy/internal/qoder"
	qstream "github.com/J-York/QoderProxy/internal/stream"
)

type Server struct {
	Config  config.Config
	Pool    *pool.Pool
	Catalog *qoder.Catalog
	Bearer  qoder.ChatTransport
	Cosy    qoder.ChatTransport
	APIKey  string
	Logger  *log.Logger

	readyMu    sync.RWMutex
	ready      bool
	degraded   bool
	readyError string

	transportMu    sync.RWMutex
	transportCache map[string]string
}

func New(cfg config.Config, p *pool.Pool, catalog *qoder.Catalog, bearer, cosy qoder.ChatTransport, apiKey string, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.Default()
	}
	return &Server{Config: cfg, Pool: p, Catalog: catalog, Bearer: bearer, Cosy: cosy, APIKey: apiKey, Logger: logger, transportCache: make(map[string]string)}
}

func (s *Server) Prime(ctx context.Context) {
	entry, err := s.Pool.Select("", nil)
	if err != nil {
		s.setReady(false, false, err)
		return
	}
	account, err := s.Pool.EnsureFresh(ctx, entry, false)
	if err != nil {
		s.setReady(false, false, err)
		return
	}
	snap, fetchErr := s.Catalog.Get(ctx, account)
	if len(snap.Models) == 0 {
		s.setReady(false, false, fetchErr)
		return
	}
	_, transportErr := s.pickTransport(ctx, entry, account, "")
	if transportErr != nil {
		s.setReady(false, snap.Degraded, transportErr)
		return
	}
	s.setReady(true, snap.Degraded, fetchErr)
}

func (s *Server) setReady(ready, degraded bool, err error) {
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	s.ready = ready
	s.degraded = degraded
	if err != nil {
		s.readyError = qoder.SanitizeSnippet([]byte(err.Error()), 256)
	} else {
		s.readyError = ""
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.applyCORS(w, r) {
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		s.health(w)
	case r.Method == http.MethodGet && r.URL.Path == "/readyz":
		s.readiness(w)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		if s.authorize(w, r) {
			s.models(w, r)
		}
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		if s.authorize(w, r) {
			s.chat(w, r)
		}
	default:
		writeError(w, http.StatusNotFound, "not_found", "route not found")
	}
}

func (s *Server) health(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}
func (s *Server) readiness(w http.ResponseWriter) {
	s.readyMu.RLock()
	defer s.readyMu.RUnlock()
	ready := s.ready && s.Pool.Available() > 0
	code := http.StatusOK
	if !ready {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{"ready": ready, "degraded": s.degraded, "available_accounts": s.Pool.Available(), "error": s.readyError})
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	if s.APIKey == "" {
		return true
	}
	got := r.Header.Get("X-API-Key")
	if got == "" && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		got = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	wantHash, gotHash := sha256.Sum256([]byte(s.APIKey)), sha256.Sum256([]byte(got))
	if subtle.ConstantTimeCompare(wantHash[:], gotHash[:]) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid proxy API key")
		return false
	}
	return true
}

func (s *Server) applyCORS(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	allowed := false
	for _, v := range s.Config.CORSOrigins {
		if v == origin {
			allowed = true
			break
		}
	}
	if !allowed {
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-API-Key, X-Session-Key")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	entry, err := s.Pool.Select("", nil)
	if err != nil {
		writeError(w, 503, "no_account", err.Error())
		return
	}
	account, err := s.Pool.EnsureFresh(r.Context(), entry, false)
	if err != nil {
		writeError(w, 502, "auth_error", err.Error())
		return
	}
	snap, err := s.Catalog.Get(r.Context(), account)
	if len(snap.Models) == 0 {
		writeError(w, 502, "catalog_error", err.Error())
		return
	}
	data := make([]map[string]any, 0, len(snap.Models))
	now := snap.FetchedAt.Unix()
	for _, m := range snap.Models {
		data = append(data, map[string]any{"id": m.ID, "object": "model", "created": now, "owned_by": "qoder", "display_name": m.DisplayName, "context_window": m.ContextWindow, "is_vl": m.IsVL, "is_reasoning": m.IsReasoning})
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data, "degraded": snap.Degraded})
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requestID := fmt.Sprintf("req-%d", start.UnixNano())
	status := 500
	modelName := ""
	accountLog := "none"
	defer func() {
		s.Logger.Printf("request_id=%s model=%s account=%s latency_ms=%d status=%d", requestID, modelName, accountLog, time.Since(start).Milliseconds(), status)
	}()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.Config.MaxRequestBytes))
	if err != nil {
		status = 413
		writeError(w, status, "request_too_large", "request body exceeds configured limit")
		return
	}
	chatReq, err := qoder.ParseChatRequest(body)
	if err != nil {
		status = 400
		writeError(w, status, "invalid_request", err.Error())
		return
	}
	modelName = chatReq.Model
	chatReq.SessionKey = r.Header.Get(s.Config.SessionHeader)
	ctx, cancel := context.WithTimeout(r.Context(), s.Config.TotalRequestTimeout.Duration)
	defer cancel()
	excluded := map[string]bool{}
	accountAttempts := 0
	maxAccounts := s.Pool.Count()
	retries5xx := 0
	for accountAttempts < maxAccounts {
		entry, selErr := s.Pool.Select(chatReq.SessionKey, excluded)
		if selErr != nil {
			status = 503
			writeError(w, status, "no_account", selErr.Error())
			return
		}
		account, refreshErr := s.Pool.EnsureFresh(ctx, entry, false)
		excluded[account.ID] = true
		accountAttempts++
		accountLog = account.AnonymousID()
		if refreshErr != nil {
			s.Pool.MarkAuthError(entry)
			continue
		}
		snap, catErr := s.Catalog.Get(ctx, account)
		if catErr != nil && len(snap.Models) == 0 {
			continue
		}
		model, ok := findModel(snap.Models, chatReq.Model)
		if !ok {
			status = 400
			writeError(w, status, "model_not_found", "model is not available for the selected Qoder account")
			return
		}
		if err := qoder.ValidateCapabilities(chatReq, model); err != nil {
			status = 400
			writeError(w, status, "unsupported_capability", err.Error())
			return
		}
		transport, pickErr := s.pickTransport(ctx, entry, account, model.UpstreamKey)
		if pickErr != nil {
			continue
		}
		up, callErr := transport.Chat(ctx, account, chatReq, model)
		if he := asHTTPError(callErr); he != nil && he.Status == 401 {
			fresh, refErr := s.Pool.EnsureFresh(ctx, entry, true)
			if refErr == nil {
				account = fresh
				up, callErr = transport.Chat(ctx, account, chatReq, model)
			}
			if asHTTPError(callErr) != nil && asHTTPError(callErr).Status == 401 {
				s.Pool.MarkAuthError(entry)
			}
		}
		if he := asHTTPError(callErr); he != nil && (he.Status == 404 || he.Status == 405) && s.Config.Transport == "auto" && transport.Name() == "bearer" {
			entry.SetTransport("cosy")
			class := "tier"
			if !isRouterTier(model.UpstreamKey) {
				class = "concrete:" + model.UpstreamKey
			}
			s.transportMu.Lock()
			s.transportCache[account.ID+"|"+class] = "cosy"
			s.transportMu.Unlock()
			transport = s.Cosy
			up, callErr = transport.Chat(ctx, account, chatReq, model)
		}
		if callErr != nil {
			he := asHTTPError(callErr)
			if he == nil {
				status = 502
				writeError(w, status, "upstream_network_error", qoder.SanitizeSnippet([]byte(callErr.Error()), 256))
				return
			}
			switch he.Status {
			case 401:
				s.Pool.MarkAuthError(entry)
			case 403:
				s.Pool.MarkDisabled(entry, "Qoder returned 403")
			case 429:
				s.Pool.MarkCooldown(entry, maxDuration(he.RetryAfter, s.Config.CooldownDefault.Duration))
			default:
				if he.Status >= 500 && retries5xx < s.Config.Max5xxRetries {
					retries5xx++
					time.Sleep(backoff(retries5xx))
					continue
				}
				status = he.Status
				if status < 400 || status > 599 {
					status = 502
				}
				writeError(w, status, "upstream_error", he.Error())
				return
			}
			continue
		}
		status = s.relay(w, ctx, up, chatReq, model)
		return
	}
	status = 503
	writeError(w, status, "accounts_exhausted", "all available Qoder accounts failed before streaming began")
}

func (s *Server) pickTransport(ctx context.Context, entry *pool.Entry, account credential.Account, modelKey string) (qoder.ChatTransport, error) {
	forced := s.Config.Transport
	class := "tier"
	if modelKey != "" && !isRouterTier(modelKey) {
		class = "concrete:" + modelKey
	}
	cacheKey := account.ID + "|" + class
	s.transportMu.RLock()
	cached := s.transportCache[cacheKey]
	s.transportMu.RUnlock()
	cache := func(name string) {
		s.transportMu.Lock()
		s.transportCache[cacheKey] = name
		s.transportMu.Unlock()
		entry.SetTransport(name)
	}
	if forced == "bearer" {
		if cached == "bearer" {
			return s.Bearer, nil
		}
		if err := s.Bearer.Probe(ctx, account); err != nil {
			return nil, err
		}
		cache("bearer")
		return s.Bearer, nil
	}
	if forced == "cosy" {
		if cached == "cosy" {
			return s.Cosy, nil
		}
		if err := s.Cosy.Probe(ctx, account); err != nil {
			return nil, err
		}
		cache("cosy")
		return s.Cosy, nil
	}
	if cached == "bearer" {
		return s.Bearer, nil
	}
	if cached == "cosy" {
		return s.Cosy, nil
	}
	// Live evidence from the current Qoder implementations shows that the
	// Bearer endpoint reliably accepts routing tiers, while concrete model keys
	// require the API3 request shape with the full model_config. Select COSY
	// before sending any inference request; never discover this by replaying a
	// failed generation across transports.
	if class != "tier" {
		if err := s.Cosy.Probe(ctx, account); err != nil {
			return nil, err
		}
		cache("cosy")
		return s.Cosy, nil
	}
	if err := s.Bearer.Probe(ctx, account); err == nil {
		cache("bearer")
		return s.Bearer, nil
	} else if he := asHTTPError(err); he == nil || he.Status != 404 {
		return nil, err
	}
	if err := s.Cosy.Probe(ctx, account); err != nil {
		return nil, err
	}
	cache("cosy")
	return s.Cosy, nil
}

func isRouterTier(model string) bool {
	switch model {
	case "auto", "ultimate", "performance", "efficient", "lite":
		return true
	default:
		return false
	}
}

func (s *Server) relay(w http.ResponseWriter, ctx context.Context, up *qoder.UpstreamResponse, req qoder.ChatRequest, model qoder.Model) int {
	router := &qstream.ThinkingRouter{}
	if req.Stream {
		flusher, ok := w.(http.Flusher)
		if !ok {
			up.Body.Close()
			writeError(w, 500, "streaming_unsupported", "HTTP writer does not support streaming")
			return 500
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(200)
		writer := &qstream.SSEWriter{W: w, Flusher: flusher, IncludeUsage: req.IncludeUsage}
		saw := false
		err := qstream.ParseSSE(ctx, up.Body, s.Config.StreamIdleTimeout.Duration, func(ev qstream.Event) error {
			if ev.Kind == "done" {
				for _, p := range router.Finalize() {
					x := ev
					x.Kind = "delta"
					if p.Reasoning {
						x.Reasoning = p.Text
					} else {
						x.Content = p.Text
					}
					if err := writer.WriteEvent(x, model.ID); err != nil {
						return err
					}
				}
				return writer.WriteEvent(ev, model.ID)
			}
			for _, routed := range router.Route(ev) {
				if routed.Kind == "delta" && (routed.Content != "" || routed.Reasoning != "" || routed.Role != "" || len(routed.ToolCalls) > 0) {
					saw = true
				}
				if err := writer.WriteEvent(routed, model.ID); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			msg := qoder.SanitizeSnippet([]byte(err.Error()), 256)
			b, _ := json.Marshal(map[string]any{"error": map[string]any{"type": "upstream_stream_error", "message": msg, "partial": saw}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			if !writer.Done {
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}
			flusher.Flush()
			return 502
		}
		return 200
	}
	acc := qstream.NewAccumulator()
	err := qstream.ParseSSE(ctx, up.Body, s.Config.StreamIdleTimeout.Duration, func(ev qstream.Event) error {
		if ev.Kind == "done" {
			for _, p := range router.Finalize() {
				x := ev
				x.Kind = "delta"
				if p.Reasoning {
					x.Reasoning = p.Text
				} else {
					x.Content = p.Text
				}
				acc.Add(x)
			}
			return nil
		}
		for _, x := range router.Route(ev) {
			acc.Add(x)
		}
		return nil
	})
	if err != nil {
		writeError(w, 502, "upstream_stream_error", qoder.SanitizeSnippet([]byte(err.Error()), 256))
		return 502
	}
	writeJSON(w, 200, acc.Response(model.ID))
	return 200
}

func findModel(models []qoder.Model, id string) (qoder.Model, bool) {
	for _, m := range models {
		if m.ID == id {
			return m, true
		}
	}
	return qoder.Model{}, false
}
func asHTTPError(err error) *qoder.HTTPError {
	var he *qoder.HTTPError
	if errors.As(err, &he) {
		return he
	}
	return nil
}
func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
func backoff(n int) time.Duration {
	return time.Duration(100*(1<<min(n, 4))+rand.Intn(100)) * time.Millisecond
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, typ, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"type": typ, "message": qoder.SanitizeSnippet([]byte(msg), 512)}})
}
