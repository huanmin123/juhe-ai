package contracts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestJ3bCutoverEvidenceRejectsUnknownAndMissingZeroFields(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	evidence := J3bCutoverEvidence{
		OldOwner: "node", NewOwner: J3bGatewayCutoverOwner, OwnerEpoch: "epoch-1", DrainCompleted: true,
		InFlight: 0, ActivePathZero: true, BlockedFindings: 0,
		BackupArtifact:       J3bBackupArtifact{Path: "backup", Hash: strings.Repeat("a", 64)},
		RollbackReplayCursor: "cursor", SourceDigest: strings.Repeat("b", 64), TargetDigest: strings.Repeat("b", 64),
		Freshness: J3bEvidenceFreshness{CapturedAt: now.Format(time.RFC3339), MaxAgeSeconds: 60},
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeJ3bCutoverEvidence(data)
	if err != nil {
		t.Fatal(err)
	}
	report := ValidateJ3bCutoverEvidenceForOwner(decoded, J3bGatewayCutoverOwner, "epoch-1", now, func(J3bBackupArtifact) error { return nil })
	if !report.Ready {
		t.Fatalf("valid evidence report=%+v", report)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "inFlight")
	data, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = DecodeJ3bCutoverEvidence(data)
	if err != nil {
		t.Fatal(err)
	}
	report = ValidateJ3bCutoverEvidenceForOwner(decoded, J3bGatewayCutoverOwner, "epoch-1", now, func(J3bBackupArtifact) error { return nil })
	if report.Ready {
		t.Fatalf("missing inFlight unexpectedly ready: %+v", report)
	}
	fields["unexpected"] = true
	data, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeJ3bCutoverEvidence(data); err == nil {
		t.Fatal("unknown field unexpectedly decoded")
	}
}
