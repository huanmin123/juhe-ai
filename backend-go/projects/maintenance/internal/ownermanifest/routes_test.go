package ownermanifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyRepositoryGatewayRouteOwnerManifest(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..")
	report, err := VerifyGatewayRouteOwnerManifest(filepath.Join(root, "docs", "migration", "GatewayManagementRouteOwnerManifest.json"), root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Families < 20 || report.MutationRoutes < 80 {
		t.Fatalf("unexpected route report=%+v", report)
	}
	if len(report.PendingFamilies) == 0 {
		t.Fatal("unconnected routes must keep the gate closed")
	}
}

func TestVerifyGatewayRouteOwnerManifestRejectsSourceDrift(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "routes.ts")
	if err := os.WriteFile(source, []byte("router.post('/one', fn)\nrouter.delete('/two', fn)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(dir, "app.ts")
	if err := os.WriteFile(app, []byte("app.use('/x', router)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := GatewayRouteOwnerManifest{ManifestVersion: 1, SourceApp: "app.ts", Families: []GatewayRouteFamily{{ID: "x", NodeMount: "/x", NodeRouterFile: "routes.ts", NodeRouterSymbol: "router", NodeMutations: []string{"POST /one"}, MutationCount: 1, GatewayHandler: "gateway/x", Status: "missing", AcceptanceGates: []string{"gate"}, Rollback: "drain"}}}
	data, _ := json.Marshal(manifest)
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyGatewayRouteOwnerManifest(manifestPath, dir); err == nil {
		t.Fatal("source drift must fail closed")
	}
}

func TestVerifyGatewayRouteOwnerManifestModelChecksArchiveClosesFamily(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..")
	report, err := VerifyGatewayRouteOwnerManifest(filepath.Join(root, "docs", "migration", "GatewayManagementRouteOwnerManifest.json"), root)
	if err != nil {
		t.Fatal(err)
	}
	if contains(report.PendingFamilies, "model-checks") {
		t.Fatalf("archived model-checks must not remain pending: %+v", report)
	}
	if report.StatusCoverage["implemented"] != 1 {
		t.Fatalf("model-checks must be the implemented route family: %+v", report)
	}
}

func TestVerifyGatewayRouteOwnerManifestModelChecksImplementedRequiresExactProof(t *testing.T) {
	root, manifest := writeModelChecksFixture(t)
	manifest.Families[0].Status = "implemented"
	if err := writeManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyGatewayRouteOwnerManifest(filepath.Join(root, "manifest.json"), root); err == nil || !strings.Contains(err.Error(), "without Gateway route proof") {
		t.Fatalf("implemented model-checks without proof must fail closed, err=%v", err)
	}
}

func TestVerifyGatewayRouteOwnerManifestModelChecksProofRequiresDualScopedEntrancesAndMatrix(t *testing.T) {
	root, manifest := writeModelChecksFixture(t)
	manifest.Families[0].Status = "implemented"
	manifest.Families[0].GatewayProof = &GatewayRouteProof{
		Methods: append([]string(nil), modelChecksRouteMatrix...),
		Mounts: []GatewayRouteMount{
			{Path: "/__aisys__/api/model-checks", Scope: "admin", EvidenceFile: "backend-go/projects/gateway/cmd/juhe-ai-gateway/system_mounts.go", ScopeEvidence: "adminScope"},
			{Path: "/__aisys__/api/my-model-checks", Scope: "self", EvidenceFile: "backend-go/projects/gateway/cmd/juhe-ai-gateway/system_mounts.go", ScopeEvidence: "selfScope"},
		},
	}
	if err := writeManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyGatewayRouteOwnerManifest(filepath.Join(root, "manifest.json"), root); err != nil {
		t.Fatalf("complete exact model-checks proof should verify: %v", err)
	}
	manifest.Families[0].GatewayProof.Mounts = manifest.Families[0].GatewayProof.Mounts[:1]
	if err := writeManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyGatewayRouteOwnerManifest(filepath.Join(root, "manifest.json"), root); err == nil || !strings.Contains(err.Error(), "proof mounts") {
		t.Fatalf("one entrance must not satisfy complete proof, err=%v", err)
	}
	manifest.Families[0].GatewayProof.Mounts = []GatewayRouteMount{
		{Path: "/__aisys__/api/model-checks", Scope: "admin", EvidenceFile: "backend-go/projects/gateway/cmd/juhe-ai-gateway/system_mounts.go", ScopeEvidence: "adminScope"},
		{Path: "/__aisys__/api/my-model-checks", Scope: "self", EvidenceFile: "backend-go/projects/gateway/cmd/juhe-ai-gateway/system_mounts.go", ScopeEvidence: "selfScope"},
	}
	manifest.Families[0].GatewayProof.Methods = manifest.Families[0].GatewayProof.Methods[:len(manifest.Families[0].GatewayProof.Methods)-1]
	if err := writeManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyGatewayRouteOwnerManifest(filepath.Join(root, "manifest.json"), root); err == nil || !strings.Contains(err.Error(), "route matrix count") {
		t.Fatalf("incomplete method matrix must fail closed, err=%v", err)
	}
	manifest.Families[0].GatewayProof.Methods = append([]string(nil), modelChecksRouteMatrix...)
	manifest.Families[0].GatewayProof.Mounts[1].Scope = "admin"
	if err := writeManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyGatewayRouteOwnerManifest(filepath.Join(root, "manifest.json"), root); err == nil || !strings.Contains(err.Error(), "scope=") {
		t.Fatalf("wrong self entrance scope must fail closed, err=%v", err)
	}
}

func writeModelChecksFixture(t *testing.T) (string, GatewayRouteOwnerManifest) {
	t.Helper()
	root := t.TempDir()
	writeFixtureFile(t, root, "backend/src/modules/system-api/system-api-app.ts", "app.use(`${systemApiPrefix}/my-model-checks`, forceSelfAccessScope, modelChecksRouter)\napp.use(`${systemApiPrefix}/model-checks`, requireAdmin, modelChecksRouter)\n")
	nodeRoutes := make([]string, 0, len(modelChecksRouteMatrix))
	for _, route := range modelChecksRouteMatrix {
		parts := strings.SplitN(route, " ", 2)
		nodeRoutes = append(nodeRoutes, "modelChecksRouter."+strings.ToLower(parts[0])+"('"+parts[1]+"', handler)")
	}
	writeFixtureFile(t, root, "backend/src/modules/model-checks/model-checks.routes.ts", strings.Join(nodeRoutes, "\n"))
	writeFixtureFile(t, root, "backend-go/projects/gateway/internal/modelcheckowner/http.go", strings.Join(modelChecksGatewayHandlerNeedles, "\n"))
	writeFixtureFile(t, root, "backend-go/projects/gateway/internal/modelcheckowner/runtime.go", "package modelcheckowner\n")
	writeFixtureFile(t, root, "backend-go/projects/gateway/cmd/juhe-ai-gateway/main.go", `j3bHost.Mount(managementMux, "/model-checks/")`)
	writeFixtureFile(t, root, "backend-go/projects/gateway/cmd/juhe-ai-gateway/system_mounts.go", `adminScope /__aisys__/api/model-checks selfScope /__aisys__/api/my-model-checks`)
	manifest := GatewayRouteOwnerManifest{ManifestVersion: 1, SourceApp: "backend/src/modules/system-api/system-api-app.ts", Families: []GatewayRouteFamily{{
		ID: "model-checks", NodeMount: "/model-checks and /my-model-checks", NodeRouterFile: "backend/src/modules/model-checks/model-checks.routes.ts", NodeRouterSymbol: "modelChecksRouter", MutationCount: 8,
		GatewayHandler: "backend-go/projects/gateway/internal/modelcheckowner", Status: "partial", AcceptanceGates: []string{"gate"}, Rollback: "drain",
		Evidence: []string{"backend-go/projects/gateway/internal/modelcheckowner/http.go", "backend-go/projects/gateway/internal/modelcheckowner/runtime.go"},
	}}}
	return root, manifest
}

func writeManifest(root string, manifest GatewayRouteOwnerManifest) error {
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "manifest.json"), data, 0o600)
}

func writeFixtureFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func contains(items []string, needle string) bool {
	for _, item := range items {
		if item == needle {
			return true
		}
	}
	return false
}
