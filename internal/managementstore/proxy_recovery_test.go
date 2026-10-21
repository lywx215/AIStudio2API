package managementstore

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

// Use the real private-file participant and reopen SQLite after each crash.
// Recovery must restore both files or retain both, with the original receipt.
func TestProxyAndCredentialsRecoverTogether(t *testing.T) {
	for _, point := range []string{"applied", "metadata-written", "receipt-written", "after-db-commit"} {
		t.Run(point, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			path := filepath.Join(root, "metadata.db")
			s := openTest(t, path)
			state := aistudio.StorageState{Cookies: []aistudio.StateCookie{{Name: "SID", Value: "synthetic-before", Domain: ".google.com", Path: "/", Expires: -1, Secure: true}}}
			before := "socks5://fixture:synthetic-before@before.invalid:1080"
			participant := &aistudio.AccountFileParticipant{AuthPaths: []string{filepath.Join(root, "auth")}, JournalDir: filepath.Join(root, "journal"), NewState: &state, ProxyOverride: &before}
			digest, err := aistudio.CredentialDigest(state)
			if err != nil {
				t.Fatal(err)
			}
			first, err := s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationImport, NativeID: "synthetic@example.invalid", CredentialDigest: digest}, participant)
			if err != nil {
				t.Fatal(err)
			}
			state.Cookies[0].Value = "synthetic-after"
			after := "socks5://fixture:synthetic-after@after.invalid:1081"
			participant.ProxyOverride = &after
			digest, err = aistudio.CredentialDigest(state)
			if err != nil {
				t.Fatal(err)
			}
			upload := &c.UploadIdentity{TaskID: id(t, "task"), Kind: c.TaskReplace}
			credential, version := first.Account.CredentialVersion, first.Account.StateVersion
			intent := c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationReplace, AccountID: first.Account.AccountID, NativeID: first.Account.NativeID,
				Expected: c.ExpectedVersions{Credential: &credential, State: &version}, CredentialChanged: true, CredentialDigest: digest, ProxyChanged: true, ProxyApplied: true, Upload: upload}
			s.fault = func(stage string) error {
				if stage == point {
					panic("synthetic crash")
				}
				return nil
			}
			captureCrash(t, func() { _, _ = s.CommitAccount(ctx, intent, participant) })
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s = openTest(t, path)
			recovery := &aistudio.AccountFileParticipant{AuthPaths: participant.AuthPaths, JournalDir: participant.JournalDir}
			if err := s.RecoverAccounts(ctx, recovery); err != nil {
				t.Fatal(err)
			}
			if err := s.RecoverAccounts(ctx, recovery); err != nil {
				t.Fatal(err)
			}
			directory, err := aistudio.AccountDirectory(participant.AuthPaths, first.Account.NativeID, false)
			if err != nil {
				t.Fatal(err)
			}
			account, err := aistudio.LoadAccountDirectory(directory)
			if err != nil {
				t.Fatal(err)
			}
			current, err := s.GetAccount(ctx, first.Account.AccountID)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := s.GetUploadReceipt(ctx, *upload)
			if err != nil {
				t.Fatal(err)
			}
			if point == "after-db-commit" {
				if account.Config.Proxy != after || account.StorageState.Cookies[0].Value != "synthetic-after" || receipt == nil || !receipt.ProxyApplied || receipt.Versions != current.Versions || current.StateVersion != version+1 || current.CredentialVersion != credential+1 {
					t.Fatal("committed proxy, credential and receipt did not survive restart")
				}
				public, _ := json.Marshal(receipt)
				if strings.Contains(string(public), "synthetic-after") || strings.Contains(string(public), "socks5") {
					t.Fatal("public receipt leaked proxy")
				}
			} else if account.Config.Proxy != before || account.StorageState.Cookies[0].Value != "synthetic-before" || receipt != nil || current.Versions != first.Account.Versions {
				t.Fatal("uncommitted replacement was not fully rolled back")
			}
		})
	}
}
