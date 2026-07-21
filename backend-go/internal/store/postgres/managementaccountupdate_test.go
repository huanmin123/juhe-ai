package postgres

import (
	"strings"
	"testing"
)

func TestManagementAccountUpdateSQLHasCASAndOwnershipGuards(t *testing.T) {
	for _, required := range []string{
		"config_revision = config_revision + 1",
		"config_revision = $4",
		"FOR UPDATE",
		"provider_protocol_profiles",
		"groups.provider_code = current_target.provider_code",
		"credentials_encrypted",
		"RETURNING",
	} {
		if !strings.Contains(managementAccountUpdateSQL, required) {
			t.Fatalf("management account update SQL missing %q", required)
		}
	}
}
