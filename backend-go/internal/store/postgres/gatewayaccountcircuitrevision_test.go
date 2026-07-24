package postgres

import (
	"strings"
	"testing"
)

func TestGatewayAccountCircuitDispatchRevisionRebuildQueryIsKeysetBounded(t *testing.T) {
	for _, fragment := range []string{"id > $1::text", "dispatch_revision >= 1", "ORDER BY id ASC", "LIMIT $2"} {
		if !strings.Contains(listGatewayAccountCircuitDispatchRevisionsSQL, fragment) {
			t.Fatalf("dispatch revision rebuild query missing %q", fragment)
		}
	}
	if strings.Contains(strings.ToUpper(listGatewayAccountCircuitDispatchRevisionsSQL), "OFFSET") {
		t.Fatal("dispatch revision rebuild must use keyset pagination")
	}
}
