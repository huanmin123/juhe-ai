package pgcfg

import (
	"os"
	"testing"
)

func TestPrepareProcessEnv(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "pg-pass")
	t.Setenv("PGPASSWORD", "")
	t.Setenv("PGAPPNAME", "")
	t.Setenv("PGTZ", "")
	PrepareProcessEnv("juhe-ai-gateway")
	if got := os.Getenv("PGPASSWORD"); got != "pg-pass" {
		t.Fatalf("PGPASSWORD = %q", got)
	}
	if got := os.Getenv("PGAPPNAME"); got != "juhe-ai-gateway" {
		t.Fatalf("PGAPPNAME = %q", got)
	}
	if got := os.Getenv("PGTZ"); got != "UTC" {
		t.Fatalf("PGTZ = %q", got)
	}
}

func TestPrepareProcessEnvPinsTimeZone(t *testing.T) {
	t.Setenv("PGTZ", "Asia/Shanghai")
	PrepareProcessEnv("juhe-ai-gateway")
	if got := os.Getenv("PGTZ"); got != "UTC" {
		t.Fatalf("PGTZ = %q, want UTC", got)
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
