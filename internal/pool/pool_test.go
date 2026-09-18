package pool

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/J-York/QoderProxy/internal/credential"
)

type countingRefresh struct{ calls atomic.Int64 }

func (f *countingRefresh) Refresh(_ context.Context, a credential.Account) (credential.Account, error) {
	f.calls.Add(1)
	time.Sleep(10 * time.Millisecond)
	a.AccessToken = "new"
	a.ExpiresAtMS = time.Now().Add(time.Hour).UnixMilli()
	return a, nil
}
func account(id string) credential.Account {
	return credential.Account{ID: id, Region: "global", TokenKind: "device", AccessToken: "old", RefreshToken: "r", ExpiresAtMS: 1, UserID: id, MachineID: "m"}
}

func TestConcurrentRefreshSingleFlightPerAccount(t *testing.T) {
	f := &countingRefresh{}
	p := New([]credential.Account{account("a")}, nil, f)
	e, _ := p.Select("", nil)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := p.EnsureFresh(context.Background(), e, false)
			if err != nil || a.AccessToken != "new" {
				t.Errorf("a=%+v err=%v", a, err)
			}
		}()
	}
	wg.Wait()
	if f.calls.Load() != 1 {
		t.Fatalf("refresh calls=%d", f.calls.Load())
	}
}
func TestRoundRobinStickyAndCooldown(t *testing.T) {
	now := time.Now().Add(time.Hour).UnixMilli()
	a, b := account("a"), account("b")
	a.ExpiresAtMS = now
	b.ExpiresAtMS = now
	p := New([]credential.Account{a, b}, nil, nil)
	e1, _ := p.Select("", nil)
	e2, _ := p.Select("", nil)
	x, _, _, _, _ := e1.Snapshot()
	y, _, _, _, _ := e2.Snapshot()
	if x.ID == y.ID {
		t.Fatal("round robin did not rotate")
	}
	s1, _ := p.Select("session-1", nil)
	s2, _ := p.Select("session-1", nil)
	aa, _, _, _, _ := s1.Snapshot()
	bb, _, _, _, _ := s2.Snapshot()
	if aa.ID != bb.ID {
		t.Fatal("sticky session moved")
	}
	p.MarkCooldown(s1, 20*time.Millisecond)
	if p.Available() != 1 {
		t.Fatalf("available=%d", p.Available())
	}
	time.Sleep(25 * time.Millisecond)
	if p.Available() != 2 {
		t.Fatalf("account did not recover after cooldown")
	}
}

// TestDisabledAccountRecoversAfterPeriod 驗證 MarkDisabled 的停用是暫時的：
// 恢復期過後 Snapshot 應惰性轉回 healthy（FORK-PLAN.md §2.2）。
func TestDisabledAccountRecoversAfterPeriod(t *testing.T) {
	old := disabledRecovery
	disabledRecovery = 20 * time.Millisecond
	t.Cleanup(func() { disabledRecovery = old })

	p := New([]credential.Account{account("a")}, nil, nil)
	e, err := p.Select("", nil)
	if err != nil {
		t.Fatal(err)
	}
	p.MarkDisabled(e, "test 403")

	if _, state, _, _, _ := e.Snapshot(); state != "disabled" {
		t.Fatalf("state=%q, want disabled", state)
	}
	if got := p.Available(); got != 0 {
		t.Fatalf("available=%d, want 0", got)
	}

	time.Sleep(40 * time.Millisecond)

	if _, state, _, _, _ := e.Snapshot(); state != "healthy" {
		t.Fatalf("state=%q, want healthy after recovery", state)
	}
	if got := p.Available(); got != 1 {
		t.Fatalf("available=%d, want 1 after recovery", got)
	}
}

// TestRecoverRestoresImmediately 驗證 Recover() 立即恢復（上游死碼的行為契約）。
func TestRecoverRestoresImmediately(t *testing.T) {
	p := New([]credential.Account{account("a")}, nil, nil)
	e, _ := p.Select("", nil)
	p.MarkDisabled(e, "test")

	p.Recover("a")

	if _, state, _, _, _ := e.Snapshot(); state != "healthy" {
		t.Fatalf("state=%q, want healthy after Recover", state)
	}
}
