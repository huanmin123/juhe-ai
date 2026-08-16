package accounthealth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSignedInputFilesRejectsTamperedSnapshot(t *testing.T) {
	root := t.TempDir()
	input := testInput("https://api.example.com", "chat_json")
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "account-1"+inputFileSuffix)
	if err := os.WriteFile(path, signedEnvelope(t, "current", []byte("key"), payload), 0o600); err != nil {
		t.Fatal(err)
	}
	inputs, err := LoadSignedInputFiles(root, map[string][]byte{"current": []byte("key")})
	if err != nil || len(inputs) != 1 || inputs[0].AccountID != input.AccountID {
		t.Fatalf("inputs=%#v err=%v", inputs, err)
	}
	if err := os.WriteFile(path, []byte(`{"algorithm":"hmac-sha256-v1","key_id":"current","payload":"e30","signature":"bad"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSignedInputFiles(root, map[string][]byte{"current": []byte("key")}); err == nil {
		t.Fatal("tampered input file must be rejected")
	}
}
