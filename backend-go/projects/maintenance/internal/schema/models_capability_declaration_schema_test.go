// models 能力声明三列 schema 测试（T82 生产缺陷根治：personal custom 目录行
// 的能力矩阵恒空，AI 对话主模型不注入任何内部工具——单表显式能力声明列
// supported_tools_by_protocol_json / input_modalities_json / output_modalities_json）：
// PG 建表 DDL 行与存量库幂等 ALTER 组的文本契约钉死（仿
// capability_domain_schema_test.go / expedited_recovery_schema_test.go 模式）。

package schema

import (
	"strings"
	"testing"
)

// TestPostgresModelsCapabilityDeclarationSchemaContract pins the models
// capability-declaration columns along the capability-domain precedent: the
// CREATE TABLE declaration lines (placed with the other capability JSON
// columns), the paired jsonb_typeof CHECK lines, and the three idempotent
// legacy ALTER guard statements.
func TestPostgresModelsCapabilityDeclarationSchemaContract(t *testing.T) {
	modelsDDL := findPostgresStatementContaining(t, "CREATE TABLE IF NOT EXISTS models (")

	wantLines := []string{
		"      supported_tools_by_protocol_json text NOT NULL DEFAULT '{}',",
		"      input_modalities_json text NOT NULL DEFAULT '[]',",
		"      output_modalities_json text NOT NULL DEFAULT '[]',",
	}
	for _, want := range wantLines {
		if line := pgColumnDeclarationLine(t, modelsDDL, strings.Fields(strings.TrimSpace(want))[0]); line != want {
			t.Fatalf("models capability declaration line drifted:\n got %q\nwant %q", line, want)
		}
	}

	// 列位置契约：三列与既有 capability JSON 列相邻——排在
	// supported_reasoning_efforts_json 之后、default_reasoning_effort 之前。
	effortsIndex := strings.Index(modelsDDL, "supported_reasoning_efforts_json text NOT NULL")
	firstDeclaration := strings.Index(modelsDDL, "supported_tools_by_protocol_json text NOT NULL")
	defaultEffortIndex := strings.Index(modelsDDL, "default_reasoning_effort text")
	if effortsIndex < 0 || firstDeclaration < effortsIndex {
		t.Fatalf("supported_tools_by_protocol_json must sit next to supported_reasoning_efforts_json (efforts at %d, tools at %d)", effortsIndex, firstDeclaration)
	}
	lastDeclaration := strings.Index(modelsDDL, "output_modalities_json text NOT NULL")
	if defaultEffortIndex < 0 || lastDeclaration < firstDeclaration || lastDeclaration > defaultEffortIndex {
		t.Fatalf("output_modalities_json must sit before default_reasoning_effort (output at %d, default effort at %d)", lastDeclaration, defaultEffortIndex)
	}

	// CHECK 段契约：三条 jsonb_typeof CHECK 与既有 JSON CHECK 同段且按列序
	// 排列（object / array / array）。
	wantChecks := []string{
		"CHECK (jsonb_typeof(supported_tools_by_protocol_json::jsonb) = 'object')",
		"CHECK (jsonb_typeof(input_modalities_json::jsonb) = 'array')",
		"CHECK (jsonb_typeof(output_modalities_json::jsonb) = 'array')",
	}
	prevIndex := strings.Index(modelsDDL, "CHECK (jsonb_typeof(supported_reasoning_efforts_json::jsonb) = 'array')")
	if prevIndex < 0 {
		t.Fatal("supported_reasoning_efforts_json array CHECK missing from models DDL")
	}
	for _, want := range wantChecks {
		index := strings.Index(modelsDDL, want)
		if index < 0 {
			t.Fatalf("models capability CHECK missing: %s", want)
		}
		if index < prevIndex {
			t.Fatalf("models capability CHECK out of order: %s", want)
		}
		prevIndex = index
	}
	codexIndex := strings.Index(modelsDDL, "CHECK (jsonb_typeof(codex_supported_reasoning_levels_json::jsonb) = 'array')")
	if codexIndex < 0 || codexIndex < prevIndex {
		t.Fatal("capability CHECKs must sit before the codex reasoning-levels CHECK")
	}

	// 存量库幂等 ALTER 组（models-capability-declaration-pg-columns，单 Source
	// 三条语句，media-stats-pg-columns 先例）：必须全部在位且逐字一致——
	// CREATE TABLE 只救新库，缺它时存量 PG 库启动契约校验缺列 fail-closed。
	wantAlters := []string{
		`ALTER TABLE models ADD COLUMN IF NOT EXISTS supported_tools_by_protocol_json text NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(supported_tools_by_protocol_json::jsonb) = 'object')`,
		`ALTER TABLE models ADD COLUMN IF NOT EXISTS input_modalities_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(input_modalities_json::jsonb) = 'array')`,
		`ALTER TABLE models ADD COLUMN IF NOT EXISTS output_modalities_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(output_modalities_json::jsonb) = 'array')`,
	}
	alterCount := 0
	for _, statement := range postgresSchemaStatements {
		if statement.Source != "models-capability-declaration-pg-columns" {
			continue
		}
		if alterCount >= len(wantAlters) {
			t.Fatalf("unexpected extra statement under models-capability-declaration-pg-columns: %.120s", statement.SQL)
		}
		if statement.SQL != wantAlters[alterCount] {
			t.Fatalf("models capability declaration ALTER %d drifted:\n got %q\nwant %q", alterCount, statement.SQL, wantAlters[alterCount])
		}
		alterCount++
	}
	if alterCount != len(wantAlters) {
		t.Fatalf("models-capability-declaration-pg-columns ALTER count = %d, want %d", alterCount, len(wantAlters))
	}
}
