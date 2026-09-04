package gatewayaccounteffects

import "log/slog"

// Logger mirrors the consumed Node logger.info/warn/error surface:
// logger.info(fields, message). Fields carry the `event` key exactly like the
// Node pino calls migrated here.
type Logger interface {
	Info(fields map[string]any, message string)
	Warn(fields map[string]any, message string)
	Error(fields map[string]any, message string)
}

// NopLogger drops every record.
type NopLogger struct{}

// Info implements Logger.
func (NopLogger) Info(map[string]any, string) {}

// Warn implements Logger.
func (NopLogger) Warn(map[string]any, string) {}

// Error implements Logger.
func (NopLogger) Error(map[string]any, string) {}

type slogLogger struct{ logger *slog.Logger }

// SlogLogger adapts *slog.Logger to Logger; the `event` field is promoted to
// the structured attribute so log records stay greppable like the Node pino
// output.
func SlogLogger(logger *slog.Logger) Logger {
	if logger == nil {
		return NopLogger{}
	}
	return slogLogger{logger: logger}
}

func (a slogLogger) Info(fields map[string]any, message string) { a.logger.Info(message, slogFields(fields)...) }
func (a slogLogger) Warn(fields map[string]any, message string) { a.logger.Warn(message, slogFields(fields)...) }
func (a slogLogger) Error(fields map[string]any, message string) { a.logger.Error(message, slogFields(fields)...) }

func slogFields(fields map[string]any) []any {
	if len(fields) == 0 {
		return nil
	}
	attrs := make([]any, 0, len(fields))
	for key, value := range fields {
		attrs = append(attrs, slog.Any(key, value))
	}
	return attrs
}
