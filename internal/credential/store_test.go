package credential

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func validAccount() Account {
	return Account{ID: "a", Region: "global", TokenKind: "job", AccessToken: "jt-secret", RefreshToken: "jrt-secret", ExpiresAtMS: 123, UserID: "u", MachineID: "m"}
}
func TestCredentialStoreWrites0600AndReplacesAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	s := NewStore(path)
	a := validAccount()
	if err := s.Upsert(a); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", st.Mode().Perm())
	}
	a.AccessToken = "jt-new"
	if err := s.Upsert(a); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil || len(got) != 1 || got[0].AccessToken != "jt-new" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
func TestCredentialStoreRejectsPermissiveFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix mode test")
	}
	path := filepath.Join(t.TempDir(), "credentials.json")
	s := NewStore(path)
	if err := s.Upsert(validAccount()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("wanted permission error")
	}
}
