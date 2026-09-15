package ownermanifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件用合成 TypeScript 源与合成仓库根驱动三个校验器的全部分支矩阵：
// owner manifest（Verify）、capability manifest（VerifyCapabilityManifest）、
// gateway route manifest（VerifyGatewayRouteOwnerManifest）。既有测试以真实
// 仓库为基线，这里补齐错误分支与“合成合法基线”的正反双向断言。

// ---- owner manifest fixtures ----

type wmOwnerFixture struct {
	manifestPath string
	typesPath    string
	accessPath   string
	handlerPath  string
}

func wmWrite(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const wmValidTypes = `import x from 'y'
export type DbServiceOperation =
 | { type: 'list_accounts' }
 | { type: 'update_account' }
 | { type: 'status' }
export type Other = never
`

const wmValidAccess = `list_accounts: 'read',
update_account: 'write',
status: 'runtime',
project_account_health_jobs_outcome: 'read',
`

const wmValidHandlers = `switch (op) {
 case 'list_accounts':
 case 'update_account':
 case 'status':
}
const fix = "update_account"
`

func wmValidOperations() []Operation {
	return []Operation{
		{Operation: "list_accounts", Access: "read", Tables: "t1", Transaction: "read-only", CurrentOwner: "node", TargetOwner: "read-consumer", Rollback: "r", Verification: "v",
			Source: Source{TypeUnion: "backend/src/modules/db-service/db-service-types.ts", Handler: "backend/src/modules/db-service/db-service-handlers.ts", TypeLine: 3, AccessModeLine: 1, HandlerLine: 2, Entrypoint: "db-service-handler", WriterKind: "read-consumer"}},
		{Operation: "update_account", Access: "write", Tables: "t1", Transaction: "accounts", CurrentOwner: "node", TargetOwner: "go-gateway", Rollback: "r", Verification: "v",
			Source: Source{TypeUnion: "backend/src/modules/db-service/db-service-types.ts", Handler: "backend/src/modules/db-service/db-service-handlers.ts", TypeLine: 4, AccessModeLine: 2, HandlerLine: 3, Entrypoint: "db-service-handler", WriterKind: "business-writer"}},
		{Operation: "status", Access: "read", Tables: "t1", Transaction: "read-only", CurrentOwner: "node", TargetOwner: "read-consumer", Rollback: "r", Verification: "v",
			Source: Source{TypeUnion: "backend/src/modules/db-service/db-service-types.ts", Handler: "backend/src/modules/db-service/db-service-handlers.ts", TypeLine: 5, AccessModeLine: 3, HandlerLine: 4, Entrypoint: "db-service-handler", WriterKind: "read-consumer"}},
	}
}

func wmWriteOwnerFixture(t *testing.T, mutate func([]Operation) []Operation) wmOwnerFixture {
	t.Helper()
	dir := t.TempDir()
	valid := wmValidOperations()
	if mutate != nil {
		valid = mutate(valid)
	}
	operations := valid
	manifest := map[string]any{
		"manifest_version": 1,
		"operation_source_contract": map[string]any{
			"path": "db-service-types.ts", "range": "2-5",
			"handler_path": "db-service-handlers.ts", "access_mode_path": "db-service-operation-access-mode.ts",
			"cutover_epoch_required": true, "drain_required": true, "rollback_requires_stop_and_replay": true,
		},
		"operations": operations,
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return wmOwnerFixture{
		manifestPath: wmWrite(t, dir, "manifest.json", string(data)),
		typesPath:    wmWrite(t, dir, "types.ts", wmValidTypes),
		accessPath:   wmWrite(t, dir, "access.ts", wmValidAccess),
		handlerPath:  wmWrite(t, dir, "handlers.ts", wmValidHandlers),
	}
}

func TestWMVerifyAcceptsSyntheticBaseline(t *testing.T) {
	fixture := wmWriteOwnerFixture(t, func(ops []Operation) []Operation { return ops })
	report, err := Verify(fixture.manifestPath, fixture.typesPath, fixture.accessPath, fixture.handlerPath)
	if err != nil {
		t.Fatalf("合成合法基线必须通过: %v", err)
	}
	if report.Reads != 2 || report.Writes != 1 || report.HandlerMatches != 3 {
		t.Fatalf("计数不匹配: %+v", report)
	}
	if report.SourceRuntime != 1 || report.TransactionGroups != 2 {
		t.Fatalf("来源/事务覆盖不匹配: %+v", report)
	}
	if report.WriterCoverage["read-consumer"] != 2 || report.WriterCoverage["business-writer"] != 1 {
		t.Fatalf("writer 覆盖不匹配: %+v", report)
	}
}

func TestWMVerifyRejectsInvalidInputs(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name     string
		mutate   func([]Operation) []Operation
		fragment string
	}{
		{
			name:     "empty operation name",
			mutate:   func(ops []Operation) []Operation { ops[0].Operation = "  "; return ops },
			fragment: "empty operation",
		},
		{
			name: "duplicate operation",
			mutate: func(ops []Operation) []Operation {
				ops[1].Operation = ops[0].Operation
				return ops
			},
			fragment: "duplicate operation",
		},
		{
			name: "operation missing from access map",
			mutate: func(ops []Operation) []Operation {
				ops[0].Operation = "unknown_read"
				return ops
			},
			fragment: "missing from access map",
		},
		{
			name: "operation missing from types",
			mutate: func(ops []Operation) []Operation {
				// access map 由 fixture 静态提供，改名单不生效 → 直接命中 types 缺失。
				ops[2].Operation = "project_account_health_jobs_outcome"
				ops[2].Access = "read"
				return ops
			},
			fragment: "missing from DbServiceOperation",
		},
		{
			name: "read operation declared as write",
			mutate: func(ops []Operation) []Operation {
				ops[0].Access = "write"
				return ops
			},
			fragment: `access="write"`,
		},
		{
			name: "mutating operation declared as read",
			mutate: func(ops []Operation) []Operation {
				ops[1].Access = "read"
				return ops
			},
			fragment: "mutating access",
		},
		{
			name: "missing metadata",
			mutate: func(ops []Operation) []Operation {
				ops[1].Tables = ""
				return ops
			},
			fragment: "lacks owner, table",
		},
		{
			name: "transaction mixes current owners",
			mutate: func(ops []Operation) []Operation {
				// list_accounts 与 status 同组，改其一的 current owner。
				ops[2].CurrentOwner = "go-gateway"
				ops[2].Source.WriterKind = "read-consumer"
				return ops
			},
			fragment: "mixes current owners",
		},
		{
			name: "transaction mixes target owners",
			mutate: func(ops []Operation) []Operation {
				ops[2].TargetOwner = "go-gateway"
				ops[2].Access = "write"
				ops[2].Source.WriterKind = "business-writer"
				return ops
			},
			fragment: "mixes target owners",
		},
		{
			name: "missing source metadata",
			mutate: func(ops []Operation) []Operation {
				ops[1].Source.TypeUnion = ""
				return ops
			},
			fragment: "lacks source location",
		},
		{
			name: "unsupported source path",
			mutate: func(ops []Operation) []Operation {
				ops[1].Source.TypeUnion = "backend/src/other.ts"
				return ops
			},
			fragment: "unsupported source or entrypoint",
		},
		{
			name: "unsupported writer kind",
			mutate: func(ops []Operation) []Operation {
				ops[1].Source.WriterKind = "side-writer"
				return ops
			},
			fragment: "unsupported writer kind",
		},
		{
			name: "access and writer kind disagree",
			mutate: func(ops []Operation) []Operation {
				ops[0].Source.WriterKind = "business-writer"
				return ops
			},
			fragment: "access and writer kind disagree",
		},
		{
			name: "stale type line",
			mutate: func(ops []Operation) []Operation {
				ops[1].Source.TypeLine = 2
				return ops
			},
			fragment: "type line 2 is stale",
		},
		{
			name: "stale access-mode line",
			mutate: func(ops []Operation) []Operation {
				ops[1].Source.AccessModeLine = 1
				return ops
			},
			fragment: "access-mode line 1 is stale",
		},
		{
			name: "stale handler line",
			mutate: func(ops []Operation) []Operation {
				ops[1].Source.HandlerLine = 1
				return ops
			},
			fragment: "handler line 1 is stale",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := wmWriteOwnerFixture(t, test.mutate)
			_, err := Verify(fixture.manifestPath, fixture.typesPath, fixture.accessPath, fixture.handlerPath)
			if err == nil || !strings.Contains(err.Error(), test.fragment) {
				t.Fatalf("期望错误包含 %q，实际: %v", test.fragment, err)
			}
		})
	}

	// runtime 来源操作按 write 交接（runtime 归入可变类）必须被接受。
	t.Run("runtime operation declared as write accepted", func(t *testing.T) {
		fixture := wmWriteOwnerFixture(t, func(ops []Operation) []Operation {
			ops[2].TargetOwner = "go-gateway"
			ops[2].Access = "write"
			ops[2].Source.WriterKind = "business-writer"
			ops[2].Transaction = "status-own"
			return ops
		})
		if _, err := Verify(fixture.manifestPath, fixture.typesPath, fixture.accessPath, fixture.handlerPath); err != nil {
			t.Fatalf("runtime→write 交接必须被接受: %v", err)
		}
	})

	t.Run("manifest unreadable", func(t *testing.T) {
		fixture := wmWriteOwnerFixture(t, nil)
		if _, err := Verify(filepath.Join(dir, "missing.json"), fixture.typesPath, fixture.accessPath, fixture.handlerPath); err == nil || !strings.Contains(err.Error(), "read owner manifest") {
			t.Fatalf("清单不可读必须报错: %v", err)
		}
	})
	t.Run("manifest malformed", func(t *testing.T) {
		path := wmWrite(t, dir, "bad.json", "{")
		fixture := wmWriteOwnerFixture(t, nil)
		if _, err := Verify(path, fixture.typesPath, fixture.accessPath, fixture.handlerPath); err == nil || !strings.Contains(err.Error(), "decode owner manifest") {
			t.Fatalf("清单坏 JSON 必须报错: %v", err)
		}
	})
	t.Run("manifest version invalid", func(t *testing.T) {
		path := wmWrite(t, dir, "v2.json", `{"manifest_version":2,"operations":[]}`)
		fixture := wmWriteOwnerFixture(t, nil)
		if _, err := Verify(path, fixture.typesPath, fixture.accessPath, fixture.handlerPath); err == nil || !strings.Contains(err.Error(), "version or operations are invalid") {
			t.Fatalf("版本错误必须报错: %v", err)
		}
	})
	t.Run("types unreadable", func(t *testing.T) {
		fixture := wmWriteOwnerFixture(t, nil)
		if _, err := Verify(fixture.manifestPath, filepath.Join(dir, "missing.ts"), fixture.accessPath, fixture.handlerPath); err == nil || !strings.Contains(err.Error(), "read DbServiceOperation source") {
			t.Fatalf("types 源不可读必须报错: %v", err)
		}
	})
	t.Run("access unreadable", func(t *testing.T) {
		fixture := wmWriteOwnerFixture(t, nil)
		if _, err := Verify(fixture.manifestPath, fixture.typesPath, filepath.Join(dir, "missing.ts"), fixture.handlerPath); err == nil || !strings.Contains(err.Error(), "read DbServiceOperation access map") {
			t.Fatalf("access 源不可读必须报错: %v", err)
		}
	})
	t.Run("handlers unreadable", func(t *testing.T) {
		fixture := wmWriteOwnerFixture(t, nil)
		if _, err := Verify(fixture.manifestPath, fixture.typesPath, fixture.accessPath, filepath.Join(dir, "missing.ts")); err == nil || !strings.Contains(err.Error(), "read DbServiceOperation handlers") {
			t.Fatalf("handlers 源不可读必须报错: %v", err)
		}
	})
	t.Run("types without operations", func(t *testing.T) {
		path := wmWrite(t, dir, "empty-types.ts", "export type Nothing = never\n")
		fixture := wmWriteOwnerFixture(t, nil)
		if _, err := Verify(fixture.manifestPath, path, fixture.accessPath, fixture.handlerPath); err == nil || !strings.Contains(err.Error(), "contains no operation types") {
			t.Fatalf("types 无操作必须报错: %v", err)
		}
	})
	t.Run("manifest missing type entries", func(t *testing.T) {
		fixture := wmWriteOwnerFixture(t, func(ops []Operation) []Operation { return ops[:2] })
		if _, err := Verify(fixture.manifestPath, fixture.typesPath, fixture.accessPath, fixture.handlerPath); err == nil || !strings.Contains(err.Error(), "missing DbServiceOperation entries") {
			t.Fatalf("清单缺项必须报错: %v", err)
		}
	})
	t.Run("access map with unknown operation", func(t *testing.T) {
		// legacy_alias 在 legacy 名单中豁免；新增未知键必须报错。
		accessPath := wmWrite(t, dir, "unknown-access.ts", wmValidAccess+"ghost_op: 'read',\n")
		fixture := wmWriteOwnerFixture(t, nil)
		if _, err := Verify(fixture.manifestPath, fixture.typesPath, accessPath, fixture.handlerPath); err == nil || !strings.Contains(err.Error(), `access map contains unknown operation "ghost_op"`) {
			t.Fatalf("access map 未知操作必须报错: %v", err)
		}
	})
}

func TestWMLineContainsAndHandlerNeedles(t *testing.T) {
	source := []byte("line1\nline2 has 'needle'\n")
	if !lineContains(source, 2, "'needle'") {
		t.Fatal("hit line must return true")
	}
	if lineContains(source, 99, "'needle'") || lineContains(source, 0, "x") || lineContains(source, 1, "") {
		t.Fatal("越界/空针必须返回 false")
	}
	if !bytesContainsHandler([]byte(`case "op":`), "op") {
		t.Fatal("双引号拼写必须被接受")
	}
	if bytesContainsHandler([]byte("nothing"), "op") {
		t.Fatal("无引用必须返回 false")
	}
}

// ---- capability manifest fixtures ----

func wmWriteCapabilityFixture(t *testing.T, mutate func(*[]Capability, *[]Operation)) (string, string) {
	t.Helper()
	dir := t.TempDir()
	operations := []Operation{
		{Operation: "op_read", Access: "read", Transaction: "read-only", CurrentOwner: "node", TargetOwner: "read-consumer"},
		{Operation: "op_write", Access: "write", Transaction: "accounts", CurrentOwner: "node", TargetOwner: "go-gateway"},
	}
	capabilities := []Capability{
		{ID: "cap-read", NodeWriterOperationGroup: "read-only", NodeOperations: []string{"op_read"}, OperationCount: 1, CurrentOwner: "node", TargetOwner: "read-consumer", GatewayTargetModule: "m", Status: "partial", MigrationMethod: "m", AcceptanceGates: []string{"g"}, Rollback: "r"},
		{ID: "cap-accounts", NodeWriterOperationGroup: "accounts", NodeOperations: []string{"op_write"}, OperationCount: 1, CurrentOwner: "node", TargetOwner: "go-gateway", GatewayTargetModule: "m", Status: "implemented", MigrationMethod: "m", AcceptanceGates: []string{"g"}, Rollback: "r", Evidence: []string{"docs/evidence.md"}},
	}
	mutate(&capabilities, &operations)
	capData, err := json.Marshal(map[string]any{"manifest_version": 1, "source_manifest": "op.json", "capabilities": capabilities})
	if err != nil {
		t.Fatal(err)
	}
	opData, err := json.Marshal(map[string]any{"manifest_version": 1, "operations": operations})
	if err != nil {
		t.Fatal(err)
	}
	return wmWrite(t, dir, "cap.json", string(capData)), wmWrite(t, dir, "op.json", string(opData))
}

func TestWMVerifyCapabilityManifestBaselineAndBranches(t *testing.T) {
	t.Run("baseline accepted", func(t *testing.T) {
		capPath, opPath := wmWriteCapabilityFixture(t, func(*[]Capability, *[]Operation) {})
		report, err := VerifyCapabilityManifest(capPath, opPath)
		if err != nil {
			t.Fatalf("合成基线必须通过: %v", err)
		}
		if report.Capabilities != 2 || report.Operations != 2 || report.Groups != 2 || report.StatusCoverage["implemented"] != 1 {
			t.Fatalf("报告计数异常: %+v", report)
		}
	})
	mutations := []struct {
		name     string
		mutate   func(*[]Capability, *[]Operation)
		fragment string
	}{
		{"empty group id", func(c *[]Capability, _ *[]Operation) { (*c)[0].NodeWriterOperationGroup = " " }, "lacks id or Node operation group"},
		{"duplicate group", func(c *[]Capability, _ *[]Operation) {
			(*c)[1].NodeWriterOperationGroup = (*c)[0].NodeWriterOperationGroup
		}, "duplicate group"},
		{"unknown group", func(c *[]Capability, _ *[]Operation) { (*c)[0].NodeWriterOperationGroup = "ghost" }, "references unknown operation group"},
		{"stale operation count", func(c *[]Capability, _ *[]Operation) { (*c)[0].OperationCount = 5 }, "operation_count=5"},
		{"missing metadata", func(c *[]Capability, _ *[]Operation) { (*c)[1].GatewayTargetModule = "" }, "lacks owner, module"},
		{"unsupported status", func(c *[]Capability, _ *[]Operation) { (*c)[0].Status = "future" }, "unsupported status"},
		{"no acceptance gates", func(c *[]Capability, _ *[]Operation) { (*c)[0].AcceptanceGates = nil }, "has no acceptance gates"},
		{"implemented without evidence", func(c *[]Capability, _ *[]Operation) { (*c)[1].Evidence = nil }, "claims implemented without evidence"},
		{"excluded wrong owner", func(c *[]Capability, _ *[]Operation) {
			(*c)[0].Status = "excluded"
			(*c)[0].TargetOwner = "go-gateway"
		}, "unchanged-excluded"},
		{"wrong handoff owner", func(c *[]Capability, _ *[]Operation) {
			(*c)[1].TargetOwner = "someone"
		}, "not an allowed handoff owner"},
		{"owner drift from operation", func(c *[]Capability, _ *[]Operation) {
			(*c)[1].CurrentOwner = "other"
		}, "owner metadata drifts"},
		{"empty listed operation", func(c *[]Capability, _ *[]Operation) {
			(*c)[0].NodeOperations = []string{"  "}
		}, "contains an empty operation"},
		{"duplicate listed operation", func(c *[]Capability, _ *[]Operation) {
			(*c)[0].NodeOperations = []string{"op_read", "op_read"}
		}, "duplicate operation"},
		{"operation outside source group", func(c *[]Capability, _ *[]Operation) {
			(*c)[0].NodeOperations = []string{"op_write"}
		}, "outside source group"},
		{"omitted operations", func(c *[]Capability, _ *[]Operation) {
			(*c)[0].NodeOperations = nil
			(*c)[0].OperationCount = 1
		}, "omits operations"},
		{"omitted groups", func(c *[]Capability, _ *[]Operation) {
			*c = (*c)[:1]
		}, "omits operation groups"},
		{"empty transaction group in operations", func(_ *[]Capability, o *[]Operation) {
			(*o)[0].Transaction = " "
		}, "empty transaction group"},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			capPath, opPath := wmWriteCapabilityFixture(t, test.mutate)
			if _, err := VerifyCapabilityManifest(capPath, opPath); err == nil || !strings.Contains(err.Error(), test.fragment) {
				t.Fatalf("期望错误包含 %q，实际: %v", test.fragment, err)
			}
		})
	}
	t.Run("inputs invalid", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := VerifyCapabilityManifest(filepath.Join(dir, "missing.json"), filepath.Join(dir, "missing2.json")); err == nil || !strings.Contains(err.Error(), "read capability manifest") {
			t.Fatalf("capability 不可读必须报错: %v", err)
		}
		capPath, opPath := wmWriteCapabilityFixture(t, func(*[]Capability, *[]Operation) {})
		if _, err := VerifyCapabilityManifest(capPath, filepath.Join(dir, "missing.json")); err == nil || !strings.Contains(err.Error(), "read operation manifest") {
			t.Fatalf("operation 清单不可读必须报错: %v", err)
		}
		badCap := wmWrite(t, dir, "bad-cap.json", "{")
		if _, err := VerifyCapabilityManifest(badCap, opPath); err == nil || !strings.Contains(err.Error(), "decode capability manifest") {
			t.Fatalf("capability 坏 JSON 必须报错: %v", err)
		}
		emptyCap := wmWrite(t, dir, "empty-cap.json", `{"manifest_version":1,"capabilities":[]}`)
		if _, err := VerifyCapabilityManifest(emptyCap, opPath); err == nil || !strings.Contains(err.Error(), "version or capabilities are invalid") {
			t.Fatalf("capability 为空必须报错: %v", err)
		}
		badOp := wmWrite(t, dir, "bad-op.json", "{")
		if _, err := VerifyCapabilityManifest(capPath, badOp); err == nil || !strings.Contains(err.Error(), "decode operation manifest") {
			t.Fatalf("operation 坏 JSON 必须报错: %v", err)
		}
		emptyOp := wmWrite(t, dir, "empty-op.json", `{"manifest_version":1,"operations":[]}`)
		if _, err := VerifyCapabilityManifest(capPath, emptyOp); err == nil || !strings.Contains(err.Error(), "version or operations are invalid") {
			t.Fatalf("operation 为空必须报错: %v", err)
		}
	})
}

// ---- gateway route manifest fixtures（不含 model-checks 特例）----

type wmRouteFamilyInput struct {
	id             string
	symbol         string
	routerFile     string
	routerSource   string
	mutations      []string
	mutationCount  int
	status         string
	evidence       []string
	gates          []string
	mount          string
	skipMountInApp bool
}

func wmWriteRouteFixture(t *testing.T, families []wmRouteFamilyInput, sourceAppArchived bool) string {
	t.Helper()
	root := t.TempDir()
	return wmWriteRouteFixtureAt(t, root, families, sourceAppArchived)
}

// wmWriteRouteFixtureAt 在指定 root 下构造 app 源、router 源与清单。
func wmWriteRouteFixtureAt(t *testing.T, root string, families []wmRouteFamilyInput, sourceAppArchived bool) string {
	t.Helper()
	var mounts strings.Builder
	for _, family := range families {
		if !family.skipMountInApp {
			mounts.WriteString("app.use(`${prefix}/x`, " + family.symbol + ")\n")
		}
		routerPath := filepath.Join(root, filepath.FromSlash(family.routerFile))
		if err := os.MkdirAll(filepath.Dir(routerPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(routerPath, []byte(family.routerSource), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	appPath := "live/app.ts"
	if sourceAppArchived {
		appPath = "migration-backup/node/final-archive/app.ts"
	}
	appAbs := filepath.Join(root, filepath.FromSlash(appPath))
	if err := os.MkdirAll(filepath.Dir(appAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(appAbs, []byte(mounts.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	familyList := make([]map[string]any, 0, len(families))
	for _, family := range families {
		entry := map[string]any{
			"id": family.id, "node_mount": "/x", "node_router_file": family.routerFile,
			"node_router_symbol": family.symbol, "node_mutations": family.mutations,
			"mutation_count": family.mutationCount, "gateway_handler": "gw", "status": family.status,
			"acceptance_gates": family.gates, "rollback": "r",
		}
		if family.evidence != nil {
			entry["evidence"] = family.evidence
		}
		familyList = append(familyList, entry)
	}
	manifest := map[string]any{"manifest_version": 1, "source_app": appPath, "families": familyList}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return wmWrite(t, root, "routes.json", string(data))
}

func wmSingleRouteFamily() wmRouteFamilyInput {
	return wmRouteFamilyInput{
		id: "accounts", symbol: "accountsRouter", routerFile: "routes/accounts.ts",
		routerSource: "accountsRouter.post('/rename', handler)\naccountsRouter.delete('/x', handler)\naccountsRouter.get('/list', handler)\n",
		mutations:    []string{"POST /rename", "DELETE /x"}, mutationCount: 2,
		status: "partial", gates: []string{"gate"},
	}
}

func TestWMVerifyGatewayRouteOwnerManifestSynthetic(t *testing.T) {
	t.Run("partial family accepted and reported pending", func(t *testing.T) {
		manifestPath := wmWriteRouteFixture(t, []wmRouteFamilyInput{wmSingleRouteFamily()}, false)
		root := filepath.Dir(manifestPath)
		report, err := VerifyGatewayRouteOwnerManifest(manifestPath, root)
		if err != nil {
			t.Fatalf("合成部分移交必须通过校验: %v", err)
		}
		if report.MutationRoutes != 2 || report.StatusCoverage["partial"] != 1 || len(report.PendingFamilies) != 1 {
			t.Fatalf("报告必须标注 pending: %+v", report)
		}
	})

	failures := []struct {
		name     string
		mutate   func(*wmRouteFamilyInput)
		fragment string
	}{
		{"missing identity metadata", func(f *wmRouteFamilyInput) { f.symbol = "  " }, "lacks identity"},
		{"duplicate family", func(f *wmRouteFamilyInput) {}, "unused"},
		{"unsupported status", func(f *wmRouteFamilyInput) { f.status = "someday" }, "unsupported status"},
		{"no gates", func(f *wmRouteFamilyInput) { f.gates = nil }, "has no acceptance gates"},
		{"implemented without evidence", func(f *wmRouteFamilyInput) { f.status = "implemented" }, "claims implemented without evidence"},
		{"archive pending without evidence", func(f *wmRouteFamilyInput) { f.status = "implemented-archive-pending" }, "without evidence"},
		{"archive pending without archive manifest", func(f *wmRouteFamilyInput) {
			f.status = "implemented-archive-pending"
			f.evidence = []string{"docs/normal.md"}
		}, "final-archive manifest reference"},
		{"mutation count drift", func(f *wmRouteFamilyInput) { f.mutationCount = 3 }, "mutation_count=3"},
		{"mutation contract drift", func(f *wmRouteFamilyInput) { f.mutations = []string{"POST /other", "DELETE /x"} }, "mutation contract drift"},
		{"declared empty mutation", func(f *wmRouteFamilyInput) { f.mutations = []string{"POST /rename", "  "} }, "declares an empty mutation"},
		{"declared duplicate mutation", func(f *wmRouteFamilyInput) { f.mutations = []string{"POST /rename", "POST /rename"} }, "duplicate mutation"},
		{"router not mounted", func(f *wmRouteFamilyInput) { f.skipMountInApp = true }, "is not mounted by"},
	}
	for _, test := range failures {
		t.Run(test.name, func(t *testing.T) {
			family := wmSingleRouteFamily()
			test.mutate(&family)
			manifestPath := wmWriteRouteFixture(t, []wmRouteFamilyInput{family}, false)
			_, err := VerifyGatewayRouteOwnerManifest(manifestPath, filepath.Dir(manifestPath))
			if test.fragment == "unused" {
				// duplicate family 需要两个同 id family，单独用例覆盖。
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.fragment) {
				t.Fatalf("期望错误包含 %q，实际: %v", test.fragment, err)
			}
		})
	}

	t.Run("duplicate family rejected", func(t *testing.T) {
		family := wmSingleRouteFamily()
		manifestPath := wmWriteRouteFixture(t, []wmRouteFamilyInput{family, family}, false)
		_, err := VerifyGatewayRouteOwnerManifest(manifestPath, filepath.Dir(manifestPath))
		if err == nil || !strings.Contains(err.Error(), "duplicate route family") {
			t.Fatalf("重复 family 必须报错: %v", err)
		}
	})
	t.Run("shared router file rejected", func(t *testing.T) {
		first, second := wmSingleRouteFamily(), wmSingleRouteFamily()
		second.id = "accounts2"
		manifestPath := wmWriteRouteFixture(t, []wmRouteFamilyInput{first, second}, false)
		_, err := VerifyGatewayRouteOwnerManifest(manifestPath, filepath.Dir(manifestPath))
		if err == nil || !strings.Contains(err.Error(), "more than once") {
			t.Fatalf("重复源文件必须报错: %v", err)
		}
	})
	t.Run("archive pending with archived app accepted", func(t *testing.T) {
		family := wmSingleRouteFamily()
		family.status = "implemented-archive-pending"
		family.evidence = []string{"migration-backup/node/final-archive/manifest.md"}
		family.skipMountInApp = true
		root := t.TempDir()
		archiveDoc := filepath.Join(root, "migration-backup", "node", "final-archive", "manifest.md")
		if err := os.MkdirAll(filepath.Dir(archiveDoc), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archiveDoc, []byte("archive manifest"), 0o600); err != nil {
			t.Fatal(err)
		}
		manifestPath := wmWriteRouteFixtureAt(t, root, []wmRouteFamilyInput{family}, true)
		report, err := VerifyGatewayRouteOwnerManifest(manifestPath, root)
		if err != nil {
			t.Fatalf("归档挂载的 archive-pending 必须通过: %v", err)
		}
		if report.StatusCoverage["implemented-archive-pending"] != 1 {
			t.Fatalf("状态覆盖异常: %+v", report)
		}
	})
	t.Run("manifest and source inputs invalid", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := VerifyGatewayRouteOwnerManifest(filepath.Join(dir, "missing.json"), dir); err == nil || !strings.Contains(err.Error(), "read gateway route manifest") {
			t.Fatalf("清单不可读必须报错: %v", err)
		}
		bad := wmWrite(t, dir, "bad.json", "{")
		if _, err := VerifyGatewayRouteOwnerManifest(bad, dir); err == nil || !strings.Contains(err.Error(), "decode gateway route manifest") {
			t.Fatalf("坏 JSON 必须报错: %v", err)
		}
		noFamilies := wmWrite(t, dir, "no-families.json", `{"manifest_version":1,"families":[]}`)
		if _, err := VerifyGatewayRouteOwnerManifest(noFamilies, dir); err == nil || !strings.Contains(err.Error(), "version or families are invalid") {
			t.Fatalf("无 family 必须报错: %v", err)
		}
		emptyApp := map[string]any{"manifest_version": 1, "source_app": " ", "families": []map[string]any{{"id": "x"}}}
		data, _ := json.Marshal(emptyApp)
		emptyPath := wmWrite(t, dir, "empty-app.json", string(data))
		if _, err := VerifyGatewayRouteOwnerManifest(emptyPath, dir); err == nil || !strings.Contains(err.Error(), "source app is empty") {
			t.Fatalf("source app 为空必须报错: %v", err)
		}
	})

	t.Run("evidence file rules", func(t *testing.T) {
		root := t.TempDir()
		// implemented 状态必须提供证据；绝对路径证据直接拒绝。
		family := wmSingleRouteFamily()
		family.status = "implemented"
		family.evidence = []string{root}
		manifestPath := wmWriteRouteFixtureAt(t, root, []wmRouteFamilyInput{family}, false)
		if _, err := VerifyGatewayRouteOwnerManifest(manifestPath, root); err == nil || !strings.Contains(err.Error(), "repository-relative") {
			t.Fatalf("绝对路径证据必须拒绝: %v", err)
		}
	})
}

func TestWMVerifyMutationSetDirect(t *testing.T) {
	if err := verifyMutationSet("f", []string{"POST /a"}, []string{"POST /a"}); err != nil {
		t.Fatalf("一致集合必须通过: %v", err)
	}
	if err := verifyMutationSet("f", []string{"POST /a"}, nil); err == nil || !strings.Contains(err.Error(), "declares 1 mutations") {
		t.Fatalf("数量漂移必须报错: %v", err)
	}
	err := verifyMutationSet("f", []string{"POST /a"}, []string{"POST /b"})
	if err == nil || !strings.Contains(err.Error(), "missing=POST /a") || !strings.Contains(err.Error(), "unexpected=POST /b") {
		t.Fatalf("缺失/多余必须并列报告: %v", err)
	}
	if err := verifyExactRouteSet("matrix", []string{"GET /a"}, []string{"GET /a"}); err != nil {
		t.Fatalf("verifyExactRouteSet 一致必须通过: %v", err)
	}
	if err := verifyExactRouteSet("matrix", []string{"GET /a"}, nil); err == nil || !strings.Contains(err.Error(), "route matrix count") {
		t.Fatalf("矩阵计数必须报错: %v", err)
	}
}

func TestWMReadRepositoryEvidenceRules(t *testing.T) {
	root := t.TempDir()
	if _, err := readRepositoryEvidence(root, "  "); err == nil || !strings.Contains(err.Error(), "path is empty") {
		t.Fatalf("空路径必须拒绝: %v", err)
	}
	abs := filepath.Join(root, "x.md")
	if _, err := readRepositoryEvidence(root, abs); err == nil || !strings.Contains(err.Error(), "repository-relative") {
		t.Fatalf("绝对路径必须拒绝: %v", err)
	}
	if _, err := readRepositoryEvidence(root, "../outside.md"); err == nil || !strings.Contains(err.Error(), "escapes repository root") {
		t.Fatalf("逃逸路径必须拒绝: %v", err)
	}
	if _, err := readRepositoryEvidence(root, "missing.md"); err == nil {
		t.Fatal("缺失文件必须报错")
	}
	if err := os.MkdirAll(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := readRepositoryEvidence(root, "adir"); err == nil || !strings.Contains(err.Error(), "path is a directory") {
		t.Fatalf("目录必须拒绝: %v", err)
	}
	wmWrite(t, root, "empty.md", "   \n")
	if _, err := readRepositoryEvidence(root, "empty.md"); err == nil || !strings.Contains(err.Error(), "file is empty") {
		t.Fatalf("空文件必须拒绝: %v", err)
	}
	wmWrite(t, root, "ok.md", "evidence")
	if data, err := readRepositoryEvidence(root, "ok.md"); err != nil || string(data) != "evidence" {
		t.Fatalf("合法证据必须可读: %v", err)
	}
}
