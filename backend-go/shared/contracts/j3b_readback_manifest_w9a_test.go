package contracts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func w9aGoodManifest(now time.Time) J3bReadbackManifest {
	manifest := J3bReadbackManifest{
		FormatVersion:          J3bReadbackManifestFormatVersion,
		Scope:                  J3bReadbackManifestScope,
		Producer:               "maintenance-cli",
		SourceSnapshotIdentity: "snap-1",
		SourceSchema:           "legacy-sqlite-dataset+stats",
		TargetSchema:           "juhe-j3b-sqlite",
		ProjectionComplete:     true,
		VerifiedAt:             now.Add(-time.Minute).Format(time.RFC3339),
	}
	for _, name := range j3bReadbackRequiredTables {
		manifest.Tables = append(manifest.Tables, J3bReadbackTableDigest{
			Name: name, SourceRows: 3, TargetRows: 3,
			SourceDigest: w9aGoodDigest, TargetDigest: w9aGoodDigest,
		})
	}
	return manifest
}

func TestW9ADecodeReadbackManifest(t *testing.T) {
	data, err := json.Marshal(w9aGoodManifest(w9aNow()))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodeJ3bReadbackManifest(data)
	if err != nil || manifest.FormatVersion != J3bReadbackManifestFormatVersion {
		t.Fatalf("valid manifest decode failed: %+v err %v", manifest, err)
	}

	if _, err := DecodeJ3bReadbackManifest([]byte(`{"unknown":1}`)); err == nil {
		t.Fatal("unknown field must fail")
	}
	if _, err := DecodeJ3bReadbackManifest([]byte(`{`)); err == nil {
		t.Fatal("malformed JSON must fail")
	}
	if _, err := DecodeJ3bReadbackManifest(append(data, []byte(" {}")...)); err == nil || !strings.Contains(err.Error(), "trailing JSON data") {
		t.Fatalf("trailing JSON must fail: %v", err)
	}
	if _, err := DecodeJ3bReadbackManifest(append(data, []byte(" @")...)); err == nil {
		t.Fatal("trailing invalid JSON must surface decode error")
	}
}

func TestW9AComputeReadbackManifestHashStable(t *testing.T) {
	now := w9aNow()
	manifest := w9aGoodManifest(now)
	hash, err := ComputeJ3bReadbackManifestHash(manifest)
	if err != nil {
		t.Fatalf("compute hash: %v", err)
	}
	if len(hash) != 64 {
		t.Fatalf("hash length = %d, want 64", len(hash))
	}
	// Cleared ManifestHash must not affect the result.
	manifest.ManifestHash = hash
	again, err := ComputeJ3bReadbackManifestHash(manifest)
	if err != nil || again != hash {
		t.Fatalf("hash not stable with ManifestHash set: %v vs %v", again, hash)
	}
	// Table order must not affect the canonical hash.
	reordered := manifest
	reordered.Tables = []J3bReadbackTableDigest{manifest.Tables[2], manifest.Tables[0], manifest.Tables[1]}
	reordered.Tables = append(reordered.Tables, manifest.Tables[3:]...)
	shuffled, err := ComputeJ3bReadbackManifestHash(reordered)
	if err != nil || shuffled != hash {
		t.Fatalf("hash must be order-independent: %v vs %v", shuffled, hash)
	}
}

func TestW9AValidateReadbackManifestHappyPaths(t *testing.T) {
	now := w9aNow()
	manifest := w9aGoodManifest(now)
	hash, err := ComputeJ3bReadbackManifestHash(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestHash = hash
	if errs := ValidateJ3bReadbackManifest(manifest, now, 3600); len(errs) != 0 {
		t.Fatalf("good manifest must validate, got %v", errs)
	}
	// Manifest hash may carry the sha256: prefix.
	prefixed := manifest
	prefixed.ManifestHash = "SHA256:" + strings.ToUpper(hash)
	if errs := ValidateJ3bReadbackManifest(prefixed, now, 3600); len(errs) != 0 {
		t.Fatalf("prefixed manifest hash must validate, got %v", errs)
	}
	// Second supported schema pair.
	other := manifest
	other.SourceSchema, other.TargetSchema = "juhe_dataset+juhe_stats", "juhe_j3b"
	otherHash, err := ComputeJ3bReadbackManifestHash(other)
	if err != nil {
		t.Fatal(err)
	}
	other.ManifestHash = otherHash
	if errs := ValidateJ3bReadbackManifest(other, now, 3600); len(errs) != 0 {
		t.Fatalf("second schema pair must validate, got %v", errs)
	}
}

func TestW9AValidateReadbackManifestTopLevelErrors(t *testing.T) {
	now := w9aNow()
	report := ValidateJ3bReadbackManifest(J3bReadbackManifest{}, now, 3600)
	for _, expected := range []string{
		"formatVersion is unsupported",
		"scope is unsupported",
		"producer is required",
		"sourceSnapshotIdentity is required",
		"sourceSchema and targetSchema are required",
		"projectionComplete must be true",
		"manifestHash must be a SHA-256 digest",
	} {
		found := false
		for _, e := range report {
			if strings.Contains(e, expected) {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing error %q in %v", expected, report)
		}
	}

	// Manifest with a missing required table must be reported per table.
	missingAll := w9aGoodManifest(now)
	missingAll.Tables = nil
	errs := ValidateJ3bReadbackManifest(missingAll, now, 3600)
	found := 0
	for _, e := range errs {
		if strings.Contains(e, "required table") && strings.Contains(e, "is missing") {
			found++
		}
	}
	if found != len(j3bReadbackRequiredTables) {
		t.Fatalf("expected %d missing-table errors, got %d in %v", len(j3bReadbackRequiredTables), found, errs)
	}
}

func TestW9AValidateReadbackManifestFreshnessAndSchema(t *testing.T) {
	now := w9aNow()
	base := w9aGoodManifest(now)
	base.ManifestHash, _ = ComputeJ3bReadbackManifestHash(base)

	if errs := ValidateJ3bReadbackManifest(base, now, 0); !containsW9A(errs, "max age must be positive") {
		t.Fatalf("non-positive max age must fail: %v", errs)
	}
	if errs := ValidateJ3bReadbackManifest(base, time.Time{}, 3600); !containsW9A(errs, "validation time is required") {
		t.Fatalf("zero now must fail: %v", errs)
	}
	badTime := base
	badTime.VerifiedAt = "long ago"
	if errs := ValidateJ3bReadbackManifest(badTime, now, 3600); !containsW9A(errs, "verifiedAt must be RFC3339") {
		t.Fatalf("bad verifiedAt must fail: %v", errs)
	}
	future := base
	future.VerifiedAt = now.Add(time.Minute).Format(time.RFC3339)
	if errs := ValidateJ3bReadbackManifest(future, now, 3600); !containsW9A(errs, "expired or from the future") {
		t.Fatalf("future verifiedAt must fail: %v", errs)
	}
	expired := base
	expired.VerifiedAt = now.Add(-2 * time.Hour).Format(time.RFC3339)
	if errs := ValidateJ3bReadbackManifest(expired, now, 3600); !containsW9A(errs, "expired or from the future") {
		t.Fatalf("expired verifiedAt must fail: %v", errs)
	}
	unsupported := base
	unsupported.SourceSchema, unsupported.TargetSchema = "a", "b"
	if errs := ValidateJ3bReadbackManifest(unsupported, now, 3600); !containsW9A(errs, "do not match a supported J3b projection") {
		t.Fatalf("unsupported schema pair must fail: %v", errs)
	}
	wrongHash := base
	wrongHash.ManifestHash = strings.Repeat("ab", 32)
	if errs := ValidateJ3bReadbackManifest(wrongHash, now, 3600); !containsW9A(errs, "manifestHash does not match canonical content") {
		t.Fatalf("wrong manifest hash must fail: %v", errs)
	}
}

func TestW9AValidateReadbackManifestTableErrors(t *testing.T) {
	now := w9aNow()
	manifestWithHash := func() J3bReadbackManifest {
		manifest := w9aGoodManifest(now)
		manifest.ManifestHash, _ = ComputeJ3bReadbackManifestHash(manifest)
		return manifest
	}

	blankName := manifestWithHash()
	blankName.Tables = append(blankName.Tables, J3bReadbackTableDigest{Name: "   "})
	if errs := ValidateJ3bReadbackManifest(blankName, now, 3600); !containsW9A(errs, "table name is required") {
		t.Fatalf("blank table name must fail: %v", errs)
	}

	outside := manifestWithHash()
	outside.Tables = append(outside.Tables, J3bReadbackTableDigest{Name: "not_in_scope"})
	errs := ValidateJ3bReadbackManifest(outside, now, 3600)
	if !containsW9A(errs, "outside the fixed J3b legacy-facts scope") {
		t.Fatalf("outside table must fail: %v", errs)
	}

	duplicated := manifestWithHash()
	duplicated.Tables = append(duplicated.Tables, duplicated.Tables[0])
	if errs := ValidateJ3bReadbackManifest(duplicated, now, 3600); !containsW9A(errs, "is duplicated") {
		t.Fatalf("duplicated table must fail: %v", errs)
	}

	negative := manifestWithHash()
	negative.Tables[0].SourceRows = -1
	if errs := ValidateJ3bReadbackManifest(negative, now, 3600); !containsW9A(errs, "negative row count") {
		t.Fatalf("negative rows must fail: %v", errs)
	}

	differ := manifestWithHash()
	differ.Tables[0].TargetRows = 4
	if errs := ValidateJ3bReadbackManifest(differ, now, 3600); !containsW9A(errs, "row counts differ") {
		t.Fatalf("differing row counts must fail: %v", errs)
	}

	badDigest := manifestWithHash()
	badDigest.Tables[0].SourceDigest = "nope"
	if errs := ValidateJ3bReadbackManifest(badDigest, now, 3600); !containsW9A(errs, "digests must be SHA-256") {
		t.Fatalf("bad table digest must fail: %v", errs)
	}

	differentDigests := manifestWithHash()
	differentDigests.Tables[0].TargetDigest = "sha256:" + strings.Repeat("cd", 32)
	if errs := ValidateJ3bReadbackManifest(differentDigests, now, 3600); !containsW9A(errs, "digests differ") {
		t.Fatalf("differing table digests must fail: %v", errs)
	}

	missingTable := manifestWithHash()
	missingTable.Tables = missingTable.Tables[1:]
	if errs := ValidateJ3bReadbackManifest(missingTable, now, 3600); !containsW9A(errs, "required table account_quality_health_hourly is missing") {
		t.Fatalf("missing required table must fail: %v", errs)
	}
}

func containsW9A(list []string, needle string) bool {
	for _, item := range list {
		if strings.Contains(item, needle) {
			return true
		}
	}
	return false
}
