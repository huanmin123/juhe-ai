package main

import "testing"

func TestMigrateMediaPricingUsageGates(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want int
	}{
		{name: "help", argv: []string{"--help"}, want: 0},
		{name: "missing dsn", argv: nil, want: 2},
		{name: "dry run needs manifest", argv: []string{"--dsn", "postgres://u:p@127.0.0.1:5432/db", "--dry-run"}, want: 2},
		{name: "apply needs confirmations", argv: []string{"--dsn", "postgres://u:p@127.0.0.1:5432/db"}, want: 2},
		{name: "apply needs stop write", argv: []string{"--dsn", "postgres://u:p@127.0.0.1:5432/db", "--backup-confirmed"}, want: 2},
		{name: "dispatch missing dsn", argv: []string{"migrate-media-pricing"}, want: 2},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var got int
			if testCase.name == "dispatch missing dsn" {
				got = runMaintenance(testCase.argv)
			} else {
				got = runMigrateMediaPricingCommand(testCase.argv)
			}
			if got != testCase.want {
				t.Fatalf("exit = %d, want %d", got, testCase.want)
			}
		})
	}
}
