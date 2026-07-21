package modelcatalogsnapshotrebuild

import (
	"os"
	"testing"
)

func TestCrossRuntimeNodeBridge(t *testing.T) {
	endpoint := os.Getenv("JUHE_MODEL_CATALOG_SNAPSHOT_BRIDGE_URL")
	secret := os.Getenv("JUHE_MODEL_CATALOG_SNAPSHOT_BRIDGE_SECRET")
	if endpoint == "" || secret == "" {
		t.Skip("cross-runtime Node bridge endpoint is not configured")
	}
	client, err := NewClient(endpoint, " "+secret+" ")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := client.Rebuild(t.Context(), "all", ""); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
}
