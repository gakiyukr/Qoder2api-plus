package stream

import "encoding/json"

type ToolFunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type ToolCallDelta struct {
	Index    int               `json:"index"`
	ID       string            `json:"id,omitempty"`
	Type     string            `json:"type,omitempty"`
	Function ToolFunctionDelta `json:"function"`
}

type UsageDetails struct {
	CachedTokens     int64 `json:"cached_tokens,omitempty"`
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
}

type Usage struct {
	PromptTokens            int64           `json:"prompt_tokens"`
	CompletionTokens        int64           `json:"completion_tokens"`
	TotalTokens             int64           `json:"total_tokens"`
	PromptTokensDetails     UsageDetails    `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails json.RawMessage `json:"completion_tokens_details,omitempty"`
}

type Event struct {
	Kind         string
	ID           string
	Created      int64
	Model        string
	Role         string
	Content      string
	Reasoning    string
	ToolCalls    []ToolCallDelta
	Usage        *Usage
	FinishReason *string
}
