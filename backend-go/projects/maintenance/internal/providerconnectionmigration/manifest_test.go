package providerconnectionmigration

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// manifest 模型 JSON 往返 + 脱敏契约测试。

func sampleManifest() Manifest {
	now := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	return Manifest{
		FormatVersion:       FormatVersion,
		CreatedAtUTC:        now.UTC().Format(time.RFC3339Nano),
		SourceSchemaVersion: SourceSchemaVersion,
		TargetSchemaVersion: TargetSchemaVersion,
		SourceSnapshotHash:  "6d1c2a51b7e6b0ef3f7d9f2f8f4e9b32a6d0c5b9e8f7a6d5c4b3a2f1e0d9c8b7",
		SourceTableRowCounts: map[string]int{
			"accounts": 2, "provider_model_catalog": 1, "usage_records": 7,
		},
		Rows: []RowDisposition{{
			SourceTable:  "accounts",
			SourcePK:     []string{"acc-1"},
			TargetTable:  "accounts",
			TargetPK:     []string{"acc-1"},
			TargetFields: map[string]string{"vendor_code": "gpt", "credential_fingerprint": "ab12"},
			Rule:         1,
			RuleNote:     "规则 1",
			Evidence: []Evidence{{
				SourceTable:  "accounts",
				SourcePK:     []string{"acc-1"},
				SourceFields: map[string]string{"provider_code": "cd34"},
				RetrievedAtUTC: now.UTC().Format(time.RFC3339Nano),
			}},
			Disposition: DispositionMapped,
			TargetIDs:   []string{"conn-ab"},
		}, {
			SourceTable:    "usage_records",
			SourcePK:       []string{},
			SourceRowCount: 7,
			Disposition:    DispositionBlocked,
			Rule:           11,
			Errors:         []string{"history_rows_unmatched"},
		}},
		GeneratedTargetIDs: []GeneratedTargetTable{{
			TargetTable: "account_connections",
			Reason:      "规则 1/2/3",
			IDs:         []string{"conn-ab"},
		}},
		BlockedCounts: BlockedCounts{AccountsBlocked: 1, HistoryRowsUnmatched: 7},
		PublicModelConflicts: []PublicModelConflict{{
			ResourceAccountID: "acc-1",
			WireModel:         "m",
			DirectModelIDs:    []string{"model-a", "model-b"},
			MappingIDs:        []string{"mapping-1"},
		}},
		HealthCheckBlocks: []HealthCheckBlock{{
			ResourceAccountID:      "acc-2",
			Reason:                 HealthReasonConnectionMany,
			CandidateConnectionIDs: []string{"conn-a", "conn-b"},
			CandidateModelIDs:      []string{"model-c"},
			SourceFieldsHash:       "ef56",
		}},
		Notes: []string{"切片边界"},
	}
}

func TestManifestJSONRoundTrip(t *testing.T) {
	original := sampleManifest()
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal manifest 失败: %v", err)
	}
	if !strings.Contains(string(encoded), `"format_version":1`) {
		t.Fatalf("format_version 必须固定为 1: %s", encoded)
	}
	var decoded Manifest
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal manifest 失败: %v", err)
	}
	reencoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("re-marshal manifest 失败: %v", err)
	}
	if string(encoded) != string(reencoded) {
		t.Fatalf("manifest JSON 往返不等值:\n first:  %s\n second: %s", encoded, reencoded)
	}
	if decoded.BlockedCounts != original.BlockedCounts {
		t.Fatalf("blocked counts 往返不符: %+v", decoded.BlockedCounts)
	}
	if len(decoded.Rows) != len(original.Rows) || decoded.Rows[0].Rule != 1 || decoded.Rows[1].SourceRowCount != 7 {
		t.Fatalf("rows 往返不符: %+v", decoded.Rows)
	}
}

func TestManifestJSONOmitemptySemantics(t *testing.T) {
	// source_row_count=0 与空 target 不应出现在 JSON（omitempty 语义）。
	encoded, err := json.Marshal(RowDisposition{SourceTable: "accounts", SourcePK: []string{"a"}, Disposition: DispositionMapped, Rule: 1})
	if err != nil {
		t.Fatalf("marshal 失败: %v", err)
	}
	for _, forbidden := range []string{"source_row_count", "target_table", "target_fields", "errors", "target_ids", "rule_note", "evidence"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("空字段 %s 不应序列化: %s", forbidden, encoded)
		}
	}
}
