package contracts

import (
	"strings"
	"testing"
	"time"
)

func validJ3bReadbackManifest(t *testing.T, now time.Time) J3bReadbackManifest {
	t.Helper()
	manifest := J3bReadbackManifest{
		FormatVersion: J3bReadbackManifestFormatVersion,
		Scope:         J3bReadbackManifestScope,
		Producer:      "test", SourceSnapshotIdentity: "snapshot-1",
		SourceSchema: "legacy-sqlite-dataset+stats", TargetSchema: "juhe-j3b-sqlite", ProjectionComplete: true,
		VerifiedAt: now.Format(time.RFC3339),
	}
	for _, name := range j3bReadbackRequiredTables {
		manifest.Tables = append(manifest.Tables, J3bReadbackTableDigest{
			Name: name, SourceRows: 1, TargetRows: 1,
			SourceDigest: strings.Repeat("a", 64), TargetDigest: strings.Repeat("a", 64),
		})
	}
	hash, err := ComputeJ3bReadbackManifestHash(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestHash = hash
	return manifest
}

func TestValidateJ3bReadbackManifestFailsClosed(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	manifest := validJ3bReadbackManifest(t, now)
	if errors := ValidateJ3bReadbackManifest(manifest, now, 60); len(errors) != 0 {
		t.Fatalf("valid manifest errors=%v", errors)
	}
	manifest.Tables = manifest.Tables[1:]
	if errors := ValidateJ3bReadbackManifest(manifest, now, 60); len(errors) == 0 {
		t.Fatal("missing table unexpectedly accepted")
	}
	manifest = validJ3bReadbackManifest(t, now)
	manifest.Tables[0].TargetDigest = strings.Repeat("b", 64)
	if errors := ValidateJ3bReadbackManifest(manifest, now, 60); len(errors) == 0 {
		t.Fatal("digest drift unexpectedly accepted")
	}
	manifest = validJ3bReadbackManifest(t, now)
	manifest.ProjectionComplete = false
	if errors := ValidateJ3bReadbackManifest(manifest, now, 60); len(errors) == 0 {
		t.Fatal("lossy projection unexpectedly accepted")
	}
	manifest = validJ3bReadbackManifest(t, now)
	manifest.ManifestHash = strings.Repeat("0", 64)
	if errors := ValidateJ3bReadbackManifest(manifest, now, 60); len(errors) == 0 {
		t.Fatal("bad aggregate hash unexpectedly accepted")
	}
	manifest = validJ3bReadbackManifest(t, now.Add(-2*time.Minute))
	if errors := ValidateJ3bReadbackManifest(manifest, now, 60); len(errors) == 0 {
		t.Fatal("expired manifest unexpectedly accepted")
	}
	manifest = validJ3bReadbackManifest(t, now)
	manifest.Scope = "wrong-scope"
	refreshJ3bReadbackManifestHash(t, &manifest)
	if errors := ValidateJ3bReadbackManifest(manifest, now, 60); len(errors) == 0 {
		t.Fatal("scope mismatch unexpectedly accepted")
	}
	manifest = validJ3bReadbackManifest(t, now)
	manifest.SourceSchema = "wrong-schema"
	refreshJ3bReadbackManifestHash(t, &manifest)
	if errors := ValidateJ3bReadbackManifest(manifest, now, 60); len(errors) == 0 {
		t.Fatal("schema mismatch unexpectedly accepted")
	}
	manifest = validJ3bReadbackManifest(t, now)
	manifest.Tables = append(manifest.Tables, J3bReadbackTableDigest{
		Name: "unapproved_legacy_fact", SourceRows: 1, TargetRows: 1,
		SourceDigest: strings.Repeat("a", 64), TargetDigest: strings.Repeat("a", 64),
	})
	refreshJ3bReadbackManifestHash(t, &manifest)
	if errors := ValidateJ3bReadbackManifest(manifest, now, 60); len(errors) == 0 {
		t.Fatal("out-of-scope table unexpectedly accepted")
	}
}

func refreshJ3bReadbackManifestHash(t *testing.T, manifest *J3bReadbackManifest) {
	t.Helper()
	manifest.ManifestHash = ""
	hash, err := ComputeJ3bReadbackManifestHash(*manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestHash = hash
}
