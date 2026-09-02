package stream

import "strings"

var thinkingTags = map[string]bool{
	"<thinking>": true, "</thinking>": false,
	"<think>": true, "</think>": false,
	"<reasoning>": true, "</reasoning>": false,
}

type Piece struct {
	Reasoning bool
	Text      string
}

type ThinkingRouter struct {
	inThinking       bool
	pending          string
	pendingReasoning bool
}

func (p *ThinkingRouter) Feed(text string, sourceReasoning bool) []Piece {
	var out []Piece
	emit := func(reasoning bool, text string) {
		if text == "" {
			return
		}
		if len(out) > 0 && out[len(out)-1].Reasoning == reasoning {
			out[len(out)-1].Text += text
			return
		}
		out = append(out, Piece{Reasoning: reasoning, Text: text})
	}
	for len(text) > 0 {
		if p.pending == "" {
			i := strings.IndexByte(text, '<')
			if i < 0 {
				emit(sourceReasoning || p.inThinking, text)
				break
			}
			if i > 0 {
				emit(sourceReasoning || p.inThinking, text[:i])
			}
			p.pending = "<"
			p.pendingReasoning = sourceReasoning
			text = text[i+1:]
			continue
		}
		p.pending += text[:1]
		p.pendingReasoning = p.pendingReasoning || sourceReasoning
		text = text[1:]
		if open, ok := thinkingTags[p.pending]; ok {
			p.inThinking = open
			p.pending = ""
			p.pendingReasoning = false
			continue
		}
		if isTagPrefix(p.pending) {
			continue
		}
		emit(p.pendingReasoning || p.inThinking, p.pending)
		p.pending = ""
		p.pendingReasoning = false
	}
	return out
}

func (p *ThinkingRouter) Finalize() []Piece {
	if p.pending == "" {
		return nil
	}
	out := []Piece{{Reasoning: p.pendingReasoning || p.inThinking, Text: p.pending}}
	p.pending = ""
	p.pendingReasoning = false
	return out
}

func isTagPrefix(s string) bool {
	for tag := range thinkingTags {
		if strings.HasPrefix(tag, s) {
			return true
		}
	}
	return false
}

func (p *ThinkingRouter) Route(ev Event) []Event {
	if ev.Kind != "delta" {
		return []Event{ev}
	}
	base := ev
	base.Content, base.Reasoning, base.ToolCalls, base.FinishReason = "", "", nil, nil
	var out []Event
	if ev.Role != "" {
		x := base
		x.Role = ev.Role
		out = append(out, x)
		base.Role = ""
	}
	for _, piece := range p.Feed(ev.Reasoning, true) {
		x := base
		if piece.Reasoning {
			x.Reasoning = piece.Text
		} else {
			x.Content = piece.Text
		}
		out = append(out, x)
	}
	for _, piece := range p.Feed(ev.Content, false) {
		x := base
		if piece.Reasoning {
			x.Reasoning = piece.Text
		} else {
			x.Content = piece.Text
		}
		out = append(out, x)
	}
	if len(ev.ToolCalls) > 0 {
		x := base
		x.ToolCalls = ev.ToolCalls
		out = append(out, x)
	}
	if ev.FinishReason != nil {
		x := base
		x.FinishReason = ev.FinishReason
		out = append(out, x)
	}
	if len(out) == 0 {
		out = append(out, base)
	}
	return out
}
