package pgcfg

import (
	"os"
	"testing"
)

func TestPrepareProcessEnv(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "pg-pass")
	t.Setenv("PGPASSWORD", "")
	t.Setenv("PGAPPNAME", "")
	PrepareProcessEnv("juhe-ai-gateway")
	if got := os.Getenv("PGPASSWORD"); got != "pg-pass" {
		t.Fatalf("PGPASSWORD = %q", got)
	}
	if got := os.Getenv("PGAPPNAME"); got != "juhe-ai-gateway" {
		t.Fatalf("PGAPPNAME = %q", got)
	}
}

func TestPrepareProcessEnvKeepsExplicitValues(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "pg-pass")
	t.Setenv("PGPASSWORD", "explicit")
	t.Setenv("PGAPPNAME", "explicit-app")
	PrepareProcessEnv("juhe-ai-gateway")
	if got := os.Getenv("PGPASSWORD"); got != "explicit" {
		t.Fatalf("PGPASSWORD = %q", got)
	}
	if got := os.Getenv("PGAPPNAME"); got != "explicit-app" {
		t.Fatalf("PGAPPNAME = %q", got)
	}
}
