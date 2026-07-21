package accounthealthcheck

import "testing"

func TestPayloadRoundTripPreservesRevisionAndUniqueKey(t *testing.T) {
	payload := Task{AccountID: "acct-1", ConfigRevision: 7, UniqueKey: "account-health-check:acct-1:7"}
	encoded, err := Encode(payload)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if decoded != payload {
		t.Fatalf("decoded = %+v, want %+v", decoded, payload)
	}
}

func TestDecodeRejectsMissingUniqueKey(t *testing.T) {
	if _, err := Decode([]byte(`{"version":1,"accountId":"acct-1","configRevision":1}`)); err == nil {
		t.Fatal("Decode() error = nil, want validation error")
	}
}

func TestDecodeRejectsUniqueKeyForDifferentRevision(t *testing.T) {
	if _, err := Decode([]byte(`{"version":1,"accountId":"acct-1","configRevision":2,"uniqueKey":"acct-1:1"}`)); err == nil {
		t.Fatal("Decode() error = nil, want unique key mismatch")
	}
}
