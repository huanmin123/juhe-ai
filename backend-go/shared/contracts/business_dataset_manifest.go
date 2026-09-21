package contracts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// BusinessDatasetManifestFormatVersion is the only accepted manifest
	// format version for the business dataset export/import tool.
	BusinessDatasetManifestFormatVersion = "business-dataset-manifest/v1"
	// BusinessDatasetManifestScope pins the manifest to the fixed 40-table
	// business whitelist (37 data tables + 3 structural-only tables).
	BusinessDatasetManifestScope = "business-40-tables-v1"
	// BusinessDatasetEmptyContentSha256 is the SHA-256 of zero bytes. It is
	// recorded for structural-only tables, whose asserted content is empty.
	BusinessDatasetEmptyContentSha256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// BusinessDatasetTables is the canonical 40-table business whitelist in
// canonical order. The first 37 tables carry exported data; the last 3 are
// structural-only tables whose rows are asserted to be empty on both export
// and import.
var BusinessDatasetTables = []string{
	// 1-37: data tables.
	"system_accounts",
	"providers",
	"protocols",
	"protocol_endpoint_families",
	"provider_protocol_profiles",
	"provider_protocol_profile_families",
	"provider_model_catalog",
	"custom_provider_models",
	"provider_default_health_check_models",
	"provider_system_default_health_check_models",
	"proxy_profiles",
	"model_quality_policies",
	"model_quality_schedules",
	"account_quality_enforcements",
	"account_name_search_terms",
	"account_name_search_documents",
	"account_supported_models",
	"account_model_mappings",
	"account_tags",
	"account_tag_bindings",
	"system_teams",
	"system_team_members",
	"resource_authorizations",
	"resource_authorization_sources",
	"resource_authorization_grants",
	"accounts",
	"groups",
	"group_authorization_settings",
	"group_accounts",
	"route_strategies",
	"route_strategy_groups",
	"response_inspection_policies",
	"global_settings",
	"system_settings",
	"request_quota_hourly_window_configs",
	"request_quota_hourly_window_scope_bindings",
	"api_keys",
	// 38-40: structural-only tables (verified empty, no data file).
	"group_account_stats_dirty",
	"account_schedule_status_events",
	"api_key_schedule_status_events",
}

// BusinessDatasetStructuralOnlyTables is the set of whitelist tables that are
// never exported as data files; export and import only assert they are empty.
var BusinessDatasetStructuralOnlyTables = []string{
	"group_account_stats_dirty",
	"account_schedule_status_events",
	"api_key_schedule_status_events",
}

// BusinessDatasetTableNameSet returns the whitelist as a set keyed by name.
func BusinessDatasetTableNameSet() map[string]struct{} {
	set := make(map[string]struct{}, len(BusinessDatasetTables))
	for _, name := range BusinessDatasetTables {
		set[name] = struct{}{}
	}
	return set
}

// BusinessDatasetStructuralOnlyNameSet returns the structural-only subset as
// a set keyed by name.
func BusinessDatasetStructuralOnlyNameSet() map[string]struct{} {
	set := make(map[string]struct{}, len(BusinessDatasetStructuralOnlyTables))
	for _, name := range BusinessDatasetStructuralOnlyTables {
		set[name] = struct{}{}
	}
	return set
}

// IsBusinessDatasetStructuralOnlyTable reports whether the named table is one
// of the three structural-only whitelist tables.
func IsBusinessDatasetStructuralOnlyTable(name string) bool {
	_, ok := BusinessDatasetStructuralOnlyNameSet()[name]
	return ok
}

// BusinessDatasetTableEntry records the exported evidence of one whitelist
// table. Sha256 is the SHA-256 of the table's canonical JSONL content (the
// exact bytes of its data file); for structural-only tables it is always
// BusinessDatasetEmptyContentSha256 and no data file is written.
type BusinessDatasetTableEntry struct {
	Name   string `json:"name"`
	File   string `json:"file"`
	Rows   int64  `json:"rows"`
	Sha256 string `json:"sha256"`
}

// BusinessDatasetManifest is a canonical, versioned assertion of one business
// dataset directory: what was exported from the source database, per-table row
// counts and content digests. Its ManifestHash is the SHA-256 of its canonical
// JSON form with ManifestHash cleared and Tables sorted by name, avoiding a
// self-referential file hash.
type BusinessDatasetManifest struct {
	FormatVersion  string                      `json:"formatVersion"`
	Scope          string                      `json:"scope"`
	Producer       string                      `json:"producer"`
	SourceIdentity string                      `json:"sourceIdentity"`
	TargetIdentity string                      `json:"targetIdentity"`
	CapturedAt     string                      `json:"capturedAt"`
	Tables         []BusinessDatasetTableEntry `json:"tables"`
	ManifestHash   string                      `json:"manifestHash"`
}

// DecodeBusinessDatasetManifest rejects unknown and trailing JSON data so an
// import cannot silently ignore a malformed or expanded manifest.
func DecodeBusinessDatasetManifest(data []byte) (BusinessDatasetManifest, error) {
	var manifest BusinessDatasetManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return BusinessDatasetManifest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return BusinessDatasetManifest{}, fmt.Errorf("trailing JSON data")
	} else if err != io.EOF {
		return BusinessDatasetManifest{}, err
	}
	return manifest, nil
}

// ComputeBusinessDatasetManifestHash returns the stable hash embedded in a
// business dataset manifest. It is pure and does not require a filesystem
// artifact: ManifestHash is cleared and tables are sorted by name before the
// canonical JSON form is hashed with SHA-256.
func ComputeBusinessDatasetManifestHash(manifest BusinessDatasetManifest) (string, error) {
	canonical := manifest
	canonical.ManifestHash = ""
	canonical.Tables = append([]BusinessDatasetTableEntry(nil), manifest.Tables...)
	sort.Slice(canonical.Tables, func(i, j int) bool { return canonical.Tables[i].Name < canonical.Tables[j].Name })
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

var businessDatasetSha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidateBusinessDatasetManifest validates the local, self-consistent shape
// of a business dataset manifest: format/scope/producer/identity fields, an
// RFC3339 CapturedAt, a self-consistent ManifestHash, unique table names, full
// coverage of the fixed 40-table whitelist with no extras, structural-only
// entries asserting zero rows, and SHA-256 digests everywhere. It proves
// nothing about any live database or directory; import-side catalog, file and
// transaction checks are separate.
func ValidateBusinessDatasetManifest(manifest BusinessDatasetManifest) []string {
	var errors []string
	add := func(message string) { errors = append(errors, message) }
	if manifest.FormatVersion != BusinessDatasetManifestFormatVersion {
		add("business dataset manifest formatVersion is unsupported")
	}
	if manifest.Scope != BusinessDatasetManifestScope {
		add("business dataset manifest scope is unsupported")
	}
	if strings.TrimSpace(manifest.Producer) == "" {
		add("business dataset manifest producer is required")
	}
	if strings.TrimSpace(manifest.SourceIdentity) == "" {
		add("business dataset manifest sourceIdentity is required")
	}
	if strings.TrimSpace(manifest.TargetIdentity) == "" {
		add("business dataset manifest targetIdentity is required")
	}
	if strings.TrimSpace(manifest.CapturedAt) == "" {
		add("business dataset manifest capturedAt is required")
	} else if _, err := time.Parse(time.RFC3339, strings.TrimSpace(manifest.CapturedAt)); err != nil {
		add("business dataset manifest capturedAt must be RFC3339")
	}
	if !businessDatasetSha256Pattern.MatchString(strings.TrimSpace(manifest.ManifestHash)) {
		add("business dataset manifest manifestHash must be a lowercase SHA-256 digest")
	} else if actual, err := ComputeBusinessDatasetManifestHash(manifest); err != nil {
		add(fmt.Sprintf("compute business dataset manifest hash: %v", err))
	} else if actual != strings.TrimSpace(manifest.ManifestHash) {
		add("business dataset manifest manifestHash does not match canonical content")
	}

	whitelist := BusinessDatasetTableNameSet()
	structuralOnly := BusinessDatasetStructuralOnlyNameSet()
	seen := make(map[string]struct{}, len(manifest.Tables))
	for _, table := range manifest.Tables {
		name := strings.TrimSpace(table.Name)
		if name == "" {
			add("business dataset manifest table name is required")
			continue
		}
		if _, required := whitelist[name]; !required {
			add(fmt.Sprintf("business dataset manifest table %s is outside the fixed business 40-table scope", name))
			continue
		}
		if _, exists := seen[name]; exists {
			add(fmt.Sprintf("business dataset manifest table %s is duplicated", name))
			continue
		}
		seen[name] = struct{}{}
		if table.Rows < 0 {
			add(fmt.Sprintf("business dataset manifest table %s has negative row count", name))
		}
		if _, structural := structuralOnly[name]; structural {
			if table.Rows != 0 {
				add(fmt.Sprintf("business dataset manifest structural-only table %s must assert zero rows", name))
			}
			if strings.TrimSpace(table.Sha256) != BusinessDatasetEmptyContentSha256 {
				add(fmt.Sprintf("business dataset manifest structural-only table %s must assert the empty-content digest", name))
			}
			continue
		}
		if strings.TrimSpace(table.File) == "" {
			add(fmt.Sprintf("business dataset manifest table %s is missing its data file name", name))
		}
		if !businessDatasetSha256Pattern.MatchString(strings.TrimSpace(table.Sha256)) {
			add(fmt.Sprintf("business dataset manifest table %s sha256 must be a lowercase SHA-256 digest", name))
		}
	}
	for _, required := range BusinessDatasetTables {
		if _, exists := seen[required]; !exists {
			add(fmt.Sprintf("business dataset manifest required table %s is missing", required))
		}
	}
	return errors
}
