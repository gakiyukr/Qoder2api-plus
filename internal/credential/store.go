package credential

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

type Account struct {
	ID           string `json:"id"`
	Region       string `json:"region"`
	TokenKind    string `json:"token_kind"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAtMS  int64  `json:"expires_at_ms"`
	UserID       string `json:"user_id"`
	Name         string `json:"name,omitempty"`
	Email        string `json:"email,omitempty"`
	MachineID    string `json:"machine_id"`
	Transport    string `json:"transport,omitempty"`
}

func (a Account) AnonymousID() string {
	s := sha256.Sum256([]byte(a.Region + "\x00" + a.UserID + "\x00" + a.ID))
	return hex.EncodeToString(s[:])[:12]
}

type fileShape struct {
	Version  int       `json:"version"`
	Accounts []Account `json:"accounts"`
}

type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(path string) *Store { return &Store{path: path} }

func (s *Store) Path() string { return s.path }

func (s *Store) Load() ([]Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() ([]Account, error) {
	st, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("credentials file %s is too permissive: want 0600, got %04o", s.path, st.Mode().Perm())
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	var f fileShape
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("unsupported credentials version %d", f.Version)
	}
	for i := range f.Accounts {
		if err := Validate(f.Accounts[i]); err != nil {
			return nil, fmt.Errorf("account %d: %w", i, err)
		}
	}
	return f.Accounts, nil
}

func Validate(a Account) error {
	if a.ID == "" || a.UserID == "" || a.AccessToken == "" || a.MachineID == "" {
		return errors.New("id, user_id, access_token, and machine_id are required")
	}
	if a.Region != "global" && a.Region != "cn" {
		return errors.New("region must be global or cn")
	}
	if a.TokenKind != "job" && a.TokenKind != "device" {
		return errors.New("token_kind must be job or device")
	}
	return nil
}

func (s *Store) Upsert(account Account) error {
	if err := Validate(account); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	accounts, err := s.loadLocked()
	if err != nil {
		return err
	}
	found := false
	for i := range accounts {
		if accounts[i].ID == account.ID || (accounts[i].Region == account.Region && accounts[i].UserID == account.UserID) {
			accounts[i] = account
			found = true
			break
		}
	}
	if !found {
		accounts = append(accounts, account)
	}
	return s.writeLocked(accounts)
}

func (s *Store) Replace(accounts []Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range accounts {
		if err := Validate(accounts[i]); err != nil {
			return err
		}
	}
	return s.writeLocked(accounts)
}

func (s *Store) writeLocked(accounts []Account) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(fileShape{Version: 1, Accounts: accounts}, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return err
	}
	ok = true
	return nil
}
