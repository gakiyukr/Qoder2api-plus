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
