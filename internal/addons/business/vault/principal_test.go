package vault

import (
	"strings"
	"testing"
)

func TestPrincipalStableAfterReopenAndDistinctKeys(t *testing.T) {
	dir := t.TempDir()
	one, err := New(dir).Principal("synthetic-key-one")
	if err != nil {
		t.Fatal(err)
	}
	again, err := New(dir).Principal("synthetic-key-one")
	if err != nil || again != one {
		t.Fatal("principal changed after reopening the vault")
	}
	two, err := New(dir).Principal("synthetic-key-two")
	if err != nil || two == one || strings.Contains(one, "synthetic-key") {
		t.Fatal("principal ownership is not isolated")
	}
	local, err := New(dir).Principal("")
	if err != nil || local == one || local == two {
		t.Fatal("single tenant principal is not distinct")
	}
}
