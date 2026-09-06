package main

import (
	"os"
	"strings"
	"testing"
)

func TestComposeSystemAPIWiresSQLiteAccountDeleteAuthorizationRevoker(t *testing.T) {
	source, err := os.ReadFile("compose.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	needle := "accountStore.SetDeletedResourceGrantRevoker(authzStore)"
	if !strings.Contains(text, needle) {
		t.Fatalf("compose root must wire the SQLite account-delete authorization revoker: %s", needle)
	}
	accountStorePos := strings.Index(text, "accountStore, err := accounts.NewStore")
	wirePos := strings.Index(text, needle)
	if accountStorePos < 0 || wirePos < accountStorePos {
		t.Fatalf("account-delete authorization revoker must be wired after account store construction")
	}
}
