package qoder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/J-York/QoderProxy/internal/credential"
	"github.com/J-York/QoderProxy/internal/protocol"
)

type Model struct {
	ID            string                     `json:"id"`
	UpstreamKey   string                     `json:"-"`
	DisplayName   string                     `json:"display_name"`
	IsVL          bool                       `json:"is_vl"`
	IsReasoning   bool                       `json:"is_reasoning"`
	Thinking      map[string]json.RawMessage `json:"thinking_config,omitempty"`
	Source        string                     `json:"source,omitempty"`
	ContextWindow int64                      `json:"context_window,omitempty"`
	RawConfig     json.RawMessage            `json:"-"`
}

type CatalogSnapshot struct {
	Models    []Model
	FetchedAt time.Time
	AccountID string
	Degraded  bool
	LastError string
}

type Catalog struct {
	HTTP      *http.Client
	Signer    *CosySigner
	TTL       time.Duration
	Now       func() time.Time
	Endpoints func(protocol.Region) (protocol.Endpoints, bool)

	mu       sync.RWMutex
	snapshot CatalogSnapshot
}

func NewCatalog(client *http.Client, ttl time.Duration) *Catalog {
	return &Catalog{HTTP: client, Signer: NewCosySigner(), TTL: ttl, Now: time.Now}
}

func (c *Catalog) Snapshot() CatalogSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := c.snapshot
	s.Models = append([]Model(nil), s.Models...)
	return s
}

func (c *Catalog) Get(ctx context.Context, account credential.Account) (CatalogSnapshot, error) {
	c.mu.RLock()
	s := c.snapshot
	fresh := len(s.Models) > 0 && s.AccountID == account.ID && !s.Degraded && c.Now().Sub(s.FetchedAt) < c.TTL
	c.mu.RUnlock()
	if fresh {
		return c.Snapshot(), nil
	}
	models, err := c.fetch(ctx, account)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil && len(models) > 0 {
		c.snapshot = CatalogSnapshot{Models: models, FetchedAt: c.Now(), AccountID: account.ID}
		return c.snapshot, nil
	}
	if len(c.snapshot.Models) > 0 && !c.snapshot.Degraded {
		c.snapshot.LastError = errString(err)
		return c.snapshot, nil
	}
	// Conservative fallback: only a text-only Lite entry, with the observed
	// 180K input limit and no unproven output-token claim.
	c.snapshot = CatalogSnapshot{
		Models:    []Model{{ID: "lite", UpstreamKey: "lite", DisplayName: "Lite", ContextWindow: 180000, Source: "system", RawConfig: json.RawMessage(`{"key":"lite","display_name":"Lite","enable":true,"max_input_tokens":180000,"is_vl":false,"is_reasoning":false,"source":"system"}`)}},
		FetchedAt: c.Now(), AccountID: account.ID, Degraded: true, LastError: errString(err),
	}
	return c.snapshot, err
}

func (c *Catalog) Find(id string) (Model, bool) {
	s := c.Snapshot()
	for _, m := range s.Models {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

func (c *Catalog) fetch(ctx context.Context, account credential.Account) ([]Model, error) {
	endpointFn := c.Endpoints
	if endpointFn == nil {
		endpointFn = protocol.ForRegion
	}
	ep, ok := endpointFn(protocol.Region(account.Region))
	if !ok {
		return nil, errors.New("unsupported region")
	}
	headers, err := c.Signer.Headers(nil, ep.ModelList, account)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.ModelList, nil)
	if err != nil {
		return nil, err
	}
	req.Header = headers
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError("model catalog", resp.StatusCode, b)
	}
	var wire struct {
		Chat []json.RawMessage `json:"chat"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return nil, err
	}
	var models []Model
	for _, raw := range wire.Chat {
		var entry struct {
			Key            string `json:"key"`
			Enable         bool   `json:"enable"`
			DisplayName    string `json:"display_name"`
			MaxInputTokens int64  `json:"max_input_tokens"`
			ContextConfig  map[string]struct {
				TokenCount int64 `json:"token_count"`
			} `json:"context_config"`
			IsVL        bool                       `json:"is_vl"`
			IsReasoning bool                       `json:"is_reasoning"`
			Thinking    map[string]json.RawMessage `json:"thinking_config"`
			Source      string                     `json:"source"`
		}
		if json.Unmarshal(raw, &entry) != nil || !entry.Enable || entry.Key == "" {
			continue
		}
		ctxWindow := int64(0)
		for _, option := range entry.ContextConfig {
			if option.TokenCount > ctxWindow {
				ctxWindow = option.TokenCount
			}
		}
		if ctxWindow == 0 {
			ctxWindow = entry.MaxInputTokens
		}
		display := entry.DisplayName
		if display == "" {
			display = entry.Key
		}
		models = append(models, Model{ID: entry.Key, UpstreamKey: entry.Key, DisplayName: display, IsVL: entry.IsVL, IsReasoning: entry.IsReasoning || len(entry.Thinking) > 0, Thinking: entry.Thinking, Source: entry.Source, ContextWindow: ctxWindow, RawConfig: append(json.RawMessage(nil), raw...)})
	}
	if len(models) == 0 {
		return nil, errors.New("model catalog contained no enabled models")
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%v", err)
}
