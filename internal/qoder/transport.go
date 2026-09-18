package qoder

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/J-York/QoderProxy/internal/credential"
	"github.com/J-York/QoderProxy/internal/protocol"
)

type ChatRequest struct {
	Raw          map[string]json.RawMessage
	Model        string
	Messages     []json.RawMessage
	Tools        json.RawMessage
	Stream       bool
	IncludeUsage bool
	SessionKey   string
}

func ParseChatRequest(body []byte) (ChatRequest, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return ChatRequest{}, fmt.Errorf("invalid JSON: %w", err)
	}
	var out ChatRequest
	out.Raw = raw
	if err := json.Unmarshal(raw["model"], &out.Model); err != nil || out.Model == "" {
		return ChatRequest{}, errors.New("model is required")
	}
	if err := json.Unmarshal(raw["messages"], &out.Messages); err != nil || len(out.Messages) == 0 {
		return ChatRequest{}, errors.New("messages must be a non-empty array")
	}
	// Modern OpenAI clients may send their highest-priority instruction with
	// role=developer. Qoder's Chat Completions transports currently understand
	// the older role=system spelling, so normalize it without changing message
	// order or content. Keeping this normalization at the request boundary makes
	// Bearer and COSY behave consistently.
	for i, message := range out.Messages {
		var fields map[string]json.RawMessage
		if json.Unmarshal(message, &fields) != nil {
			continue
		}
		var role string
		if json.Unmarshal(fields["role"], &role) == nil && role == "developer" {
			fields["role"] = json.RawMessage(`"system"`)
			normalized, err := json.Marshal(fields)
			if err != nil {
				return ChatRequest{}, fmt.Errorf("messages[%d] is invalid: %w", i, err)
			}
			out.Messages[i] = normalized
		}
	}
	normalizedMessages, err := json.Marshal(out.Messages)
	if err != nil {
		return ChatRequest{}, fmt.Errorf("messages are invalid: %w", err)
	}
	raw["messages"] = normalizedMessages
	if v := raw["stream"]; len(v) > 0 {
		_ = json.Unmarshal(v, &out.Stream)
	}
	out.Tools = raw["tools"]
	if v := raw["stream_options"]; len(v) > 0 {
		var opts struct {
			IncludeUsage bool `json:"include_usage"`
		}
		_ = json.Unmarshal(v, &opts)
		out.IncludeUsage = opts.IncludeUsage
	}
	return out, nil
}

func ValidateCapabilities(req ChatRequest, model Model) error {
	var hasImage bool
	for i, raw := range req.Messages {
		var msg struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
			ToolCalls  json.RawMessage `json:"tool_calls"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			return fmt.Errorf("messages[%d] is invalid: %w", i, err)
		}
		switch msg.Role {
		case "system", "user", "assistant", "tool":
		default:
			return fmt.Errorf("messages[%d].role %q is unsupported", i, msg.Role)
		}
		if msg.Role == "tool" && msg.ToolCallID == "" {
			return fmt.Errorf("messages[%d].tool_call_id is required for tool role", i)
		}
		if len(msg.Content) > 0 && string(msg.Content) != "null" {
			var s string
			if json.Unmarshal(msg.Content, &s) != nil {
				var parts []struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				}
				if err := json.Unmarshal(msg.Content, &parts); err != nil {
					return fmt.Errorf("messages[%d].content must be string, null, or multipart array", i)
				}
				for _, p := range parts {
					switch p.Type {
					case "text":
					case "image_url":
						if p.ImageURL.URL == "" {
							return fmt.Errorf("messages[%d] contains empty image_url", i)
						}
						hasImage = true
					default:
						return fmt.Errorf("messages[%d] contains unsupported content part %q", i, p.Type)
					}
				}
			}
		}
	}
	if hasImage && !model.IsVL {
		return fmt.Errorf("model %q does not advertise image support", model.ID)
	}
	var enableThinking *bool
	if raw := req.Raw["enable_thinking"]; len(raw) > 0 {
		var v bool
		if json.Unmarshal(raw, &v) != nil {
			return errors.New("enable_thinking must be boolean")
		}
		enableThinking = &v
	}
	var effort string
	if raw := req.Raw["reasoning_effort"]; len(raw) > 0 {
		if json.Unmarshal(raw, &effort) != nil {
			return errors.New("reasoning_effort must be string")
		}
	}
	if (enableThinking != nil && *enableThinking || effort != "" && effort != "none") && !model.IsReasoning {
		return fmt.Errorf("model %q does not advertise thinking support", model.ID)
	}
	if effort != "" && effort != "none" {
		var cfg struct {
			Enabled struct {
				Efforts map[string]json.RawMessage `json:"efforts"`
			} `json:"enabled"`
		}
		if len(model.Thinking) > 0 {
			b, _ := json.Marshal(model.Thinking)
			_ = json.Unmarshal(b, &cfg)
			if len(cfg.Enabled.Efforts) > 0 {
				if _, ok := cfg.Enabled.Efforts[effort]; !ok {
					return fmt.Errorf("model %q does not advertise reasoning_effort %q", model.ID, effort)
				}
			}
		}
	}
	return nil
}

type UpstreamResponse struct {
	Status int
	Header http.Header
	Body   io.ReadCloser
}

type ChatTransport interface {
	Name() string
	Probe(context.Context, credential.Account) error
	Chat(context.Context, credential.Account, ChatRequest, Model) (*UpstreamResponse, error)
}

type BearerTransport struct {
	HTTP      *http.Client
	Endpoints func(protocol.Region) (protocol.Endpoints, bool)
}

func (t *BearerTransport) endpoints(region protocol.Region) (protocol.Endpoints, bool) {
	if t.Endpoints != nil {
		return t.Endpoints(region)
	}
	return protocol.ForRegion(region)
}

func (t *BearerTransport) Name() string { return "bearer" }

func (t *BearerTransport) Probe(ctx context.Context, account credential.Account) error {
	ep, _ := t.endpoints(protocol.Region(account.Region))
	if ep.BearerChat == "" {
		return &HTTPError{Status: http.StatusNotFound, Message: "bearer endpoint is not verified for this region"}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodOptions, ep.BearerChat, nil)
	req.Header.Set("Authorization", "Bearer "+account.AccessToken)
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 405 for OPTIONS proves the route exists but disallows capability probing.
	if resp.StatusCode == http.StatusNotFound {
		return &HTTPError{Status: resp.StatusCode, Message: "bearer route not found"}
	}
	if resp.StatusCode >= 500 {
		return &HTTPError{Status: resp.StatusCode, Message: "bearer probe failed"}
	}
	return nil
}

func (t *BearerTransport) Chat(ctx context.Context, account credential.Account, req ChatRequest, model Model) (*UpstreamResponse, error) {
	ep, _ := t.endpoints(protocol.Region(account.Region))
	if ep.BearerChat == "" {
		return nil, &HTTPError{Status: http.StatusNotFound, Message: "bearer endpoint unavailable for region"}
	}
	allowed := []string{"messages", "tools", "tool_choice", "parallel_tool_calls", "stop", "temperature", "top_p", "max_tokens", "max_completion_tokens", "reasoning", "reasoning_effort", "enable_thinking", "stream_options"}
	body := map[string]json.RawMessage{}
	for _, k := range allowed {
		if v := req.Raw[k]; len(v) > 0 {
			body[k] = v
		}
	}
	body["model"], _ = json.Marshal(model.UpstreamKey)
	body["stream"] = json.RawMessage("true")
	body["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	rid, _ := randomUUID()
	sid, _ := randomUUID()
	if req.SessionKey != "" {
		sid = stableID("session", account.UserID, model.UpstreamKey, req.SessionKey)
	}
	metadata := map[string]any{"context": map[string]any{"request_id": rid, "request_set_id": rid, "session_id": sid, "task_id": "common", "client_type": "qodercli"}}
	body["metadata"], _ = json.Marshal(metadata)
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.BearerChat, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Authorization", "Bearer "+account.AccessToken)
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "text/event-stream")
	hreq.Header.Set("Accept-Encoding", "identity")
	hreq.Header.Set("User-Agent", protocol.BearerUserAgent)
	hreq.Header.Set("X-Request-ID", rid)
	hreq.Header.Set("X-Session-ID", sid)
	resp, err := t.HTTP.Do(hreq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &HTTPError{Status: resp.StatusCode, Message: SanitizeSnippet(preview, 256), RetryAfter: retryAfter(resp.Header)}
	}
	return &UpstreamResponse{Status: resp.StatusCode, Header: resp.Header, Body: resp.Body}, nil
}

type CosyTransport struct {
	HTTP      *http.Client
	Signer    *CosySigner
	Endpoints func(protocol.Region) (protocol.Endpoints, bool)
}

func (t *CosyTransport) endpoints(region protocol.Region) (protocol.Endpoints, bool) {
	if t.Endpoints != nil {
		return t.Endpoints(region)
	}
	return protocol.ForRegion(region)
}

func (t *CosyTransport) Name() string { return "cosy" }

func (t *CosyTransport) Probe(ctx context.Context, account credential.Account) error {
	ep, _ := t.endpoints(protocol.Region(account.Region))
	h, err := t.Signer.Headers(nil, ep.ModelList, account)
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ep.ModelList, nil)
	req.Header = h
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &HTTPError{Status: resp.StatusCode, Message: SanitizeSnippet(b, 256)}
	}
	return nil
}

func (t *CosyTransport) Chat(ctx context.Context, account credential.Account, req ChatRequest, model Model) (*UpstreamResponse, error) {
	ep, _ := t.endpoints(protocol.Region(account.Region))
	messages := make([]json.RawMessage, len(req.Messages))
	copy(messages, req.Messages)
	for i, raw := range messages {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		var role string
		_ = json.Unmarshal(m["role"], &role)
		if role == "assistant" && (string(m["content"]) == "null" || string(m["content"]) == `""`) && len(m["tool_calls"]) > 0 {
			m["content"] = json.RawMessage(`" "`)
			messages[i], _ = json.Marshal(m)
		}
	}
	lastUser := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if text, ok := messageText(messages[i], "user"); ok {
			lastUser = text
			break
		}
	}
	maxTokens := int64(32768)
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		var v int64
		if json.Unmarshal(req.Raw[key], &v) == nil && v > 0 && v < maxTokens {
			maxTokens = v
		}
	}
	parameters := map[string]any{"max_tokens": maxTokens}
	if raw := req.Raw["enable_thinking"]; len(raw) > 0 {
		var v bool
		_ = json.Unmarshal(raw, &v)
		parameters["enable_thinking"] = v
	}
	if raw := req.Raw["reasoning_effort"]; len(raw) > 0 {
		var v string
		_ = json.Unmarshal(raw, &v)
		parameters["reasoning_effort"] = v
		if _, ok := parameters["enable_thinking"]; !ok {
			parameters["enable_thinking"] = v != "none"
		}
	}
	rid, _ := randomUUID()
	sid := stableID("session", account.UserID, model.UpstreamKey, req.SessionKey)
	record := stableID("record", model.UpstreamKey, string(mustJSON(messages)), string(req.Tools), fmt.Sprint(maxTokens))
	var modelConfig any
	if json.Unmarshal(model.RawConfig, &modelConfig) != nil {
		return nil, errors.New("invalid cached model_config")
	}
	var tools any = []any{}
	if len(req.Tools) > 0 && string(req.Tools) != "null" {
		_ = json.Unmarshal(req.Tools, &tools)
	}
	body := map[string]any{
		"request_id": rid, "request_set_id": record, "chat_record_id": record, "session_id": sid, "stream": true,
		"chat_task": "FREE_INPUT", "is_reply": true, "is_retry": false, "source": 1, "version": "3", "session_type": "qodercli", "agent_id": "agent_common", "task_id": "common",
		"code_language": "", "chat_prompt": "", "image_urls": nil, "aliyun_user_type": "", "system": "", "messages": messages, "tools": tools, "parameters": parameters,
		"chat_context": map[string]any{"chatPrompt": "", "imageUrls": nil, "extra": map[string]any{"context": []any{}, "modelConfig": map[string]any{"key": model.UpstreamKey, "is_reasoning": model.IsReasoning}, "originalContent": lastUser}, "features": []any{}, "text": lastUser},
		"model_config": modelConfig, "business": map[string]any{"product": "cli", "version": "1.0.0", "type": "agent", "stage": "start", "id": rid, "name": trimText(lastUser, 30), "begin_at": time.Now().UnixMilli()},
	}
	plain, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	encoded := []byte(EncodeBody(plain))
	h, err := t.Signer.Headers(encoded, ep.CosyChat, account)
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.CosyChat, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	hreq.Header = h
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "text/event-stream")
	hreq.Header.Set("Cache-Control", "no-cache")
	hreq.Header.Set("Accept-Encoding", "identity")
	hreq.Header.Set("X-Model-Key", model.UpstreamKey)
	source := model.Source
	if source == "" {
		source = "system"
	}
	hreq.Header.Set("X-Model-Source", source)
	resp, err := t.HTTP.Do(hreq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &HTTPError{Status: resp.StatusCode, Message: SanitizeSnippet(preview, 256), RetryAfter: retryAfter(resp.Header)}
	}
	return &UpstreamResponse{Status: resp.StatusCode, Header: resp.Header, Body: resp.Body}, nil
}

type HTTPError struct {
	Status     int
	Message    string
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Qoder upstream HTTP %d: %s", e.Status, e.Message)
}

// quotaMarkerRe 匹配上游「模型級配額拒絕」的業務特徵：
// 實測 Free 層帳號請求付費模型時，上游以 HTTP 200 + 信封內嵌
// 403 + {"code":"112","message":"{\"pricingUrl\":...}"} 返回
// （見 FORK-PLAN.md §2.1）。帳號本身健康，僅該模型無額度。
var quotaMarkerRe = regexp.MustCompile(`"code"\s*:\s*"?112"?|"pricingUrl"`)

// IsQuotaError 判斷上游錯誤訊息是否為模型級配額拒絕，
// 而非帳號級拒絕（token 失效、無權限、地區封鎖）。
// 呼叫者據此決定是否停用帳號：配額錯誤不停用。
func IsQuotaError(message string) bool {
	return quotaMarkerRe.MatchString(message)
}

func retryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if n, err := time.ParseDuration(v + "s"); err == nil {
		return n
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}
func stableID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(p))
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:24]
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func messageText(raw json.RawMessage, wanted string) (string, bool) {
	var m struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil || m.Role != wanted {
		return "", false
	}
	if s, ok := m.Content.(string); ok {
		return s, true
	}
	return "", true
}
func trimText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
