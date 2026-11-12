package main

import (
	"strings"
	"testing"
)

func TestPurgeNonChatGateValidation(t *testing.T) {
	cases := []struct {
		name    string
		gate    purgeNonChatGate
		wantErr string
	}{
		{name: "missing dsn", gate: purgeNonChatGate{ManifestOut: "m.json"}, wantErr: "--dsn"},
		{name: "non postgres dsn", gate: purgeNonChatGate{DSN: "mysql://x", ManifestOut: "m.json"}, wantErr: "postgres://"},
		{name: "missing manifest", gate: purgeNonChatGate{DSN: "postgres://x"}, wantErr: "--manifest-out"},
		{name: "dry run ok", gate: purgeNonChatGate{DSN: "postgres://x", ManifestOut: "m.json", DryRun: true}},
		{name: "apply missing stop", gate: purgeNonChatGate{DSN: "postgres://x", ManifestOut: "m.json"}, wantErr: "--stop-write-confirmed"},
		{name: "apply missing backup", gate: purgeNonChatGate{DSN: "postgres://x", ManifestOut: "m.json", StopWriteConfirmed: true}, wantErr: "--backup-confirmed"},
		{name: "apply ok", gate: purgeNonChatGate{DSN: "postgres://x", ManifestOut: "m.json", StopWriteConfirmed: true, BackupConfirmed: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.gate.validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}
