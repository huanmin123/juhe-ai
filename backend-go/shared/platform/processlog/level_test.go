package processlog

import (
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		fallback slog.Level
		want     slog.Level
		wantErr  bool
	}{
		{name: "empty falls back", raw: "", fallback: slog.LevelInfo, want: slog.LevelInfo},
		{name: "whitespace falls back", raw: "   ", fallback: slog.LevelWarn, want: slog.LevelWarn},
		{name: "trace", raw: "trace", want: LevelTrace},
		{name: "debug", raw: "debug", want: slog.LevelDebug},
		{name: "info", raw: "info", want: slog.LevelInfo},
		{name: "warn", raw: "warn", want: slog.LevelWarn},
		{name: "error", raw: "error", want: slog.LevelError},
		{name: "fatal", raw: "fatal", want: slog.LevelError},
		{name: "silent", raw: "silent", want: LevelSilent},
		{name: "upper case accepted", raw: "WARN", want: slog.LevelWarn},
		{name: "padded accepted", raw: "  info  ", want: slog.LevelInfo},
		{name: "invalid rejected", raw: "verbose", wantErr: true},
		{name: "empty quotes rejected", raw: `""`, wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ParseLevel(testCase.raw, testCase.fallback)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("ParseLevel(%q) expected error, got level %d", testCase.raw, got)
				}
				if err.Error() != "JUHE_AI_LOG_LEVEL 只能配置为 trace/debug/info/warn/error/fatal/silent" {
					t.Fatalf("ParseLevel(%q) unexpected error text: %v", testCase.raw, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseLevel(%q) unexpected error: %v", testCase.raw, err)
			}
			if got != testCase.want {
				t.Fatalf("ParseLevel(%q) = %d, want %d", testCase.raw, got, testCase.want)
			}
		})
	}
}

func TestLoadLevel(t *testing.T) {
	t.Run("default is info", func(t *testing.T) {
		got, err := LoadLevel(func(string) string { return "" })
		if err != nil {
			t.Fatalf("LoadLevel unexpected error: %v", err)
		}
		if got != slog.LevelInfo {
			t.Fatalf("LoadLevel default = %d, want info(%d)", got, slog.LevelInfo)
		}
	})
	t.Run("reads the env contract", func(t *testing.T) {
		got, err := LoadLevel(func(name string) string {
			if name != "JUHE_AI_LOG_LEVEL" {
				t.Fatalf("LoadLevel read unexpected env %q", name)
			}
			return "debug"
		})
		if err != nil {
			t.Fatalf("LoadLevel unexpected error: %v", err)
		}
		if got != slog.LevelDebug {
			t.Fatalf("LoadLevel = %d, want debug(%d)", got, slog.LevelDebug)
		}
	})
	t.Run("invalid value fails startup", func(t *testing.T) {
		if _, err := LoadLevel(func(string) string { return "loud" }); err == nil {
			t.Fatal("LoadLevel(loud) expected error")
		}
	})
}

func TestLevelMappingCoversSlogRecords(t *testing.T) {
	// trace must keep debug records visible (record level >= handler level);
	// silent must suppress error records — the two mapped levels have no
	// slog record of their own.
	if slog.LevelDebug < LevelTrace {
		t.Fatalf("trace level %d must keep debug records %d visible", LevelTrace, slog.LevelDebug)
	}
	if slog.LevelError >= LevelSilent {
		t.Fatalf("silent level %d must suppress error records %d", LevelSilent, slog.LevelError)
	}
}
