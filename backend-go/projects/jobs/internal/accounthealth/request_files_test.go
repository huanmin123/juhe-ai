package accounthealth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadSignedProbeRequestsAcceptsSourceFence(t *testing.T) {
	root := t.TempDir()
	request := ProbeRequest{RequestID: "request-1", AccountID: "account-1", Reason: "request_failure", InputVersion: 2, ConfigRevision: 3, DispatchRevision: 4, Deadline: time.Now().UTC().Add(time.Minute), SourceFence: &SourceFence{StateKey: "state-1", AccountID: "account-1", SourceGeneration: 1, SourceFenceID: "fence-1", RuntimeKey: "runtime-1", ProbeGeneration: 1, ConfigRevision: 3}}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "request-1"+requestFileSuffix), signedEnvelope(t, "current", []byte("key"), payload), 0o600); err != nil {
		t.Fatal(err)
	}
	requests, err := LoadSignedProbeRequests(root, map[string][]byte{"current": []byte("key")})
	if err != nil || len(requests) != 1 || requests[0].SourceFence == nil || requests[0].SourceFence.SourceFenceID != "fence-1" {
		t.Fatalf("requests=%#v err=%v", requests, err)
	}
}
