package accounthealth

import (
	"context"
	"encoding/base64"
	"os"
	"testing"
	"time"
)

// TestNodePublishedInputCrossLanguageFixture is intentionally opt-in. The
// Node regression creates the signed/encrypted file and owns the temporary
// upstream server; this test proves jobs consumes that stable protocol and
// writes an outcome without Node, Gateway, IPC, or Redis dependencies.
func TestNodePublishedInputCrossLanguageFixture(t *testing.T) {
	inputRoot := os.Getenv("JUHE_AI_J1_CROSS_LANGUAGE_INPUT_DIRECTORY")
	storePath := os.Getenv("JUHE_AI_J1_CROSS_LANGUAGE_STORE_PATH")
	keyText := os.Getenv("JUHE_AI_J1_CROSS_LANGUAGE_SIGNING_KEY")
	secret := os.Getenv("JUHE_AI_J1_CROSS_LANGUAGE_CREDENTIAL_SECRET")
	if inputRoot == "" || storePath == "" || keyText == "" || secret == "" {
		t.Skip("requires the Node-owned J1 cross-language fixture")
	}
	key, err := base64.RawURLEncoding.DecodeString(keyText)
	if err != nil || len(key) < 32 {
		t.Fatalf("decode fixture signing key: %v", err)
	}
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: storePath})
	if err != nil {
		t.Fatalf("open jobs-owned SQLite store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure jobs-owned SQLite schema: %v", err)
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "cross-language-jobs-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire jobs owner lease: acquired=%t err=%v", acquired, err)
	}
	runner := NewRunner(Config{
		InputDirectory:   inputRoot,
		InputKeys:        map[string][]byte{"runtime-v1": key},
		CredentialSecret: secret,
		ProbeTimeout:     3 * time.Second,
		MaxResponseBytes: 4096,
		MaxConcurrency:   1,
		Now:              time.Now,
	}, store, nil)
	if err := runner.runCycle(ctx, lease); err != nil {
		t.Fatalf("run direct J1 probe cycle: %v", err)
	}
}
