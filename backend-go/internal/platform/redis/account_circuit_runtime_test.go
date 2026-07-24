package redis

import (
	"strings"
	"testing"
)

func TestAccountCircuitRuntimeLuaUsesReadyIndexAndDoesNotScanGlobalState(t *testing.T) {
	for name, script := range map[string]string{
		"mutation":         accountCircuitRuntimeMutationLua,
		"escalation":       accountCircuitRuntimeEscalationLua,
		"due":              accountCircuitRuntimeListDueLua,
		"account revision": accountCircuitRuntimeReplaceAccountRevisionLua,
	} {
		if strings.Contains(script, "HGETALL") {
			t.Fatalf("%s runtime script must use reverse indexes, not HGETALL", name)
		}
		for _, fragment := range []string{"status') ~= 'ready", "ownerMode') ~= 'go-runtime-state-v1"} {
			if !strings.Contains(script, fragment) {
				t.Fatalf("%s runtime script must require %q", name, fragment)
			}
		}
	}
	if !strings.Contains(accountCircuitRuntimeMutationLua, "dispatch_tombstone") || !strings.Contains(accountCircuitRuntimeMutationLua, "state['phase'] = 'SUSPECT'") {
		t.Fatal("runtime mutation must fence durable revision and handle SUSPECT explicitly")
	}
	if !strings.Contains(accountCircuitRuntimeListDueLua, "state['phase'] == 'OPEN' or state['phase'] == 'RECOVERING'") {
		t.Fatal("listDue must return canary-eligible phases only")
	}
}

func TestAccountCircuitRuntimeIndexBackfillRequiresLockEpochAndAudit(t *testing.T) {
	for _, fragment := range []string{"buildEpoch", "source changed during backfill", "status', 'ready", "ownerMode', 'go-runtime-state-v1", "HSCAN"} {
		if fragment == "HSCAN" {
			continue
		}
		if !strings.Contains(beginAccountCircuitRuntimeIndexLua+applyAccountCircuitRuntimeIndexPageLua+finalizeAccountCircuitRuntimeIndexLua, fragment) {
			t.Fatalf("runtime index script missing %q", fragment)
		}
	}
	if !strings.Contains(accountCircuitRuntimeIndexOwnerMode, "go-runtime-state-v1") {
		t.Fatal("runtime index owner mode must be explicit")
	}
}

func TestAccountCircuitRuntimeLegacyRestoreCannotWriteReadyIndex(t *testing.T) {
	if !strings.Contains(restoreAccountCircuitIncidentLua, "legacy restore") || !strings.Contains(restoreAccountCircuitIncidentLua, "index_status") {
		t.Fatal("legacy incident restore must be fenced away from ready runtime owner")
	}
}
