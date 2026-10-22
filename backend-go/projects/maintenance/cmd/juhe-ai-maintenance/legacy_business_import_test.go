package main

import "testing"

// TestLegacyBusinessImportUsageGates pins the CLI's usage contract: the command
// refuses to run without a legacy DSN, a target DSN or the decrypting secret,
// instead of opening a half-configured connection.
func TestLegacyBusinessImportUsageGates(t *testing.T) {
	cases := []struct {
		name                      string
		dryRun                    bool
		legacyDSN, target, secret string
	}{
		{"missing legacy dsn", false, "", "postgres://t", "s"},
		{"missing target dsn", false, "postgres://l", "", "s"},
		{"missing secret", true, "postgres://l", "postgres://t", ""},
		{"blank legacy dsn", true, "   ", "postgres://t", "s"},
	}
	for _, tc := range cases {
		if code := legacyBusinessImportResult(tc.dryRun, tc.legacyDSN, tc.target, tc.secret); code != 2 {
			t.Fatalf("%s: exit=%d, want 2 (usage error)", tc.name, code)
		}
	}
}
