package contracts

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

var errW9AFake = errors.New("w9a fake verifier failure")

var w9aGoodDigest = "sha256:" + strings.Repeat("ab", 32)

func w9aNow() time.Time { return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) }

func w9aGoodEvidence(now time.Time) J3bCutoverEvidence {
	return J3bCutoverEvidence{
		OldOwner:             "node-gateway",
		NewOwner:             J3bGatewayCutoverOwner,
		OwnerEpoch:           "epoch-1",
		DrainCompleted:       true,
		ActivePathZero:       true,
		RollbackReplayCursor: "cursor-1",
		BackupArtifact:       J3bBackupArtifact{Path: "backup.tar.zst", Hash: w9aGoodDigest},
		SourceDigest:         w9aGoodDigest,
		TargetDigest:         w9aGoodDigest,
		ReadbackManifest: J3bReadbackManifestReference{
			Path:                   "manifest.json",
			Hash:                   w9aGoodDigest,
			FormatVersion:          J3bReadbackManifestFormatVersion,
			Scope:                  J3bReadbackManifestScope,
			SourceSnapshotIdentity: "snap-1",
			SourceSchema:           "legacy-sqlite-dataset+stats",
			TargetSchema:           "juhe-j3b-sqlite",
		},
		Freshness: J3bEvidenceFreshness{
			CapturedAt:    now.Add(-time.Minute).Format(time.RFC3339),
			MaxAgeSeconds: 3600,
		},
	}
}

func w9aOkVerifier(J3bBackupArtifact) error { return nil }

func w9aReportHas(t *testing.T, report J3bCutoverEvidenceReport, needle string) {
	t.Helper()
	for _, e := range report.Errors {
		if strings.Contains(e, needle) {
			return
		}
	}
	t.Fatalf("report missing error %q, got %v", needle, report.Errors)
}

func TestW9ADecodeCutoverEvidence(t *testing.T) {
	decoded, err := DecodeJ3bCutoverEvidence([]byte(`{"oldOwner":"node","newOwner":"go-gateway","inFlight":0}`))
	if err != nil {
		t.Fatalf("valid evidence: %v", err)
	}
	if decoded.OldOwner != "node" || decoded.NewOwner != "go-gateway" {
		t.Fatalf("decoded fields drifted: %+v", decoded)
	}
	if !decoded.inFlightSet {
		t.Fatal("inFlight 0 must be marked as set")
	}
	if !decoded.parsedFromJSON {
		t.Fatal("decode path must mark parsedFromJSON")
	}

	if _, err := DecodeJ3bCutoverEvidence([]byte(`{invalid`)); err == nil {
		t.Fatal("invalid JSON must fail")
	}
	if _, err := DecodeJ3bCutoverEvidence([]byte(`{} trailing`)); err == nil {
		t.Fatal("trailing invalid token must fail")
	}
	if _, err := DecodeJ3bCutoverEvidence([]byte(`{} {}`)); err == nil || !strings.Contains(err.Error(), "trailing JSON data") {
		t.Fatalf("trailing JSON value must fail: %v", err)
	}
	if _, err := DecodeJ3bCutoverEvidence([]byte(`{} @`)); err == nil {
		t.Fatal("trailing invalid JSON must fail with decode error")
	}
}

func TestW9AEvidenceUnmarshalJSON(t *testing.T) {
	var withCounters J3bCutoverEvidence
	if err := json.Unmarshal([]byte(`{"oldOwner":"n","inFlight":0,"blockedFindings":0}`), &withCounters); err != nil {
		t.Fatalf("explicit zero counters: %v", err)
	}
	if !withCounters.inFlightSet || !withCounters.blockedFindingsSet || !withCounters.parsedFromJSON {
		t.Fatalf("explicit zero counters must be marked set: %+v", withCounters)
	}

	var nullCounters J3bCutoverEvidence
	if err := json.Unmarshal([]byte(`{"inFlight":null,"blockedFindings":null}`), &nullCounters); err != nil {
		t.Fatalf("null counters: %v", err)
	}
	if nullCounters.inFlightSet || nullCounters.blockedFindingsSet {
		t.Fatal("null counters must not count as set")
	}

	if err := json.Unmarshal([]byte(`{"unknownField":1}`), &J3bCutoverEvidence{}); err == nil {
		t.Fatal("unknown fields must be rejected")
	}
	if err := json.Unmarshal([]byte(`{`), &J3bCutoverEvidence{}); err == nil {
		t.Fatal("malformed JSON must be rejected")
	}
}

func TestW9AValidateCutoverEvidenceHappyPath(t *testing.T) {
	now := w9aNow()
	good := w9aGoodEvidence(now)
	// The plain entry point has no readback verifier, so a complete manifest
	// reference always demands the WithReadback variant.
	w9aReportHas(t, ValidateJ3bCutoverEvidence(good, now, w9aOkVerifier), "readbackManifest verifier is required")

	report := ValidateJ3bCutoverEvidenceWithReadback(good, now, w9aOkVerifier,
		func(J3bReadbackManifestReference, J3bCutoverEvidence, time.Time) error { return nil })
	if !report.Ready || len(report.Errors) != 0 {
		t.Fatalf("good evidence must be ready, got %v", report.Errors)
	}
}

func TestW9AValidateCutoverEvidenceEmptyReportsAllStructuralErrors(t *testing.T) {
	report := ValidateJ3bCutoverEvidence(J3bCutoverEvidence{}, w9aNow(), nil)
	for _, expected := range []string{
		"oldOwner is required",
		"newOwner is required",
		"ownerEpoch is required",
		"drainCompleted must be true",
		"activePathZero must be true",
		"rollbackReplayCursor is required",
		"readbackManifest path and SHA-256 hash are required",
		"readbackManifest format, scope, snapshot identity and schemas are required",
		"backupArtifact.hash must be a SHA-256 digest",
		"backupArtifact.path is required",
		"freshness.maxAgeSeconds must be positive",
	} {
		w9aReportHas(t, report, expected)
	}
	if report.Ready {
		t.Fatal("empty evidence must not be ready")
	}
}

func TestW9AValidateCutoverEvidenceSemanticErrors(t *testing.T) {
	now := w9aNow()
	base := w9aGoodEvidence(now)

	sameOwner := base
	sameOwner.OldOwner = J3bGatewayCutoverOwner
	w9aReportHas(t, ValidateJ3bCutoverEvidence(sameOwner, now, w9aOkVerifier), "oldOwner and newOwner must differ")

	badNewOwner := base
	badNewOwner.NewOwner = "someone-else"
	w9aReportHas(t, ValidateJ3bCutoverEvidence(badNewOwner, now, w9aOkVerifier), "newOwner must be go-gateway")

	badSource := base
	badSource.SourceDigest = "deadbeef"
	w9aReportHas(t, ValidateJ3bCutoverEvidence(badSource, now, w9aOkVerifier), "sourceDigest must be a SHA-256 digest when provided")

	badTarget := base
	badTarget.TargetDigest = "sha256:zzzz"
	w9aReportHas(t, ValidateJ3bCutoverEvidence(badTarget, now, w9aOkVerifier), "targetDigest must be a SHA-256 digest when provided")

	badCursor := base
	badCursor.RollbackReplayCursor = "   "
	w9aReportHas(t, ValidateJ3bCutoverEvidence(badCursor, now, w9aOkVerifier), "rollbackReplayCursor is required")

	badManifest := base
	badManifest.ReadbackManifest.Hash = "not-a-digest"
	w9aReportHas(t, ValidateJ3bCutoverEvidence(badManifest, now, w9aOkVerifier), "readbackManifest path and SHA-256 hash are required")

	badFormat := base
	badFormat.ReadbackManifest.FormatVersion = "wrong"
	w9aReportHas(t, ValidateJ3bCutoverEvidence(badFormat, now, w9aOkVerifier), "readbackManifest format, scope, snapshot identity and schemas are required")
}

func TestW9AValidateCutoverEvidenceVerifiersAndCounters(t *testing.T) {
	now := w9aNow()
	base := w9aGoodEvidence(now)

	// Verifiers required.
	w9aReportHas(t, ValidateJ3bCutoverEvidence(base, now, nil), "backupArtifact verifier is required")
	w9aReportHas(t, ValidateJ3bCutoverEvidenceWithReadback(base, now, w9aOkVerifier, nil), "readbackManifest verifier is required")

	// Verifier failures surface their error text.
	failArtifact := func(J3bBackupArtifact) error { return errW9AFake }
	w9aReportHas(t, ValidateJ3bCutoverEvidence(base, now, failArtifact), "backupArtifact verification failed")
	failReadback := func(J3bReadbackManifestReference, J3bCutoverEvidence, time.Time) error { return errW9AFake }
	w9aReportHas(t, ValidateJ3bCutoverEvidenceWithReadback(base, now, w9aOkVerifier, failReadback), "readbackManifest verification failed")

	// WithReadback happy path.
	if report := ValidateJ3bCutoverEvidenceWithReadback(base, now, w9aOkVerifier, func(J3bReadbackManifestReference, J3bCutoverEvidence, time.Time) error { return nil }); !report.Ready {
		t.Fatalf("readback happy path must be ready, got %v", report.Errors)
	}

	// Missing counters via JSON decode must be rejected even when zero.
	var missing J3bCutoverEvidence
	if err := json.Unmarshal([]byte(`{"oldOwner":"n","newOwner":"go-gateway"}`), &missing); err != nil {
		t.Fatal(err)
	}
	w9aReportHas(t, ValidateJ3bCutoverEvidence(missing, now, w9aOkVerifier), "inFlight is required")
	w9aReportHas(t, ValidateJ3bCutoverEvidence(missing, now, w9aOkVerifier), "blockedFindings is required")

	// Non-zero explicit counters must be rejected.
	var nonzero J3bCutoverEvidence
	if err := json.Unmarshal([]byte(`{"inFlight":3,"blockedFindings":2}`), &nonzero); err != nil {
		t.Fatal(err)
	}
	w9aReportHas(t, ValidateJ3bCutoverEvidence(nonzero, now, w9aOkVerifier), "inFlight must be zero")
	w9aReportHas(t, ValidateJ3bCutoverEvidence(nonzero, now, w9aOkVerifier), "blockedFindings must be zero")
}

func TestW9AValidateCutoverEvidenceFreshnessBranches(t *testing.T) {
	now := w9aNow()
	base := w9aGoodEvidence(now)

	overflow := base
	overflow.Freshness.MaxAgeSeconds = math.MaxInt64
	w9aReportHas(t, ValidateJ3bCutoverEvidence(overflow, now, w9aOkVerifier), "freshness.maxAgeSeconds is out of range")

	badTime := base
	badTime.Freshness.CapturedAt = "yesterday"
	w9aReportHas(t, ValidateJ3bCutoverEvidence(badTime, now, w9aOkVerifier), "freshness.capturedAt must be RFC3339")

	zeroNow := base
	w9aReportHas(t, ValidateJ3bCutoverEvidence(zeroNow, time.Time{}, w9aOkVerifier), "validation time is required")

	future := base
	future.Freshness.CapturedAt = now.Add(time.Minute).Format(time.RFC3339)
	w9aReportHas(t, ValidateJ3bCutoverEvidence(future, now, w9aOkVerifier), "freshness evidence is expired or from the future")

	expired := base
	expired.Freshness.MaxAgeSeconds = 10
	w9aReportHas(t, ValidateJ3bCutoverEvidence(expired, now, w9aOkVerifier), "freshness evidence is expired or from the future")
}

func TestW9AValidateCutoverEvidenceForOwner(t *testing.T) {
	now := w9aNow()
	base := w9aGoodEvidence(now)

	w9aOkReadback := func(J3bReadbackManifestReference, J3bCutoverEvidence, time.Time) error { return nil }

	if report := ValidateJ3bCutoverEvidenceForOwnerWithReadback(base, J3bGatewayCutoverOwner, "epoch-1", now, w9aOkVerifier, w9aOkReadback); !report.Ready {
		t.Fatalf("matching owner/epoch must be ready, got %v", report.Errors)
	}
	// Plain entry points keep every other check but demand the readback
	// verifier for a complete manifest reference.
	plainReport := ValidateJ3bCutoverEvidenceForOwner(base, J3bGatewayCutoverOwner, "epoch-1", now, w9aOkVerifier)
	if len(plainReport.Errors) != 1 || !strings.Contains(plainReport.Errors[0], "readbackManifest verifier is required") {
		t.Fatalf("plain entry point must only miss the readback verifier, got %v", plainReport.Errors)
	}
	w9aReportHas(t, ValidateJ3bCutoverEvidenceForOwner(base, "other-owner", "", now, w9aOkVerifier), "newOwner does not match configured owner")
	w9aReportHas(t, ValidateJ3bCutoverEvidenceForOwner(base, "", "other-epoch", now, w9aOkVerifier), "ownerEpoch does not match configured epoch")
	if report := ValidateJ3bCutoverEvidenceForOwner(base, "  ", "", now, w9aOkVerifier); len(report.Errors) != 1 {
		t.Fatalf("blank owner/epoch must skip owner binding, got %v", report.Errors)
	}

	withReadback := ValidateJ3bCutoverEvidenceForOwnerWithReadback(base, J3bGatewayCutoverOwner, "epoch-1", now, w9aOkVerifier, w9aOkReadback)
	if !withReadback.Ready {
		t.Fatalf("owner+readback happy path must be ready, got %v", withReadback.Errors)
	}
	w9aReportHas(t, ValidateJ3bCutoverEvidenceForOwnerWithReadback(base, "mismatch", "epoch-1", now, w9aOkVerifier, w9aOkReadback), "newOwner does not match configured owner")
}

func TestW9ABackfillEvidenceEntryPoints(t *testing.T) {
	now := w9aNow()
	base := w9aGoodEvidence(now)
	okReadback := func(J3bReadbackManifestReference, J3bCutoverEvidence, time.Time) error { return nil }
	if report := ValidateJ3bBackfillEvidenceWithReadback(base, now, w9aOkVerifier, okReadback); !report.Ready {
		t.Fatalf("backfill+readback happy path must be ready, got %v", report.Errors)
	}
	// Plain backfill entry demands the readback verifier for a complete
	// manifest reference.
	w9aReportHas(t, ValidateJ3bBackfillEvidence(base, now, w9aOkVerifier), "readbackManifest verifier is required")
	w9aReportHas(t, ValidateJ3bBackfillEvidence(base, now, nil), "backupArtifact verifier is required")
}

func TestW9AValidEvidenceDigest(t *testing.T) {
	raw := strings.Repeat("ab", 32)
	if !validJ3bEvidenceDigest(raw) {
		t.Fatal("64 hex chars must validate")
	}
	if !validJ3bEvidenceDigest("SHA256:" + strings.ToUpper(raw)) {
		t.Fatal("sha256 prefix with upper case hex must validate")
	}
	if validJ3bEvidenceDigest("") || validJ3bEvidenceDigest("abc") {
		t.Fatal("wrong length must fail")
	}
	if validJ3bEvidenceDigest(strings.Repeat("zz", 32)) {
		t.Fatal("non-hex must fail")
	}
}

func TestW9AEqualEvidenceDigestNormalization(t *testing.T) {
	raw := strings.Repeat("cd", 32)
	if !equalJ3bEvidenceDigest("SHA256:"+strings.ToUpper(raw), "  "+raw+"  ") {
		t.Fatal("digest comparison must be prefix/case/space insensitive")
	}
	if equalJ3bEvidenceDigest(raw, strings.Repeat("ef", 32)) {
		t.Fatal("different digests must not compare equal")
	}
	if normalizeJ3bEvidenceDigest("  SHA256:AB ") != "ab" {
		t.Fatalf("normalize output drifted: %q", normalizeJ3bEvidenceDigest("  SHA256:AB "))
	}
}
