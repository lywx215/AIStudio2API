package aistudio

import (
	"bytes"
	"context"
	"encoding/base64"
	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
	"os"
	"path/filepath"
	"testing"
)

func TestLoginPasswordDeleteJournalDoesNotCopyPassword(t *testing.T) {
	p, record, _, _ := g2FileFixture(t)
	directory, err := AccountDirectory(p.AuthPaths, record.Plan.Before.NativeID, false)
	if err != nil {
		t.Fatal(err)
	}
	password := "private-login-password-marker"
	credentials := LoginCredentials{Email: record.Plan.Before.NativeID, Password: password, AccountID: record.Plan.Before.AccountID, Version: 1, LastOperationID: "10000000-0000-4000-8000-000000000001"}
	if err := WriteLoginCredentials(directory, credentials); err != nil {
		t.Fatal(err)
	}
	privateFile, _ := os.ReadFile(filepath.Join(directory, LoginCredentialsName))
	if err := os.WriteFile(filepath.Join(directory, "."+LoginCredentialsName+"-crash.tmp"), privateFile, 0600); err != nil {
		t.Fatal(err)
	}
	record.Plan.Intent.Kind = c.MutationDelete
	record.Plan.After = *record.Plan.Before
	record.Plan.After.Deleted = true
	ref, err := p.Prepare(context.Background(), record.Plan)
	if err != nil {
		t.Fatal(err)
	}
	record.ParticipantRef = ref
	raw, _ := os.ReadFile(ref)
	if bytes.Contains(raw, []byte(password)) || bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString([]byte(password)))) {
		t.Fatal("delete journal copied password")
	}
	stored, _ := os.ReadFile(filepath.Join(directory, LoginCredentialsName))
	if bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(stored))) {
		t.Fatal("delete journal copied password file")
	}
	if err := p.Apply(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := p.Rollback(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if recovered, err := ReadLoginCredentials(directory, credentials.Email); err != nil || recovered.Password != password {
		t.Fatal("rollback did not preserve password", err)
	}
}
