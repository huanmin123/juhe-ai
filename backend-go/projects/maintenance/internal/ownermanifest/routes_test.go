package ownermanifest

import (
	"encoding/json"
	"os"
	"path/filepath"
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
