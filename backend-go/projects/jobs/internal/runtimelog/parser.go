package runtimelog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

type LineOptions struct {
	SourceKey  string
	LogFile    string
	LogOffset  int64
	LineNumber int64
	Now        func() time.Time
}

func ParseLine(rawLine string, options LineOptions) *Record {
	line := strings.TrimSpace(rawLine)
	if line == "" {
		return nil
	}
	now := options.now()
	metadata := Record{LogFile: strings.TrimSpace(options.LogFile), LogOffset: options.LogOffset, LineNumber: options.LineNumber}
	var value any
	if err := json.Unmarshal([]byte(line), &value); err != nil {
		return fallbackRecord(line, options, metadata, "运行日志行不是有效 JSON", now)
	}
	if _, ok := value.(map[string]any); !ok {
		return fallbackRecord(line, options, metadata, "运行日志行不是 JSON 对象", now)
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &parsed); err != nil {
		return fallbackRecord(line, options, metadata, "运行日志行不是有效 JSON", now)
	}
	timestamp := readString(parsed["time"])
	return &Record{
		ID:           stableID(sourceKey(options, line)),
		LogFile:      metadata.LogFile,
		LogOffset:    metadata.LogOffset,
		LineNumber:   metadata.LineNumber,
		Time:         timestamp,
		Level:        normalizeLevel(parsed["level"]),
		TraceID:      readString(parsed["traceId"]),
		Event:        readString(parsed["event"]),
		Message:      firstNonEmpty(readString(parsed["msg"]), readString(parsed["message"])),
		ErrorMessage: firstNonEmpty(readString(parsed["errorMessage"]), errorMessage(parsed["err"])),
		RawJSON:      line,
		CreatedAt:    timestamp,
	}
}

func fallbackRecord(raw string, options LineOptions, metadata Record, errorMessage string, now time.Time) *Record {
	timestamp := nodeISO(now)
	return &Record{
		ID:           stableID(sourceKey(options, raw)),
		LogFile:      metadata.LogFile,
		LogOffset:    metadata.LogOffset,
		LineNumber:   metadata.LineNumber,
		Time:         timestamp,
		Level:        "warn",
		Event:        "runtime_log_parse_failed",
		Message:      "运行日志文件包含无法解析的完整行",
		ErrorMessage: errorMessage,
		RawJSON:      raw,
		CreatedAt:    timestamp,
	}
}

func sourceKey(options LineOptions, fallback string) string {
	if strings.TrimSpace(options.SourceKey) != "" {
		return options.SourceKey
	}
	return fallback
}

func stableID(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "rtlog_" + hex.EncodeToString(digest[:])[:32]
}

func normalizeLevel(value json.RawMessage) string {
	if stringValue := readString(value); stringValue != "" {
		return strings.ToLower(stringValue)
	}
	var numeric float64
	if err := json.Unmarshal(value, &numeric); err != nil {
		return "info"
	}
	switch {
	case numeric >= 60:
		return "fatal"
	case numeric >= 50:
		return "error"
	case numeric >= 40:
		return "warn"
	case numeric >= 30:
		return "info"
	case numeric >= 20:
		return "debug"
	default:
		return "trace"
	}
}

func readString(value json.RawMessage) string {
	var text string
	if len(value) == 0 || json.Unmarshal(value, &text) != nil {
		return ""
	}
	return strings.TrimSpace(text)
}

func errorMessage(value json.RawMessage) string {
	var object map[string]json.RawMessage
	if len(value) == 0 || json.Unmarshal(value, &object) != nil {
		return ""
	}
	return readString(object["message"])
}

func (options LineOptions) now() time.Time {
	if options.Now != nil {
		return options.Now()
	}
	return time.Now().UTC()
}
