package stream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const maxSSEEventBytes = 16 << 20

type lineResult struct {
	line []byte
	err  error
}

func ParseSSE(ctx context.Context, body io.ReadCloser, idle time.Duration, emit func(Event) error) error {
	defer body.Close()
	r := bufio.NewReaderSize(body, 64<<10)
	var incomplete bytes.Buffer
	var incompleteErr error
	done := false
	consume := func(payload []byte) error {
		if len(bytes.TrimSpace(payload)) == 0 {
			return nil
		}
		if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
			if incomplete.Len() > 0 {
				return fmt.Errorf("upstream sent [DONE] with incomplete SSE JSON: %w", incompleteErr)
			}
			done = true
			return emit(Event{Kind: "done"})
		}
		if incomplete.Len() > 0 {
			if incomplete.Len()+len(payload) > maxSSEEventBytes {
				return errors.New("incomplete SSE JSON exceeds 16 MiB limit")
			}
			incomplete.Write(payload)
			payload = bytes.Clone(incomplete.Bytes())
		} else if len(payload) > maxSSEEventBytes {
			return errors.New("SSE event exceeds 16 MiB limit")
		}
		events, isDone, err := parsePayload(payload)
		if err != nil {
			// Some Qoder gateways have been observed ending an SSE frame while
			// the outer JSON object is still incomplete, then continuing it in
			// the next data frame. Recover only from the unambiguous truncated-
			// JSON error. All other malformed payloads still fail immediately.
			if isIncompleteJSON(err) {
				if incomplete.Len() == 0 {
					incomplete.Write(payload)
				}
				incompleteErr = err
				return nil
			}
			return err
		}
		incomplete.Reset()
		incompleteErr = nil
		for _, ev := range events {
			if err := emit(ev); err != nil {
				return err
			}
		}
		if isDone {
			done = true
			return emit(Event{Kind: "done"})
		}
		return nil
	}

	for !done {
		ch := make(chan lineResult, 1)
		go func() {
			line, err := r.ReadBytes('\n')
			ch <- lineResult{line: line, err: err}
		}()
		var timer <-chan time.Time
		var t *time.Timer
		if idle > 0 {
			t = time.NewTimer(idle)
			timer = t.C
		}
		var lr lineResult
		select {
		case <-ctx.Done():
			_ = body.Close()
			if t != nil {
				t.Stop()
			}
			return ctx.Err()
		case <-timer:
			_ = body.Close()
			return errors.New("upstream stream idle timeout")
		case lr = <-ch:
			if t != nil {
				t.Stop()
			}
		}
		line := bytes.TrimRight(lr.line, "\r\n")
		if bytes.HasPrefix(line, []byte("data:")) {
			part := bytes.TrimPrefix(line, []byte("data:"))
			// SSE permits one optional space after the colon. Do not TrimSpace:
			// a broken gateway may split JSON inside a string, where a trailing
			// space is model output and must be preserved during reassembly.
			part = bytes.TrimPrefix(part, []byte(" "))
			if err := consume(part); err != nil {
				return err
			}
			if done {
				return nil
			}
		} else if incomplete.Len() > 0 && len(line) > 0 && !isSSEControlLine(line) {
			// Although SSE requires every payload line to use the data: field,
			// some Qoder model routes have emitted pretty-printed JSON where
			// only the first physical line has that prefix. Accept an ordinary
			// continuation only while a JSON value is known to be incomplete.
			// This does not make unrelated malformed events disappear: the
			// combined value must become valid JSON or parsing still fails.
			if err := consume(line); err != nil {
				return err
			}
		}
		if lr.err != nil {
			if errors.Is(lr.err, io.EOF) {
				if incomplete.Len() > 0 {
					return fmt.Errorf("upstream stream ended with incomplete SSE JSON: %w", incompleteErr)
				}
				if done {
					return nil
				}
				return errors.New("upstream stream ended without [DONE]")
			}
			return lr.err
		}
	}
	return nil
}

func isSSEControlLine(line []byte) bool {
	return bytes.HasPrefix(line, []byte(":")) ||
		bytes.HasPrefix(line, []byte("event:")) ||
		bytes.HasPrefix(line, []byte("id:")) ||
		bytes.HasPrefix(line, []byte("retry:"))
}

func isIncompleteJSON(err error) bool {
	var syntaxErr *json.SyntaxError
	return errors.As(err, &syntaxErr) && syntaxErr.Error() == "unexpected end of JSON input"
}

func parsePayload(payload []byte) ([]Event, bool, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(payload, &root); err != nil {
		return nil, false, fmt.Errorf("malformed SSE JSON: %w", err)
	}
	if rawStatus, ok := root["statusCodeValue"]; ok {
		var status int
		if err := json.Unmarshal(rawStatus, &status); err != nil {
			return nil, false, errors.New("envelope has invalid statusCodeValue")
		}
		var inner string
		if err := json.Unmarshal(root["body"], &inner); err != nil {
			return nil, false, errors.New("envelope has invalid body")
		}
		if status != 200 {
			return nil, false, fmt.Errorf("envelope upstream status %d: %s", status, truncate(inner, 256))
		}
		if strings.TrimSpace(inner) == "[DONE]" {
			return nil, true, nil
		}
		if inner == "" {
			return nil, false, nil
		}
		payload = []byte(inner)
	}
	var chunk struct {
		ID      string          `json:"id"`
		Object  string          `json:"object"`
		Created json.RawMessage `json:"created"`
		Model   string          `json:"model"`
		Choices []struct {
			Index int `json:"index"`
			Delta struct {
				Role             string          `json:"role"`
				Content          any             `json:"content"`
				ReasoningContent any             `json:"reasoning_content"`
				ToolCalls        []ToolCallDelta `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage    *Usage `json:"usage"`
		RawUsage *struct {
			PromptTokens       int64           `json:"prompt_tokens"`
			CompletionTokens   int64           `json:"completion_tokens"`
			TotalTokens        int64           `json:"total_tokens"`
			InputTokens        int64           `json:"input_tokens"`
			OutputTokens       int64           `json:"output_tokens"`
			CachedTokens       int64           `json:"cached_tokens"`
			CacheWriteTokens   int64           `json:"cache_write_tokens"`
			PromptTokenDetails UsageDetails    `json:"prompt_tokens_details"`
			InputTokenDetails  UsageDetails    `json:"input_tokens_details"`
			CompletionDetails  json.RawMessage `json:"completion_tokens_details"`
		} `json:"raw_usage"`
	}
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return nil, false, fmt.Errorf("malformed inner SSE JSON: %w", err)
	}
	created := parseInt64(chunk.Created)
	var out []Event
	for _, choice := range chunk.Choices {
		content, err := stringDelta(choice.Delta.Content)
		if err != nil {
			return nil, false, fmt.Errorf("invalid content delta: %w", err)
		}
		reasoning, err := stringDelta(choice.Delta.ReasoningContent)
		if err != nil {
			return nil, false, fmt.Errorf("invalid reasoning delta: %w", err)
		}
		out = append(out, Event{Kind: "delta", ID: chunk.ID, Created: created, Model: chunk.Model, Role: choice.Delta.Role, Content: content, Reasoning: reasoning, ToolCalls: choice.Delta.ToolCalls, FinishReason: choice.FinishReason})
	}
	usage := chunk.Usage
	if usage == nil && chunk.RawUsage != nil {
		r := chunk.RawUsage
		p, c := r.PromptTokens, r.CompletionTokens
		if p == 0 {
			p = r.InputTokens
		}
		if c == 0 {
			c = r.OutputTokens
		}
		total := r.TotalTokens
		if total == 0 {
			total = p + c
		}
		cached := r.CachedTokens
		if cached == 0 {
			cached = r.PromptTokenDetails.CachedTokens
		}
		if cached == 0 {
			cached = r.InputTokenDetails.CachedTokens
		}
		cacheWrite := r.CacheWriteTokens
		if cacheWrite == 0 {
			cacheWrite = r.PromptTokenDetails.CacheWriteTokens
		}
		if cacheWrite == 0 {
			cacheWrite = r.InputTokenDetails.CacheWriteTokens
		}
		usage = &Usage{PromptTokens: p, CompletionTokens: c, TotalTokens: total, PromptTokensDetails: UsageDetails{CachedTokens: cached, CacheWriteTokens: cacheWrite}, CompletionTokensDetails: r.CompletionDetails}
	}
	if usage != nil {
		out = append(out, Event{Kind: "usage", ID: chunk.ID, Created: created, Model: chunk.Model, Usage: usage})
	}
	if len(out) == 0 {
		return nil, false, errors.New("SSE JSON contained neither choices nor usage")
	}
	return out, false, nil
}

func stringDelta(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	if s, ok := v.(string); ok {
		return s, nil
	}
	return "", errors.New("expected string or null")
}

func parseInt64(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return int64(f)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		n, _ = strconv.ParseInt(s, 10, 64)
	}
	return n
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
