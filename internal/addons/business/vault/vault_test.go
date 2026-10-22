package vault

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedRoundTripConflictAndTamperDetection(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vault")
	store := New(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("empty read created storage")
	}
	secret := "synthetic-cookie-never-plaintext"
	saved, err := store.Update(0, func(s *Snapshot) error {
		s.Accounts = append(s.Accounts, Account{ID: NewID(), Enabled: true, Epoch: 1, Credential: Credential{SecureCSES: secret, CSESIndex: "index", ConfigID: "config"}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(0, func(*Snapshot) error { return nil }); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	loaded, err := New(dir).Load()
	if err != nil || loaded.Revision != saved.Revision || loaded.Accounts[0].Credential.SecureCSES != secret {
		t.Fatal("round trip failed", err)
	}
	for _, name := range []string{"key.bin", "accounts.enc"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || bytes.Contains(data, []byte(secret)) {
			t.Fatal("credential leaked to disk", err)
		}
	}
	path := filepath.Join(dir, "accounts.enc")
	data, _ := os.ReadFile(path)
	data[len(data)-1] ^= 1
	_ = os.WriteFile(path, data, 0600)
	if _, err := New(dir).Load(); !errors.Is(err, ErrUnavailable) {
		t.Fatal("accepted tampered vault")
	}
	if _, err := store.Update(saved.Revision, func(*Snapshot) error { return nil }); !errors.Is(err, ErrUnavailable) {
		t.Fatal("overwrote damaged vault")
	}
}
