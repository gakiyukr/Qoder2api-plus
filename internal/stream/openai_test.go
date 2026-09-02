package stream

import "testing"

func TestAggregateNoArgumentAndParallelTools(t *testing.T) {
	a := NewAccumulator()
	a.Add(Event{ToolCalls: []ToolCallDelta{{Index: 0, ID: "a", Type: "function", Function: ToolFunctionDelta{Name: "ping"}}, {Index: 1, ID: "b", Type: "function", Function: ToolFunctionDelta{Name: "sum", Arguments: "{\"a\":"}}}})
	a.Add(Event{ToolCalls: []ToolCallDelta{{Index: 1, Function: ToolFunctionDelta{Arguments: "1}"}}}})
	finish := "tool_calls"
	a.Add(Event{FinishReason: &finish})
	resp := a.Response("m")
	choices := resp["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	calls := msg["tool_calls"].([]map[string]any)
	if calls[0]["function"].(map[string]any)["arguments"] != "{}" {
		t.Fatalf("no-arg=%v", calls[0])
	}
	if calls[1]["function"].(map[string]any)["arguments"] != "{\"a\":1}" {
		t.Fatalf("split args=%v", calls[1])
	}
	if choices[0].(map[string]any)["finish_reason"] != "tool_calls" {
		t.Fatal("finish reason changed")
	}
}
