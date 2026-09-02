package qoder

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/J-York/QoderProxy/internal/credential"
	"github.com/J-York/QoderProxy/internal/protocol"
)

func testAccount() credential.Account {
	return credential.Account{ID: "a", Region: "global", TokenKind: "device", AccessToken: "dt-test", RefreshToken: "drt-test", ExpiresAtMS: time.Now().Add(time.Hour).UnixMilli(), UserID: "u", MachineID: "m"}
}
func testModel(vl, reasoning bool) Model {
	return Model{ID: "lite", UpstreamKey: "lite", DisplayName: "Lite", IsVL: vl, IsReasoning: reasoning, Source: "system", RawConfig: json.RawMessage(`{"key":"lite","enable":true,"display_name":"Lite","is_vl":true,"is_reasoning":true,"source":"system"}`)}
}

func TestBearerPreservesOpenAIFields(t *testing.T) {
	var got map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dt-test" {
			t.Errorf("auth missing")
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	tr := &BearerTransport{HTTP: srv.Client(), Endpoints: func(protocol.Region) (protocol.Endpoints, bool) { return protocol.Endpoints{BearerChat: srv.URL}, true }}
	raw := []byte(`{"model":"lite","messages":[{"role":"system","content":"s1"},{"role":"system","content":"s2"},{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ping","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"pong"}],"tools":[{"type":"function","function":{"name":"ping","parameters":{"type":"object","properties":{}}}}],"tool_choice":"auto","reasoning_effort":"high","enable_thinking":true,"max_completion_tokens":123,"stream":true}`)
	req, err := ParseChatRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tr.Chat(context.Background(), testAccount(), req, testModel(true, true))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, k := range []string{"messages", "tools", "tool_choice", "reasoning_effort", "enable_thinking", "max_completion_tokens", "metadata"} {
		if len(got[k]) == 0 {
			t.Errorf("missing %s", k)
		}
	}
	var msgs []json.RawMessage
	_ = json.Unmarshal(got["messages"], &msgs)
	if len(msgs) != 5 {
		t.Fatalf("messages=%d", len(msgs))
	}
}

func TestCosyRequestPreservesSystemToolResultAndImage(t *testing.T) {
	var decoded map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		plain, err := DecodeBody(string(b))
		if err != nil {
			t.Error(err)
		}
		_ = json.Unmarshal(plain, &decoded)
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	tr := &CosyTransport{HTTP: srv.Client(), Signer: NewCosySigner(), Endpoints: func(protocol.Region) (protocol.Endpoints, bool) { return protocol.Endpoints{CosyChat: srv.URL}, true }}
	req, _ := ParseChatRequest([]byte(`{"model":"lite","messages":[{"role":"system","content":"keep me"},{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]},{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"ping","arguments":""}}]},{"role":"tool","tool_call_id":"c","content":"ok"}],"stream":true}`))
	resp, err := tr.Chat(context.Background(), testAccount(), req, testModel(true, true))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var msgs []map[string]json.RawMessage
	_ = json.Unmarshal(decoded["messages"], &msgs)
	if len(msgs) != 4 {
		t.Fatalf("messages=%d", len(msgs))
	}
	var system string
	_ = json.Unmarshal(msgs[0]["content"], &system)
	if system != "keep me" {
		t.Fatalf("system=%q", system)
	}
	var assistant string
	_ = json.Unmarshal(msgs[2]["content"], &assistant)
	if assistant != " " {
		t.Fatalf("assistant placeholder=%q", assistant)
	}
	if len(msgs[3]["tool_call_id"]) == 0 {
		t.Fatal("tool result lost")
	}
	if string(msgs[1]["content"]) == "null" {
		t.Fatal("image lost")
	}
}

func TestCapabilitiesRejectUnsupportedImageAndThinking(t *testing.T) {
	req, _ := ParseChatRequest([]byte(`{"model":"lite","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}],"enable_thinking":true}`))
	if err := ValidateCapabilities(req, testModel(false, false)); err == nil {
		t.Fatal("expected capability error")
	}
}

func TestDeveloperRoleIsNormalizedToSystem(t *testing.T) {
	req, err := ParseChatRequest([]byte(`{"model":"lite","messages":[{"role":"developer","content":"follow these instructions"},{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCapabilities(req, testModel(false, false)); err != nil {
		t.Fatal(err)
	}
	var messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(req.Raw["messages"], &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != "system" || messages[0].Content != "follow these instructions" {
		t.Fatalf("unexpected normalized messages: %#v", messages)
	}
}
