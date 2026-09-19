package gatewayaccounteffects

// w11c: logger adapter 覆盖。queue eviction / local-clear guards /
// key-model recovery helpers 原属 Node side-effect 死半区，已随
// PLAN-20260919T000723744Z 任务 B 删除。

import (
	"strings"
	"testing"
)

func TestW11CNopLoggerAcceptsAll(t *testing.T) {
	logger := NopLogger{}
	logger.Info(map[string]any{"k": 1}, "info")
	logger.Warn(map[string]any{"k": 1}, "warn")
	logger.Error(map[string]any{"k": 1}, "error")
}

func TestW11CMustJSONAndTextHelpers(t *testing.T) {
	if got := mustJSON(map[string]int{"a": 1}); got == "" || !strings.Contains(got, "\"a\"") {
		t.Fatalf("mustJSON = %s", got)
	}
}

func stringPtr(value string) *string { return &value }
