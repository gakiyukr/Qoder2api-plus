package stream

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

type accumulatorTool struct {
	ID        string
	Type      string
	Name      string
	Arguments string
}

type Accumulator struct {
	ID, Model, Role, Content, Reasoning string
	Created                             int64
	Tools                               map[int]*accumulatorTool
	Usage                               *Usage
	FinishReason                        *string
}

func NewAccumulator() *Accumulator { return &Accumulator{Tools: make(map[int]*accumulatorTool)} }

func (a *Accumulator) Add(ev Event) {
	if ev.ID != "" {
		a.ID = ev.ID
	}
	if ev.Model != "" {
		a.Model = ev.Model
	}
	if ev.Created != 0 {
		a.Created = ev.Created
	}
	if ev.Role != "" {
		a.Role = ev.Role
	}
	a.Content += ev.Content
	a.Reasoning += ev.Reasoning
	for _, tc := range ev.ToolCalls {
		t := a.Tools[tc.Index]
		if t == nil {
			t = &accumulatorTool{}
			a.Tools[tc.Index] = t
		}
		if tc.ID != "" {
			t.ID = tc.ID
		}
		if tc.Type != "" {
			t.Type = tc.Type
		}
		if tc.Function.Name != "" {
			t.Name = tc.Function.Name
		}
		t.Arguments += tc.Function.Arguments
	}
	if ev.Usage != nil {
		a.Usage = ev.Usage
	}
	if ev.FinishReason != nil {
		a.FinishReason = ev.FinishReason
	}
}

func (a *Accumulator) Response(fallbackModel string) map[string]any {
	if a.ID == "" {
		a.ID = "chatcmpl-qoder-proxy"
	}
	if a.Model == "" {
		a.Model = fallbackModel
	}
	if a.Created == 0 {
		a.Created = time.Now().Unix()
	}
	msg := map[string]any{"role": "assistant", "content": a.Content}
	if a.Content == "" && len(a.Tools) > 0 {
		msg["content"] = nil
	}
	if a.Reasoning != "" {
		msg["reasoning_content"] = a.Reasoning
	}
	if len(a.Tools) > 0 {
		calls := make([]map[string]any, 0, len(a.Tools))
		for i := 0; i < len(a.Tools); i++ {
			t := a.Tools[i]
			if t == nil {
				continue
			}
			typeName := t.Type
			if typeName == "" {
				typeName = "function"
			}
			args := t.Arguments
			if args == "" {
				args = "{}"
			}
			calls = append(calls, map[string]any{"id": t.ID, "type": typeName, "function": map[string]any{"name": t.Name, "arguments": args}})
		}
		msg["tool_calls"] = calls
	}
	finish := any(nil)
	if a.FinishReason != nil {
		finish = *a.FinishReason
	}
	resp := map[string]any{"id": a.ID, "object": "chat.completion", "created": a.Created, "model": a.Model, "choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}}}
	if a.Usage != nil {
		resp["usage"] = a.Usage
	}
	return resp
}

type SSEWriter struct {
	W            io.Writer
	Flusher      interface{ Flush() }
	IncludeUsage bool
	Done         bool
}

func (w *SSEWriter) WriteEvent(ev Event, fallbackModel string) error {
	if ev.Kind == "done" {
		if w.Done {
			return nil
		}
		w.Done = true
		if _, err := io.WriteString(w.W, "data: [DONE]\n\n"); err != nil {
			return err
		}
		w.Flusher.Flush()
		return nil
	}
	if ev.Kind == "usage" && !w.IncludeUsage {
		return nil
	}
	model := ev.Model
	if model == "" {
		model = fallbackModel
	}
	id := ev.ID
	if id == "" {
		id = "chatcmpl-qoder-proxy"
	}
	created := ev.Created
	if created == 0 {
		created = time.Now().Unix()
	}
	delta := map[string]any{}
	if ev.Role != "" {
		delta["role"] = ev.Role
	}
	if ev.Content != "" {
		delta["content"] = ev.Content
	}
	if ev.Reasoning != "" {
		delta["reasoning_content"] = ev.Reasoning
	}
	if len(ev.ToolCalls) > 0 {
		delta["tool_calls"] = ev.ToolCalls
	}
	finish := any(nil)
	if ev.FinishReason != nil {
		finish = *ev.FinishReason
	}
	chunk := map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	if ev.Usage != nil {
		chunk["usage"] = ev.Usage
		chunk["choices"] = []any{}
	}
	b, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w.W, "data: %s\n\n", b); err != nil {
		return err
	}
	w.Flusher.Flush()
	return nil
}
