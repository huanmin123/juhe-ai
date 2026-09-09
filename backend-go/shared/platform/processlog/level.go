// Package processlog ports the shared logging runtime policy the Node backend
// installed at process level (shared/logger.ts installProcessLogHandlers,
// config/runtime.ts logLevelConfig and shared/request-context.ts
// isGatewayTimingDetailSampled / shouldWriteGatewayStageDetail):
//
//   - the JUHE_AI_LOG_LEVEL contract (trace..silent) driving the slog level of
//     both binaries,
//   - the process resilience contract (a broken stdout/stderr pipe must keep
//     the service alive and drop the log write; an unrecovered panic is logged
//     as fatal and exits 1 like the Node uncaughtException branch),
//   - the gateway timing-detail sampling gate (performance mode samples stage
//     details by a stable traceId hash instead of writing every slow stage).
package processlog

import (
	"fmt"
	"log/slog"
	"strings"
)

// LogLevelEnvName is the JUHE_AI_LOG_LEVEL environment variable (Node
// config/runtime.ts:886 logLevelConfig('JUHE_AI_LOG_LEVEL', 'info')).
const LogLevelEnvName = "JUHE_AI_LOG_LEVEL"

// slog level mapping for the two levels slog has no record for. Go never
// emits trace-level records, so trace keeps every debug+ record visible
// (Node trace ⊃ debug); silent sits above Error so nothing is rendered.
const (
	LevelTrace  = slog.LevelDebug - 4
	LevelSilent = slog.LevelError + 4
)

// ParseLevel mirrors logLevelConfig (config/runtime.ts:1318-1323): the value
// is trimmed and lower-cased, empty falls back to the caller default and any
// other value fails with the Node startup error.
func ParseLevel(raw string, fallback slog.Level) (slog.Level, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return fallback, nil
	}
	switch value {
	case "trace":
		return LevelTrace, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	case "fatal":
		return slog.LevelError, nil
	case "silent":
		return LevelSilent, nil
	}
	return 0, fmt.Errorf("%s 只能配置为 trace/debug/info/warn/error/fatal/silent", LogLevelEnvName)
}

// LoadLevel reads JUHE_AI_LOG_LEVEL with the Node default (info) and the
// startup-fail contract for invalid values.
func LoadLevel(getenv func(string) string) (slog.Level, error) {
	raw := ""
	if getenv != nil {
		raw = getenv(LogLevelEnvName)
	}
	return ParseLevel(raw, slog.LevelInfo)
}
