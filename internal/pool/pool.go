package pool

import (
	"context"
	"crypto/sha256"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/J-York/QoderProxy/internal/credential"
)

type Refresher interface {
	Refresh(context.Context, credential.Account) (credential.Account, error)
}

type Entry struct {
	mu                sync.Mutex
	Account           credential.Account
	state             string
	cooldownUntil     time.Time
	disabledUntil     time.Time
	selectedTransport string
	lastError         string
}

// disabledRecovery 是帳號被停用後自動恢復的等待期。
// 包級變數（非常量）以便測試縮短。
var disabledRecovery = 5 * time.Minute

func (e *Entry) Snapshot() (credential.Account, string, time.Time, string, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	state := e.state
	if state == "cooldown" && time.Now().After(e.cooldownUntil) {
		e.state = "healthy"
		state = "healthy"
	}
	if state == "disabled" && !e.disabledUntil.IsZero() && time.Now().After(e.disabledUntil) {
		e.state = "healthy"
		e.disabledUntil = time.Time{}
		state = "healthy"
	}
	return e.Account, state, e.cooldownUntil, e.selectedTransport, e.lastError
}

func (e *Entry) SetTransport(name string) { e.mu.Lock(); e.selectedTransport = name; e.mu.Unlock() }

type Pool struct {
	entries       []*Entry
	store         *credential.Store
	refresh       Refresher
	rr            atomic.Uint64
	refreshBefore time.Duration
	persistMu     sync.Mutex
}

func New(accounts []credential.Account, store *credential.Store, refresh Refresher) *Pool {
	p := &Pool{store: store, refresh: refresh, refreshBefore: 5 * time.Minute}
	for _, a := range accounts {
		p.entries = append(p.entries, &Entry{Account: a, state: "healthy", selectedTransport: a.Transport})
	}
	sort.Slice(p.entries, func(i, j int) bool { return p.entries[i].Account.ID < p.entries[j].Account.ID })
	return p
}

func (p *Pool) Count() int { return len(p.entries) }

func (p *Pool) Available() int {
	n := 0
	for _, e := range p.entries {
		_, state, _, _, _ := e.Snapshot()
		if state == "healthy" {
			n++
		}
	}
	return n
}

func (p *Pool) Select(session string, excluded map[string]bool) (*Entry, error) {
	var candidates []*Entry
	for _, e := range p.entries {
		a, state, _, _, _ := e.Snapshot()
		if state == "healthy" && !excluded[a.ID] {
			candidates = append(candidates, e)
		}
	}
	if len(candidates) == 0 {
		return nil, errors.New("no healthy Qoder account is available")
	}
	var idx int
	if session != "" {
		sum := sha256.Sum256([]byte(session))
		idx = int(sum[0]) % len(candidates)
	} else {
		idx = int(p.rr.Add(1)-1) % len(candidates)
	}
	return candidates[idx], nil
}

func (p *Pool) EnsureFresh(ctx context.Context, e *Entry, force bool) (credential.Account, error) {
	e.mu.Lock()
	now := time.Now().UnixMilli()
	if !force && e.Account.ExpiresAtMS > now+p.refreshBefore.Milliseconds() {
		account := e.Account
		e.mu.Unlock()
		return account, nil
	}
	if p.refresh == nil {
		account := e.Account
		e.mu.Unlock()
		return account, errors.New("no credential refresher configured")
	}
	updated, err := p.refresh.Refresh(ctx, e.Account)
	if err != nil {
		e.lastError = "token refresh failed"
		account := e.Account
		e.mu.Unlock()
		return account, err
	}
	e.Account = updated
	e.state = "healthy"
	e.lastError = ""
	e.mu.Unlock()
	if p.store != nil {
		p.persistMu.Lock()
		defer p.persistMu.Unlock()
		accounts := make([]credential.Account, 0, len(p.entries))
		for _, item := range p.entries {
			item.mu.Lock()
			accounts = append(accounts, item.Account)
			item.mu.Unlock()
		}
		if err := p.store.Replace(accounts); err != nil {
			return updated, err
		}
	}
	return updated, nil
}

func (p *Pool) MarkAuthError(e *Entry) {
	e.mu.Lock()
	e.state = "auth_error"
	e.lastError = "authentication rejected"
	e.mu.Unlock()
}

func (p *Pool) MarkDisabled(e *Entry, reason string) {
	e.mu.Lock()
	e.state = "disabled"
	e.disabledUntil = time.Now().Add(disabledRecovery)
	e.lastError = reason
	e.mu.Unlock()
}
func (p *Pool) MarkCooldown(e *Entry, d time.Duration) {
	if d <= 0 {
		d = time.Minute
	}
	e.mu.Lock()
	e.state = "cooldown"
	e.cooldownUntil = time.Now().Add(d)
	e.lastError = "rate limited"
	e.mu.Unlock()
}
func (p *Pool) Recover(id string) {
	for _, e := range p.entries {
		e.mu.Lock()
		if e.Account.ID == id {
			e.state = "healthy"
			e.cooldownUntil = time.Time{}
			e.lastError = ""
		}
		e.mu.Unlock()
	}
}

type Status struct {
	ID            string    `json:"id"`
	Region        string    `json:"region"`
	State         string    `json:"state"`
	Transport     string    `json:"transport,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	CooldownUntil time.Time `json:"cooldown_until,omitempty"`
}

func (p *Pool) Statuses() []Status {
	out := make([]Status, 0, len(p.entries))
	for _, e := range p.entries {
		a, s, c, t, l := e.Snapshot()
		out = append(out, Status{ID: a.AnonymousID(), Region: a.Region, State: s, Transport: t, LastError: l, CooldownUntil: c})
	}
	return out
}

// AdminEntries 對每個帳號（含非 healthy）以快照執行 fn，供管理端點使用。
// fn 收到帳號值副本與當前狀態；對非 healthy 帳號，呼叫者應避免打上游。
// 帳號是副本，fn 內不得寫回池狀態（寫狀態請用 Mark* 系列）。
func (p *Pool) AdminEntries(fn func(a credential.Account, state string)) {
	for _, e := range p.entries {
		a, state, _, _, _ := e.Snapshot()
		fn(a, state)
	}
}
