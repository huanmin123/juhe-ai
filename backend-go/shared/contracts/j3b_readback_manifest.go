package contracts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

const (
	J3bReadbackManifestFormatVersion = "j3b-readback-manifest/v1"
	J3bReadbackManifestScope         = "j3b-legacy-facts-v1"
)

var j3bReadbackRequiredTables = []string{
	"account_quality_health_hourly",
	"model_check_items",
	"model_check_observations",
	"model_check_runs",
	"model_token_intercept_baseline_versions",
}

// J3bReadbackManifestReference binds cutover evidence to an independently
// written readback manifest. Hash is the SHA-256 of the manifest file bytes.
type J3bReadbackManifestReference struct {
	Path                   string `json:"path"`
	Hash                   string `json:"hash"`
	FormatVersion          string `json:"formatVersion"`
	Scope                  string `json:"scope"`
	SourceSnapshotIdentity string `json:"sourceSnapshotIdentity"`
	SourceSchema           string `json:"sourceSchema"`
	TargetSchema           string `json:"targetSchema"`
}

// J3bReadbackManifest is a canonical, versioned readback assertion. Its
// ManifestHash is the SHA-256 of its canonical JSON form with ManifestHash
// cleared, avoiding a self-referential file hash.
type J3bReadbackManifest struct {
	FormatVersion          string                   `json:"formatVersion"`
	Scope                  string                   `json:"scope"`
	Producer               string                   `json:"producer"`
	SourceSnapshotIdentity string                   `json:"sourceSnapshotIdentity"`
	SourceSchema           string                   `json:"sourceSchema"`
	TargetSchema           string                   `json:"targetSchema"`
	ProjectionComplete     bool                     `json:"projectionComplete"`
	VerifiedAt             string                   `json:"verifiedAt"`
	Tables                 []J3bReadbackTableDigest `json:"tables"`
	ManifestHash           string                   `json:"manifestHash"`
}

type J3bReadbackTableDigest struct {
	Name         string `json:"name"`
	SourceRows   int64  `json:"sourceRows"`
	TargetRows   int64  `json:"targetRows"`
	SourceDigest string `json:"sourceDigest"`
	TargetDigest string `json:"targetDigest"`
}

// DecodeJ3bReadbackManifest rejects unknown and trailing JSON data so a
// cutover cannot silently ignore a malformed or expanded manifest.
func DecodeJ3bReadbackManifest(data []byte) (J3bReadbackManifest, error) {
	var manifest J3bReadbackManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return J3bReadbackManifest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return J3bReadbackManifest{}, fmt.Errorf("trailing JSON data")
	} else if err != io.EOF {
		return J3bReadbackManifest{}, err
	}
	return manifest, nil
}

// ComputeJ3bReadbackManifestHash returns the stable hash embedded in a
// manifest. It is pure and does not require a filesystem artifact.
func ComputeJ3bReadbackManifestHash(manifest J3bReadbackManifest) (string, error) {
	canonical := manifest
	canonical.ManifestHash = ""
	canonical.Tables = append([]J3bReadbackTableDigest(nil), manifest.Tables...)
	sort.Slice(canonical.Tables, func(i, j int) bool { return canonical.Tables[i].Name < canonical.Tables[j].Name })
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// ValidateJ3bReadbackManifest validates the local, self-consistent evidence
// shape. It proves neither a live database snapshot nor backup recovery.
func ValidateJ3bReadbackManifest(manifest J3bReadbackManifest, now time.Time, maxAgeSeconds int64) []string {
	var errors []string
	add := func(message string) { errors = append(errors, message) }
	if manifest.FormatVersion != J3bReadbackManifestFormatVersion {
		add("readback manifest formatVersion is unsupported")
	}
	if manifest.Scope != J3bReadbackManifestScope {
		add("readback manifest scope is unsupported")
	}
	if strings.TrimSpace(manifest.Producer) == "" {
		add("readback manifest producer is required")
	}
	if strings.TrimSpace(manifest.SourceSnapshotIdentity) == "" {
		add("readback manifest sourceSnapshotIdentity is required")
	}
	if strings.TrimSpace(manifest.SourceSchema) == "" || strings.TrimSpace(manifest.TargetSchema) == "" {
		add("readback manifest sourceSchema and targetSchema are required")
	} else if !((manifest.SourceSchema == "legacy-sqlite-dataset+stats" && manifest.TargetSchema == "juhe-j3b-sqlite") || (manifest.SourceSchema == "juhe_dataset+juhe_stats" && manifest.TargetSchema == "juhe_j3b")) {
		add("readback manifest sourceSchema and targetSchema do not match a supported J3b projection")
	}
	if !manifest.ProjectionComplete {
		add("readback manifest projectionComplete must be true")
	}
	if maxAgeSeconds <= 0 {
		add("readback manifest max age must be positive")
	} else if now.IsZero() {
		add("readback manifest validation time is required")
	} else if verifiedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(manifest.VerifiedAt)); err != nil {
		add("readback manifest verifiedAt must be RFC3339")
	} else if verifiedAt.After(now) || now.Sub(verifiedAt) > time.Duration(maxAgeSeconds)*time.Second {
		add("readback manifest is expired or from the future")
	}
	if !validJ3bEvidenceDigest(manifest.ManifestHash) {
		add("readback manifest manifestHash must be a SHA-256 digest")
	} else if actual, err := ComputeJ3bReadbackManifestHash(manifest); err != nil {
		add(fmt.Sprintf("compute readback manifest hash: %v", err))
	} else if !equalJ3bEvidenceDigest(actual, manifest.ManifestHash) {
		add("readback manifest manifestHash does not match canonical content")
	}
	tables := make(map[string]J3bReadbackTableDigest, len(manifest.Tables))
	for _, table := range manifest.Tables {
		name := strings.TrimSpace(table.Name)
		if name == "" {
			add("readback manifest table name is required")
			continue
		}
		if _, exists := tables[name]; exists {
			add(fmt.Sprintf("readback manifest table %s is duplicated", name))
			continue
		}
		tables[name] = table
		if table.SourceRows < 0 || table.TargetRows < 0 {
			add(fmt.Sprintf("readback manifest table %s has negative row count", name))
		}
		if table.SourceRows != table.TargetRows {
			add(fmt.Sprintf("readback manifest table %s row counts differ", name))
		}
		if !validJ3bEvidenceDigest(table.SourceDigest) || !validJ3bEvidenceDigest(table.TargetDigest) {
			add(fmt.Sprintf("readback manifest table %s digests must be SHA-256", name))
		} else if !equalJ3bEvidenceDigest(table.SourceDigest, table.TargetDigest) {
			add(fmt.Sprintf("readback manifest table %s digests differ", name))
		}
	}
	for _, required := range j3bReadbackRequiredTables {
		if _, exists := tables[required]; !exists {
			add(fmt.Sprintf("readback manifest required table %s is missing", required))
		}
	}
	return errors
}
