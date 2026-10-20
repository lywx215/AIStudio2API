package aistudio

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	contract "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

// AccountFileParticipant implements the file half of the metadata store's
// recoverable commit. A journal is immutable until the store resolves it.
// NewState/NewConfig are private command inputs, never persisted in metadata.
type AccountFileParticipant struct {
	AuthPaths  []string
	JournalDir string
	NewState   *StorageState
	NewConfig  *AccountConfig
	ExtraFiles map[string][]byte
}

type accountFileImage struct {
	Name         string `json:"name"`
	Before       []byte `json:"before"`
	After        []byte `json:"after"`
	BeforeExists bool   `json:"beforeExists"`
	AfterExists  bool   `json:"afterExists"`
}

type accountFileJournal struct {
	OperationID      string             `json:"operationId"`
	NativeID         string             `json:"nativeId"`
	Directory        string             `json:"directory"`
	CreatedDirectory bool               `json:"createdDirectory"`
	Delete           bool               `json:"delete"`
	BackupDirectory  string             `json:"backupDirectory,omitempty"`
	Directories      []string           `json:"directories,omitempty"`
	Files            []accountFileImage `json:"files"`
}

// CredentialDigest hashes canonical native storage state, so harmless JSON
// whitespace never invalidates a credential version.
func CredentialDigest(state StorageState) ([32]byte, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(data), nil
}

// AccountDirectory resolves only explicitly configured roots. Missing accounts
// can be created only with one directory root; ambiguous roots fail closed.
func AccountDirectory(paths []string, nativeID string, create bool) (string, error) {
	id, err := normalizeAccountEmail(nativeID)
	if err != nil || id != nativeID {
		return "", contract.NewError(contract.CodeInvalidCredentials)
	}
	var found string
	for _, root := range paths {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return "", err
		}
		if filepath.Base(absolute) == storageStateName {
			absolute = filepath.Dir(absolute)
		}
		candidate := filepath.Join(absolute, id)
		if filepath.Base(absolute) == id {
			candidate = absolute
		}
		if err := validateAccountPathAncestors(candidate); err != nil {
			return "", err
		}
		info, statErr := os.Lstat(candidate)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return "", statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("account path is not a private directory")
		}
		if found != "" && found != candidate {
			return "", fmt.Errorf("duplicate native account directory")
		}
		found = candidate
	}
	if found != "" {
		return found, nil
	}
	if !create || len(paths) != 1 {
		return "", contract.NewError(contract.CodeNotFound)
	}
	root, err := filepath.Abs(paths[0])
	if err != nil {
		return "", err
	}
	if filepath.Base(root) == storageStateName || filepath.Base(root) == id {
		return "", contract.NewError(contract.CodeInvalidState)
	}
	return filepath.Join(root, id), nil
}

func (p *AccountFileParticipant) journalPath(operation string) (string, error) {
	if !strings.HasPrefix(operation, "op_") || !contract.ValidID(operation) || p.JournalDir == "" {
		return "", fmt.Errorf("invalid account journal identity")
	}
	root, err := filepath.Abs(p.JournalDir)
	if err != nil {
		return "", err
	}
	if err := validateAccountPathAncestors(root); err != nil {
		return "", err
	}
	return filepath.Join(root, operation+".json"), nil
}

func (p *AccountFileParticipant) Prepare(ctx context.Context, plan contract.CommitPlan) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ref, err := p.journalPath(plan.Intent.OperationID)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(ref); err == nil {
		return "", fmt.Errorf("account journal already exists")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	directory, err := AccountDirectory(p.AuthPaths, plan.After.NativeID, plan.Before == nil)
	if err != nil {
		return "", err
	}
	_, statErr := os.Lstat(directory)
	if plan.Before == nil && statErr == nil {
		return "", fmt.Errorf("untracked account directory already exists")
	}
	if plan.Before != nil {
		state, err := LoadStorageState(filepath.Join(directory, storageStateName))
		if err != nil {
			return "", err
		}
		digest, err := CredentialDigest(state)
		if err != nil || digest != plan.Before.CredentialDigest {
			return "", fmt.Errorf("before credential image differs from metadata")
		}
		config, err := readAccountConfig(filepath.Join(directory, accountConfigName))
		if err != nil {
			return "", err
		}
		if config.Enabled != (plan.Before.Enabled && !plan.Before.Archived && !plan.Before.Deleted) {
			return "", fmt.Errorf("before account state differs from metadata")
		}
	}
	journal := accountFileJournal{OperationID: plan.Intent.OperationID, NativeID: plan.After.NativeID, Directory: directory, CreatedDirectory: os.IsNotExist(statErr), Delete: plan.Intent.Kind == contract.MutationDelete}
	if statErr != nil && !os.IsNotExist(statErr) {
		return "", statErr
	}
	// Delete captures every regular file, including browser fingerprint and runtime
	// state. Workers must be drained before Prepare, not from this bounded method.
	names := []string{accountConfigName, storageStateName}
	for name := range p.ExtraFiles {
		if name != "camoufox-fingerprint.json" {
			return "", fmt.Errorf("unsupported credential companion file")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if journal.Delete {
		if err := validateAccountTree(directory); err != nil {
			return "", err
		}
		journal.BackupDirectory = filepath.Join(filepath.Dir(directory), ".account-delete-"+plan.Intent.OperationID+".tmp")
		if _, err := os.Lstat(journal.BackupDirectory); !os.IsNotExist(err) {
			if err != nil {
				return "", err
			}
			return "", fmt.Errorf("pending account delete backup already exists")
		}
		names = nil
		entries, err := os.ReadDir(directory)
		if err != nil {
			return "", err
		}
		for _, entry := range entries {
			if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
				journal.Directories = append(journal.Directories, entry.Name())
				continue
			}
			if !entry.Type().IsRegular() {
				return "", fmt.Errorf("unexpected account directory entry")
			}
			names = append(names, entry.Name())
		}
	}
	for _, name := range names {
		image := accountFileImage{Name: name}
		data, err := readPrivateAccountFile(filepath.Join(directory, name))
		if err == nil {
			image.Before, image.BeforeExists = data, true
		} else if !os.IsNotExist(err) {
			return "", err
		}
		image.After, image.AfterExists = append([]byte(nil), image.Before...), image.BeforeExists
		if journal.Delete {
			image.After, image.AfterExists = nil, false
		} else {
			switch name {
			case accountConfigName:
				var config AccountConfig
				if p.NewConfig != nil {
					config = *p.NewConfig
				} else if image.BeforeExists {
					if err := json.Unmarshal(image.Before, &config); err != nil {
						return "", err
					}
				} else {
					config = DefaultAccountConfig(plan.After.NativeID)
				}
				config.Label, config.Enabled = plan.After.NativeID, plan.After.Enabled && !plan.After.Archived && !plan.After.Deleted
				if err := config.Validate(); err != nil {
					return "", err
				}
				image.After, err = json.MarshalIndent(config, "", "  ")
				if err != nil {
					return "", err
				}
				image.After = append(image.After, '\n')
				image.AfterExists = true
			case storageStateName:
				if p.NewState != nil {
					if err := p.NewState.Validate(); err != nil {
						return "", err
					}
					image.After, err = json.MarshalIndent(*p.NewState, "", "  ")
					if err != nil {
						return "", err
					}
					image.After = append(image.After, '\n')
					image.AfterExists = true
				}
				if !image.AfterExists {
					return "", fmt.Errorf("credential file missing")
				}
				var state StorageState
				if err := json.Unmarshal(image.After, &state); err != nil {
					return "", err
				}
				if id, err := accountEmailID(DefaultAccountConfig(plan.After.NativeID), state); err != nil || id != plan.After.NativeID {
					return "", fmt.Errorf("credential native identity mismatch")
				}
				digest, err := CredentialDigest(state)
				if err != nil || digest != plan.After.CredentialDigest {
					return "", fmt.Errorf("credential digest does not match commit plan")
				}
			default:
				image.After = append([]byte(nil), p.ExtraFiles[name]...)
				image.AfterExists = true
			}
		}
		journal.Files = append(journal.Files, image)
	}
	if err := os.MkdirAll(filepath.Dir(ref), 0o700); err != nil {
		return "", err
	}
	bytes, err := json.Marshal(journal)
	if err != nil {
		return "", err
	}
	if err := atomicWriteFile(ref, bytes, 0o600); err != nil {
		return "", err
	}
	return ref, nil
}

func readPrivateAccountFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("account file is not regular")
	}
	return os.ReadFile(path)
}

func (p *AccountFileParticipant) load(record contract.RecoveryRecord) (accountFileJournal, string, error) {
	ref, err := p.journalPath(record.Plan.Intent.OperationID)
	if err != nil {
		return accountFileJournal{}, "", err
	}
	if record.ParticipantRef != "" && filepath.Clean(record.ParticipantRef) != ref {
		return accountFileJournal{}, "", fmt.Errorf("untrusted participant reference")
	}
	data, err := readPrivateAccountFile(ref)
	if err != nil {
		return accountFileJournal{}, ref, err
	}
	var journal accountFileJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return journal, ref, err
	}
	if journal.OperationID != record.Plan.Intent.OperationID || journal.NativeID != record.Plan.After.NativeID {
		return journal, ref, fmt.Errorf("journal identity mismatch")
	}
	permitted := false
	for _, root := range p.AuthPaths {
		absolute, pathErr := filepath.Abs(root)
		if pathErr != nil {
			return journal, ref, pathErr
		}
		if filepath.Base(absolute) == storageStateName {
			absolute = filepath.Dir(absolute)
		}
		candidate := filepath.Join(absolute, journal.NativeID)
		if filepath.Base(absolute) == journal.NativeID {
			candidate = absolute
		}
		if candidate == journal.Directory {
			permitted = true
			break
		}
	}
	if !permitted {
		return journal, ref, fmt.Errorf("journal directory mismatch")
	}
	if err := validateAccountPathAncestors(journal.Directory); err != nil {
		return journal, ref, err
	}
	if journal.Delete && journal.BackupDirectory != filepath.Join(filepath.Dir(journal.Directory), ".account-delete-"+journal.OperationID+".tmp") {
		return journal, ref, fmt.Errorf("untrusted account delete backup")
	}
	if journal.Delete {
		if err := validateAccountPathAncestors(journal.BackupDirectory); err != nil {
			return journal, ref, err
		}
	}
	for _, file := range journal.Files {
		if file.Name == "" || filepath.Base(file.Name) != file.Name || file.Name == "." || file.Name == ".." {
			return journal, ref, fmt.Errorf("invalid journal file")
		}
	}
	return journal, ref, nil
}

func matchesFile(path string, data []byte, exists bool) (bool, error) {
	actual, err := readPrivateAccountFile(path)
	if os.IsNotExist(err) {
		return !exists, nil
	}
	if err != nil {
		return false, err
	}
	return exists && sha256.Sum256(actual) == sha256.Sum256(data), nil
}

func writeImage(path string, data []byte, exists bool) error {
	if !exists {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return atomicWriteFile(path, data, 0o600)
}

func (p *AccountFileParticipant) Apply(ctx context.Context, record contract.RecoveryRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	journal, _, err := p.load(record)
	if err != nil {
		return err
	}
	if journal.Delete {
		return applyAccountDelete(journal)
	}
	// Check the whole image before touching any file. Partial images caused by a
	// crash are accepted only if every file is exactly its before or after image.
	if err := checkJournalImage(journal, false); err != nil {
		return err
	}
	if err := os.MkdirAll(journal.Directory, 0o700); err != nil {
		return err
	}
	for _, file := range journal.Files {
		if err := writeImage(filepath.Join(journal.Directory, file.Name), file.After, file.AfterExists); err != nil {
			return err
		}
	}
	return nil
}

func checkJournalImage(journal accountFileJournal, afterOnly bool) error {
	for _, file := range journal.Files {
		path := filepath.Join(journal.Directory, file.Name)
		after, err := matchesFile(path, file.After, file.AfterExists)
		if err != nil {
			return err
		}
		if after {
			continue
		}
		if !afterOnly {
			before, err := matchesFile(path, file.Before, file.BeforeExists)
			if err != nil {
				return err
			}
			if before {
				continue
			}
		}
		return fmt.Errorf("account files changed outside pending commit")
	}
	return nil
}

func (p *AccountFileParticipant) Rollback(_ context.Context, record contract.RecoveryRecord) error {
	journal, ref, err := p.load(record)
	if os.IsNotExist(err) {
		if record.Phase == contract.CommitPlanned {
			return clearAccountWriteTemps(p.JournalDir, []string{record.Plan.Intent.OperationID + ".json"})
		}
		return p.verifyResolved(record, false)
	}
	if err != nil {
		return err
	}
	if journal.Delete {
		if err := rollbackAccountDelete(journal); err != nil {
			return err
		}
		return removeAccountJournal(ref)
	}
	if err := checkJournalImage(journal, false); err != nil {
		return err
	}
	for _, file := range journal.Files {
		if err := writeImage(filepath.Join(journal.Directory, file.Name), file.Before, file.BeforeExists); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(journal.Files))
	for _, file := range journal.Files {
		names = append(names, file.Name)
	}
	if err := clearAccountWriteTemps(journal.Directory, names); err != nil {
		return err
	}
	if journal.CreatedDirectory {
		if err := os.Remove(journal.Directory); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return removeAccountJournal(ref)
}

func (p *AccountFileParticipant) Finalize(_ context.Context, record contract.RecoveryRecord) error {
	journal, ref, err := p.load(record)
	// A crash after unlinking a finalized journal is already complete. Verify the
	// committed credential digest rather than reapplying an old after image.
	if os.IsNotExist(err) {
		return p.verifyResolved(record, true)
	}
	if err != nil {
		return err
	}
	if journal.Delete {
		if _, err := os.Lstat(journal.Directory); !os.IsNotExist(err) {
			if err != nil {
				return err
			}
			return fmt.Errorf("deleted account directory reappeared")
		}
		if _, err := os.Lstat(journal.BackupDirectory); err == nil {
			if err := checkAccountDeleteBackup(journal, true); err != nil {
				return err
			}
			// BackupDirectory was checked against the operation-specific sibling
			// above; RemoveAll never receives an unchecked/computed external path.
			if err := os.RemoveAll(journal.BackupDirectory); err != nil {
				return err
			}
			if err := syncAccountParent(journal.BackupDirectory); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		return removeAccountJournal(ref)
	}
	if err := checkJournalImage(journal, true); err != nil {
		return err
	}
	names := make([]string, 0, len(journal.Files))
	for _, file := range journal.Files {
		names = append(names, file.Name)
	}
	if err := clearAccountWriteTemps(journal.Directory, names); err != nil {
		return err
	}
	return removeAccountJournal(ref)
}

func checkAccountDeleteBackup(journal accountFileJournal, allowRemoved bool) error {
	if err := validateAccountTree(journal.BackupDirectory); err != nil {
		return err
	}
	info, err := os.Lstat(journal.BackupDirectory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("delete backup is not a private directory")
	}
	for _, file := range journal.Files {
		path := filepath.Join(journal.BackupDirectory, file.Name)
		before, err := matchesFile(path, file.Before, file.BeforeExists)
		if err != nil {
			return err
		}
		if before {
			continue
		}
		if allowRemoved {
			if _, err := os.Lstat(path); os.IsNotExist(err) {
				continue
			}
		}
		return fmt.Errorf("delete backup changed outside pending commit")
	}
	known := make(map[string]bool, len(journal.Files)+len(journal.Directories))
	for _, file := range journal.Files {
		known[file.Name] = true
	}
	for _, name := range journal.Directories {
		if filepath.Base(name) != name {
			return fmt.Errorf("invalid delete backup directory")
		}
		known[name] = true
	}
	entries, err := os.ReadDir(journal.BackupDirectory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !known[entry.Name()] || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected delete backup entry")
		}
	}
	return nil
}

func validateAccountPathAncestors(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for current := absolute; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if accountPathIsReparse(info) {
				return fmt.Errorf("account path contains a reparse point")
			}
			if current != absolute && !info.IsDir() {
				return fmt.Errorf("account path parent is not a directory")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}

func validateAccountTree(directory string) error {
	if err := validateAccountPathAncestors(directory); err != nil {
		return err
	}
	return filepath.WalkDir(directory, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if accountPathIsReparse(info) {
			return fmt.Errorf("account directory contains a reparse point")
		}
		return nil
	})
}

func applyAccountDelete(journal accountFileJournal) error {
	_, backupErr := os.Lstat(journal.BackupDirectory)
	if backupErr == nil {
		if _, err := os.Lstat(journal.Directory); !os.IsNotExist(err) {
			return fmt.Errorf("account delete has both source and backup")
		}
		return checkAccountDeleteBackup(journal, false)
	}
	if !os.IsNotExist(backupErr) {
		return backupErr
	}
	before := journal
	before.BackupDirectory = journal.Directory
	if err := checkAccountDeleteBackup(before, false); err != nil {
		return err
	}
	// Same-parent rename is atomic, does not copy the potentially large browser
	// cache, and records all nested files in the preserved before directory.
	if err := os.Rename(journal.Directory, journal.BackupDirectory); err != nil {
		return err
	}
	return syncAccountParent(journal.Directory)
}

func rollbackAccountDelete(journal accountFileJournal) error {
	_, backupErr := os.Lstat(journal.BackupDirectory)
	if os.IsNotExist(backupErr) {
		before := journal
		before.BackupDirectory = journal.Directory
		return checkAccountDeleteBackup(before, false)
	}
	if backupErr != nil {
		return backupErr
	}
	if _, err := os.Lstat(journal.Directory); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return fmt.Errorf("rollback refuses a newer account directory")
	}
	if err := checkAccountDeleteBackup(journal, false); err != nil {
		return err
	}
	if err := os.Rename(journal.BackupDirectory, journal.Directory); err != nil {
		return err
	}
	return syncAccountParent(journal.Directory)
}

func removeAccountJournal(ref string) error {
	if err := os.Remove(ref); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return syncAccountParent(ref)
}

func clearAccountWriteTemps(directory string, names []string) error {
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		for _, name := range names {
			if strings.HasPrefix(entry.Name(), "."+name+"-") {
				if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil && !os.IsNotExist(err) {
					return err
				}
				break
			}
		}
	}
	return nil
}

func (p *AccountFileParticipant) verifyResolved(record contract.RecoveryRecord, after bool) error {
	expected := record.Plan.Before
	if after {
		expected = &record.Plan.After
	}
	directory, err := AccountDirectory(p.AuthPaths, record.Plan.After.NativeID, false)
	missing := contract.PublicError(err).Code == contract.CodeNotFound
	if expected == nil || expected.Deleted {
		if missing {
			return nil
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("removed account directory reappeared")
	}
	if err != nil {
		return err
	}
	state, err := LoadStorageState(filepath.Join(directory, storageStateName))
	if err != nil {
		return err
	}
	digest, err := CredentialDigest(state)
	if err != nil || digest != expected.CredentialDigest {
		return fmt.Errorf("resolved credential image changed")
	}
	config, err := readAccountConfig(filepath.Join(directory, accountConfigName))
	if err != nil {
		return err
	}
	if config.Enabled != (expected.Enabled && !expected.Archived) {
		return fmt.Errorf("resolved account state changed")
	}
	return nil
}

var _ contract.AccountCommitParticipant = (*AccountFileParticipant)(nil)
