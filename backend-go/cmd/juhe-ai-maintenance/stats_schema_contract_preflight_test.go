package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestStatsSchemaContractPreflightCommandRequiresPostgresURL(t *testing.T) {
	t.Setenv("JUHE_AI_POSTGRES_URL", "")
	var combined bytes.Buffer
	cmd := newStatsSchemaContractPreflightCommand()
	cmd.SetOut(&combined)
	cmd.SetErr(&combined)

	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_POSTGRES_URL is required") {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
}

func TestStatsSchemaContractPreflightCommandRejectsArgumentsBeforeConnecting(t *testing.T) {
	t.Setenv("JUHE_AI_POSTGRES_URL", "postgres://unused.invalid/db")
	cmd := newStatsSchemaContractPreflightCommand()
	cmd.SetArgs([]string{"unexpected"})

	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("ExecuteContext() error = nil, want argument error")
	}
}
