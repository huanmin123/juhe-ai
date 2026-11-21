package authzrevision

import (
	"reflect"
	"testing"
)

const testSecret = "revision-test-secret"

func TestDecodeEndpointModes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "empty", raw: "[]", want: []string{}},
		{name: "sorted values", raw: "[\"chat_json\",\"responses_sse\"]", want: []string{"chat_json", "responses_sse"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeEndpointModes(test.raw)
			if err != nil {
				t.Fatalf("decodeEndpointModes() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("decodeEndpointModes() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestCredentialMaterialFingerprintsCoversFullAPIKeyPool(t *testing.T) {
	base := map[string]any{"api_keys": []any{"sk-a", "sk-b"}, "email": "owner@example.com"}
	pool, subject := credentialMaterialFingerprints(testSecret, base)
	if pool == "" || subject == "" {
		t.Fatalf("fingerprints must be set: pool=%q subject=%q", pool, subject)
	}
	same, sameSubject := credentialMaterialFingerprints(testSecret, map[string]any{
		"api_keys": []any{"sk-a", "sk-b"}, "email": "owner@example.com",
	})
	if pool != same || subject != sameSubject {
		t.Fatal("identical material must produce identical fingerprints")
	}
	// Replacing the second (non-primary) key is material even though
	// accounts.credential_fingerprint only hashes the first key.
	changed, _ := credentialMaterialFingerprints(testSecret, map[string]any{
		"api_keys": []any{"sk-a", "sk-c"}, "email": "owner@example.com",
	})
	if changed == pool {
		t.Fatal("second pool member change must change the pool fingerprint")
	}
	// Reordering changes dispatch order and is therefore material too.
	reordered, _ := credentialMaterialFingerprints(testSecret, map[string]any{
		"api_keys": []any{"sk-b", "sk-a"}, "email": "owner@example.com",
	})
	if reordered == pool {
		t.Fatal("pool reorder must change the pool fingerprint")
	}
}

func TestCredentialMaterialFingerprintsSubjectIgnoresTokenRotation(t *testing.T) {
	before := map[string]any{
		"refresh_token": "rt-old", "access_token": "at-old",
		"account_id": "acct-1", "sub": "sub-1", "email": "owner@example.com",
	}
	_, subject := credentialMaterialFingerprints(testSecret, before)
	rotated := map[string]any{
		"refresh_token": "rt-new", "access_token": "at-new",
		"account_id": "acct-1", "sub": "sub-1", "email": "owner@example.com",
	}
	_, rotatedSubject := credentialMaterialFingerprints(testSecret, rotated)
	if subject == "" || subject != rotatedSubject {
		t.Fatalf("token rotation must not change the subject fingerprint: %q vs %q", subject, rotatedSubject)
	}
	changed := map[string]any{
		"refresh_token": "rt-old", "access_token": "at-old",
		"account_id": "acct-2", "sub": "sub-1", "email": "owner@example.com",
	}
	_, changedSubject := credentialMaterialFingerprints(testSecret, changed)
	if changedSubject == subject {
		t.Fatal("subject identity change must change the subject fingerprint")
	}
}

func TestCredentialMaterialFingerprintsDependsOnSecret(t *testing.T) {
	credentials := map[string]any{"api_key": "sk-a", "email": "owner@example.com"}
	poolA, subjectA := credentialMaterialFingerprints("secret-a", credentials)
	poolB, subjectB := credentialMaterialFingerprints("secret-b", credentials)
	if poolA == poolB || subjectA == subjectB {
		t.Fatal("different revision secrets must produce different fingerprints")
	}
}

func TestCredentialMaterialFingerprintsEmptyMaterial(t *testing.T) {
	pool, subject := credentialMaterialFingerprints(testSecret, map[string]any{})
	if pool != "" || subject != "" {
		t.Fatalf("empty credentials must leave fingerprints empty: %q %q", pool, subject)
	}
	// Single-key form remains covered; empty entries are ignored.
	pool, _ = credentialMaterialFingerprints(testSecret, map[string]any{"api_key": " sk-a "})
	if pool == "" {
		t.Fatal("single api_key must produce a pool fingerprint")
	}
}
