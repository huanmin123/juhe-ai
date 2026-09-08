package statreads

import "testing"

// D-102 (BUG-0175 wave 4 W4-E): the db-service:* replica family joins the
// valid read-side role vocabulary so db-service replica samples survive the
// process_event_loop_samples filter.
func TestIsValidProcessRoleIncludesDBServicePrefixFamily(t *testing.T) {
	valid := []string{
		"db-service:1", "db-service:2", "db-service:12",
		"gateway:1", "control:1", "control-replica:2",
		"stats-worker:8", "ingest-worker:64", "ops-worker:3",
		"server", "db-service",
	}
	for _, role := range valid {
		if !isValidProcessRole(role) {
			t.Errorf("isValidProcessRole(%q) = false, want true", role)
		}
	}
	invalid := []string{
		"db-service:",
		"ingest-worker:0", "ingest-worker:65", "stats-worker:1x",
		"unknown-role", "gateway:", "",
	}
	for _, role := range invalid {
		if isValidProcessRole(role) {
			t.Errorf("isValidProcessRole(%q) = true, want false", role)
		}
	}
}
