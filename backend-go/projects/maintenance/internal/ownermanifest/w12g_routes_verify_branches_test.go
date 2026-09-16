package ownermanifest

// w12g 波次：补齐 routes 校验链（model-checks 基线/证明、证据读取、清单早期
// 校验）与 validateOperationSourceContract、VerifyCapabilityManifest 的错误
// 分支。fixture 复用 routes_test.go 的 writeModelChecksFixture /
// writeFixtureFile / writeManifest 模式，只走临时目录。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func w12gTrue() *bool { value := true; return &value }

func TestW12GReadRepositoryEvidenceRejections(t *testing.T) {
	root := t.TempDir()
	// Windows 上 IsAbs 需要卷名；root 本身是绝对路径，拼出的证据路径必然绝对。
	if _, err := readRepositoryEvidence(root, filepath.Join(root, "escape.md")); err == nil || !strings.Contains(err.Error(), "repository-relative") {
		t.Fatalf("绝对路径必须拒绝: %v", err)
	}
	dir := filepath.Join(root, "docs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := readRepositoryEvidence(root, "docs"); err == nil || !strings.Contains(err.Error(), "path is a directory") {
		t.Fatalf("目录必须拒绝: %v", err)
	}
}

func TestW12GCollectRouterMethodsFiltersForeignSymbols(t *testing.T) {
	source := []byte("otherRouter.get('/x', h)\nrouter.post('/run', h)\nrouter.get('/options', h)\n")
	methods := collectRouterMethods(source, "router")
	if len(methods) != 2 || methods[0] != "POST /run" || methods[1] != "GET /options" {
		t.Fatalf("外部 router 符号必须被过滤: %v", methods)
	}
}

func TestW12GValidateOperationSourceContractGateFlags(t *testing.T) {
	valid := OperationSourceContract{
		Path: "p", Range: "r", HandlerPath: "h", AccessModePath: "a",
		CutoverEpochRequired: w12gTrue(), DrainRequired: w12gTrue(), RollbackRequiresStopAndReplay: w12gTrue(),
	}
	if err := validateOperationSourceContract(valid); err != nil {
		t.Fatalf("完整契约必须通过: %v", err)
	}
	incomplete := valid
	incomplete.Path = " "
	if err := validateOperationSourceContract(incomplete); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("空字段必须拒绝: %v", err)
	}
	noCutover := valid
	noCutover.CutoverEpochRequired = nil
	if err := validateOperationSourceContract(noCutover); err == nil || !strings.Contains(err.Error(), "cutover_epoch_required") {
		t.Fatalf("缺失 cutover 门必须拒绝: %v", err)
	}
	falseDrain := valid
	falseDrain.DrainRequired = w12gTrue()
	*falseDrain.DrainRequired = false
	if err := validateOperationSourceContract(falseDrain); err == nil || !strings.Contains(err.Error(), "drain_required") {
		t.Fatalf("drain=false 必须拒绝: %v", err)
	}
	falseRollback := valid
	falseRollback.RollbackRequiresStopAndReplay = nil
	if err := validateOperationSourceContract(falseRollback); err == nil || !strings.Contains(err.Error(), "rollback_requires_stop_and_replay") {
		t.Fatalf("缺失 rollback 门必须拒绝: %v", err)
	}
}

func TestW12GVerifyMissingDbServiceOperationEntries(t *testing.T) {
	dir := t.TempDir()
	writeFixtureFile(t, dir, "types.ts", "export type DbServiceOperation =\n  | { type: 'opA' }\n  | { type: 'opB' }\n")
	writeFixtureFile(t, dir, "access.ts", "opA: 'write',\nopB: 'write',\n")
	writeFixtureFile(t, dir, "handlers.ts", "'opA': handler,\n'opB': handler,\n")
	manifestValue := manifest{
		ManifestVersion:         1,
		OperationSourceContract: OperationSourceContract{Path: "p", Range: "r", HandlerPath: "h", AccessModePath: "a", CutoverEpochRequired: w12gTrue(), DrainRequired: w12gTrue(), RollbackRequiresStopAndReplay: w12gTrue()},
		Operations: []Operation{{
			Operation: "opA", Access: "write", Tables: "t", Transaction: "tx", CurrentOwner: "node", TargetOwner: "go-gateway", Rollback: "stop", Verification: "v",
			Source: Source{TypeUnion: "backend/src/modules/db-service/db-service-types.ts", Handler: "backend/src/modules/db-service/db-service-handlers.ts", TypeLine: 2, AccessModeLine: 1, HandlerLine: 1, Entrypoint: "db-service-handler", WriterKind: "business-writer"},
		}},
	}
	data, err := json.Marshal(manifestValue)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "owner.json")
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Verify(manifestPath,
		filepath.Join(dir, "types.ts"), filepath.Join(dir, "access.ts"), filepath.Join(dir, "handlers.ts"))
	if err == nil || !strings.Contains(err.Error(), "missing DbServiceOperation entries") {
		t.Fatalf("清单缺 opB 必须 fail closed: %v", err)
	}
}

func TestW12GVerifyGatewayRouteOwnerManifestEarlyRejections(t *testing.T) {
	t.Run("app source missing", func(t *testing.T) {
		root := t.TempDir()
		writeFixtureFile(t, root, "routes.ts", "router.post('/one', h)\n")
		manifestPath, err := w12gWriteRouteManifest(root, GatewayRouteOwnerManifest{
			ManifestVersion: 1, SourceApp: "missing-app.ts",
			Families: []GatewayRouteFamily{w12gRouteFamily("x", "routes.ts", "missing", "missing")},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyGatewayRouteOwnerManifest(manifestPath, root); err == nil || !strings.Contains(err.Error(), "read Node system-api app") {
			t.Fatalf("app 源缺失必须报读失败: %v", err)
		}
	})

	t.Run("router source missing", func(t *testing.T) {
		root := t.TempDir()
		writeFixtureFile(t, root, "app.ts", "app.use('/x', router)\n")
		manifestPath, err := w12gWriteRouteManifest(root, GatewayRouteOwnerManifest{
			ManifestVersion: 1, SourceApp: "app.ts",
			Families: []GatewayRouteFamily{w12gRouteFamily("x", "missing-routes.ts", "missing", "missing")},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyGatewayRouteOwnerManifest(manifestPath, root); err == nil || !strings.Contains(err.Error(), "read route family") {
			t.Fatalf("router 源缺失必须报读失败: %v", err)
		}
	})

	t.Run("archive pending still mounted", func(t *testing.T) {
		root := t.TempDir()
		writeFixtureFile(t, root, "app.ts", "app.use('/x', router)\n")
		writeFixtureFile(t, root, "routes.ts", "router.post('/one', h)\n")
		writeFixtureFile(t, root, "evidence/final-archive-manifest.md", "archive snapshot manifest\n")
		family := w12gRouteFamily("x", "routes.ts", "missing", "missing")
		family.Status = "implemented-archive-pending"
		family.Evidence = []string{"evidence/final-archive-manifest.md"}
		manifestPath, err := w12gWriteRouteManifest(root, GatewayRouteOwnerManifest{ManifestVersion: 1, SourceApp: "app.ts", Families: []GatewayRouteFamily{family}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyGatewayRouteOwnerManifest(manifestPath, root); err == nil || !strings.Contains(err.Error(), "archive-pending but still mounted") {
			t.Fatalf("archive-pending 仍挂载必须拒绝: %v", err)
		}
	})
}

func TestW12GVerifyModelChecksRouteOwnerBranches(t *testing.T) {
	t.Run("wrong node mount", func(t *testing.T) {
		root, routeManifest := writeModelChecksFixture(t)
		routeManifest.Families[0].NodeMount = "/wrong"
		if err := writeManifest(root, routeManifest); err != nil {
			t.Fatal(err)
		}
		_, err := VerifyGatewayRouteOwnerManifest(filepath.Join(root, "manifest.json"), root)
		if err == nil || !strings.Contains(err.Error(), "must declare both Node management entrances") {
			t.Fatalf("NodeMount 漂移必须拒绝: %v", err)
		}
	})

	t.Run("route matrix drift keeps mutation count", func(t *testing.T) {
		// 改一个 GET 路径：主循环 mutation 计数不变（GET 被跳过），但
		// model-checks 完整矩阵校验失败。
		root, routeManifest := writeModelChecksFixture(t)
		writeFixtureFile(t, root, "backend/src/modules/model-checks/model-checks.routes.ts",
			strings.Replace(strings.Join(nodeRoutesForMatrix(t), "\n"), "get('/options'", "get('/renamed'", 1))
		if err := writeManifest(root, routeManifest); err != nil {
			t.Fatal(err)
		}
		_, err := VerifyGatewayRouteOwnerManifest(filepath.Join(root, "manifest.json"), root)
		if err == nil || !strings.Contains(err.Error(), "mutation contract drift") || !strings.Contains(err.Error(), "GET /renamed") {
			t.Fatalf("Node 矩阵漂移必须拒绝: %v", err)
		}
	})

	t.Run("single mount drift fails", func(t *testing.T) {
		root, routeManifest := writeModelChecksFixture(t)
		writeFixtureFile(t, root, "backend/src/modules/system-api/system-api-app.ts",
			"app.use(`${systemApiPrefix}/model-checks`, requireAdmin, modelChecksRouter)\n")
		if err := writeManifest(root, routeManifest); err != nil {
			t.Fatal(err)
		}
		_, err := VerifyGatewayRouteOwnerManifest(filepath.Join(root, "manifest.json"), root)
		if err == nil || !strings.Contains(err.Error(), "dual management mount") {
			t.Fatalf("单挂载漂移必须拒绝: %v", err)
		}
	})

	t.Run("both mounts removed with partial status continues", func(t *testing.T) {
		root, routeManifest := writeModelChecksFixture(t)
		writeFixtureFile(t, root, "backend/src/modules/system-api/system-api-app.ts", "// mounts intentionally removed\n")
		if err := writeManifest(root, routeManifest); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyGatewayRouteOwnerManifest(filepath.Join(root, "manifest.json"), root); err != nil {
			t.Fatalf("partial 豁免双挂载移除: %v", err)
		}
	})
}

func TestW12GVerifyModelChecksGatewayBaselineBranches(t *testing.T) {
	buildFamily := func(t *testing.T) (string, GatewayRouteFamily) {
		t.Helper()
		root, routeManifest := writeModelChecksFixture(t)
		return root, routeManifest.Families[0]
	}

	t.Run("handler path missing", func(t *testing.T) {
		root, family := buildFamily(t)
		family.GatewayHandler = "backend-go/projects/gateway/internal/not-there"
		err := verifyModelChecksGatewayBaseline(family, root)
		if err == nil || !strings.Contains(err.Error(), "Gateway handler path") {
			t.Fatalf("handler 缺失必须报 stat 错误: %v", err)
		}
	})

	t.Run("handler not directory", func(t *testing.T) {
		root, family := buildFamily(t)
		family.GatewayHandler = "backend-go/projects/gateway/internal/modelcheckowner/http.go"
		err := verifyModelChecksGatewayBaseline(family, root)
		if err == nil || !strings.Contains(err.Error(), "must be a directory") {
			t.Fatalf("handler 为文件必须拒绝: %v", err)
		}
	})

	t.Run("evidence missing http.go", func(t *testing.T) {
		root, family := buildFamily(t)
		family.Evidence = []string{"backend-go/projects/gateway/internal/modelcheckowner/runtime.go"}
		err := verifyModelChecksGatewayBaseline(family, root)
		if err == nil || !strings.Contains(err.Error(), "evidence must include") {
			t.Fatalf("证据缺 http.go 必须拒绝: %v", err)
		}
	})

	t.Run("evidence unreadable", func(t *testing.T) {
		root, family := buildFamily(t)
		family.Evidence = []string{"backend-go/projects/gateway/internal/modelcheckowner/http.go"}
		if err := os.Remove(filepath.Join(root, "backend-go", "projects", "gateway", "internal", "modelcheckowner", "http.go")); err != nil {
			t.Fatal(err)
		}
		err := verifyModelChecksGatewayBaseline(family, root)
		if err == nil || !strings.Contains(err.Error(), "read model-checks Gateway handler evidence") {
			t.Fatalf("证据读取失败必须被包装: %v", err)
		}
	})

	t.Run("needle missing", func(t *testing.T) {
		root, family := buildFamily(t)
		writeFixtureFile(t, root, "backend-go/projects/gateway/internal/modelcheckowner/http.go", "package modelcheckowner\n")
		err := verifyModelChecksGatewayBaseline(family, root)
		if err == nil || !strings.Contains(err.Error(), "missing route evidence") {
			t.Fatalf("handler 缺路由证据必须拒绝: %v", err)
		}
	})

	t.Run("gateway mount source missing", func(t *testing.T) {
		root, family := buildFamily(t)
		if err := os.Remove(filepath.Join(root, "backend-go", "projects", "gateway", "cmd", "juhe-ai-gateway", "main.go")); err != nil {
			t.Fatal(err)
		}
		err := verifyModelChecksGatewayBaseline(family, root)
		if err == nil || !strings.Contains(err.Error(), "read model-checks Gateway mount source") {
			t.Fatalf("main.go 缺失必须被包装: %v", err)
		}
	})

	t.Run("gateway mount evidence missing", func(t *testing.T) {
		root, family := buildFamily(t)
		writeFixtureFile(t, root, "backend-go/projects/gateway/cmd/juhe-ai-gateway/main.go", "package main\n")
		err := verifyModelChecksGatewayBaseline(family, root)
		if err == nil || !strings.Contains(err.Error(), "partial Gateway mount evidence is missing") {
			t.Fatalf("挂载证据缺失必须拒绝: %v", err)
		}
	})
}

func TestW12GVerifyModelChecksGatewayProofBranches(t *testing.T) {
	baseMounts := func() []GatewayRouteMount {
		return []GatewayRouteMount{
			{Path: "/__aisys__/api/model-checks", Scope: "admin", EvidenceFile: "mounts.go", ScopeEvidence: "adminScope"},
			{Path: "/__aisys__/api/my-model-checks", Scope: "self", EvidenceFile: "mounts.go", ScopeEvidence: "selfScope"},
		}
	}
	newRoot := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		writeFixtureFile(t, root, "mounts.go", "adminScope /__aisys__/api/model-checks selfScope /__aisys__/api/my-model-checks")
		return root
	}

	t.Run("unexpected mount path", func(t *testing.T) {
		root := newRoot(t)
		proof := &GatewayRouteProof{Methods: append([]string(nil), modelChecksRouteMatrix...), Mounts: baseMounts()}
		proof.Mounts[1].Path = "/__aisys__/api/other"
		err := verifyModelChecksGatewayProof(proof, root)
		if err == nil || !strings.Contains(err.Error(), "unexpected mount") {
			t.Fatalf("未知挂载必须拒绝: %v", err)
		}
	})

	t.Run("duplicate mount", func(t *testing.T) {
		root := newRoot(t)
		proof := &GatewayRouteProof{Methods: append([]string(nil), modelChecksRouteMatrix...), Mounts: baseMounts()}
		proof.Mounts[1] = proof.Mounts[0]
		err := verifyModelChecksGatewayProof(proof, root)
		if err == nil || !strings.Contains(err.Error(), "duplicates mount") {
			t.Fatalf("重复挂载必须拒绝: %v", err)
		}
	})

	t.Run("empty scope evidence", func(t *testing.T) {
		root := newRoot(t)
		proof := &GatewayRouteProof{Methods: append([]string(nil), modelChecksRouteMatrix...), Mounts: baseMounts()}
		proof.Mounts[0].ScopeEvidence = " "
		err := verifyModelChecksGatewayProof(proof, root)
		if err == nil || !strings.Contains(err.Error(), "scope evidence is empty") {
			t.Fatalf("空 scope 证据必须拒绝: %v", err)
		}
	})

	t.Run("evidence file missing", func(t *testing.T) {
		root := newRoot(t)
		proof := &GatewayRouteProof{Methods: append([]string(nil), modelChecksRouteMatrix...), Mounts: baseMounts()}
		proof.Mounts[0].EvidenceFile = "missing-mounts.go"
		err := verifyModelChecksGatewayProof(proof, root)
		if err == nil || !strings.Contains(err.Error(), "evidence:") {
			t.Fatalf("证据文件缺失必须报错: %v", err)
		}
	})

	t.Run("evidence does not prove path and scope", func(t *testing.T) {
		root := newRoot(t)
		proof := &GatewayRouteProof{Methods: append([]string(nil), modelChecksRouteMatrix...), Mounts: baseMounts()}
		proof.Mounts[0].ScopeEvidence = "unrelated-token"
		err := verifyModelChecksGatewayProof(proof, root)
		if err == nil || !strings.Contains(err.Error(), "does not prove declared path and scope") {
			t.Fatalf("证据不证明挂载必须拒绝: %v", err)
		}
	})
}

func TestW12GVerifyCapabilityManifestDuplicateOperationAcrossGroups(t *testing.T) {
	dir := t.TempDir()
	// 同名操作 x 出现在 g1 与 g2 两个事务组。
	operations := manifest{
		ManifestVersion: 1,
		Operations: []Operation{
			{Operation: "x", Access: "write", Transaction: "g1", CurrentOwner: "node", TargetOwner: "go-gateway"},
			{Operation: "x", Access: "write", Transaction: "g2", CurrentOwner: "node", TargetOwner: "go-gateway"},
		},
	}
	operationsData, err := json.Marshal(operations)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "operations.json"), operationsData, 0o600); err != nil {
		t.Fatal(err)
	}
	capabilityFor := func(group string) Capability {
		return Capability{
			ID: "cap-" + group, NodeWriterOperationGroup: group, NodeOperations: []string{"x"}, OperationCount: 1,
			CurrentOwner: "node", TargetOwner: "go-gateway", GatewayTargetModule: "gateway", Status: "missing",
			MigrationMethod: "rewrite", AcceptanceGates: []string{"gate"}, Rollback: "drain",
		}
	}
	capabilities := CapabilityManifest{ManifestVersion: 1, Capabilities: []Capability{capabilityFor("g1"), capabilityFor("g2")}}
	capabilitiesData, err := json.Marshal(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "capabilities.json"), capabilitiesData, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = VerifyCapabilityManifest(filepath.Join(dir, "capabilities.json"), filepath.Join(dir, "operations.json"))
	if err == nil || !strings.Contains(err.Error(), "is assigned to groups") {
		t.Fatalf("跨组重复操作必须拒绝: %v", err)
	}
}

// w12gRouteFamily 构造一个只带必需元数据的路由 family。
func w12gRouteFamily(id, routerFile, status, handler string) GatewayRouteFamily {
	return GatewayRouteFamily{
		ID: id, NodeMount: "/x", NodeRouterFile: routerFile, NodeRouterSymbol: "router",
		NodeMutations: []string{"POST /one"}, MutationCount: 1, GatewayHandler: handler,
		Status: status, AcceptanceGates: []string{"gate"}, Rollback: "drain",
	}
}

func w12gWriteRouteManifest(root string, routeManifest GatewayRouteOwnerManifest) (string, error) {
	data, err := json.Marshal(routeManifest)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// nodeRoutesForMatrix 把 modelChecksRouteMatrix 还原为 fixture Node 路由源。
func nodeRoutesForMatrix(t *testing.T) []string {
	t.Helper()
	lines := make([]string, 0, len(modelChecksRouteMatrix))
	for _, route := range modelChecksRouteMatrix {
		parts := strings.SplitN(route, " ", 2)
		lines = append(lines, "modelChecksRouter."+strings.ToLower(parts[0])+"('"+parts[1]+"', handler)")
	}
	return lines
}
