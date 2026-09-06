package accounts

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// CPA YAML input support mirrors parseCpaInput in
// account-import-source-adapters.ts: CLIProxyAPI string inputs decode through
// the YAML parser (JSON is a YAML subset), closing the previously deferred
// "YAML 导入暂未支持" slice.

// parseCpaInput mirrors parseCpaInput. Non-string payloads (already decoded
// JSON objects from the request body) pass through untouched; string payloads
// must decode as YAML. Like the Node `yaml` parse helper, only a single
// document is accepted. It returns (nil, false) only on decode failure, after
// recording the Node error message on the source summary.
func parseCpaInput(value any, state *adapterState) (any, bool) {
	text, ok := value.(string)
	if !ok {
		return value, true
	}
	decoder := yaml.NewDecoder(strings.NewReader(text))
	var parsed any
	// An empty stream decodes to nil (like the Node parseYaml("") -> null
	// path) so the caller reports 来源导入内容必须是对象.
	if err := decoder.Decode(&parsed); err != nil && !errors.Is(err, io.EOF) {
		addSourceMessage(state, "CLIProxyAPI 导入内容必须是有效 YAML 或 JSON")
		return nil, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		addSourceMessage(state, "CLIProxyAPI 导入内容必须是有效 YAML 或 JSON")
		return nil, false
	}
	return normalizeYAMLValue(parsed), true
}

// normalizeYAMLValue converts the yaml.v3 decode tree into the JSON-shaped
// values the adapters expect. json.Unmarshal hands every number through as
// float64 (the Node runtime is all-doubles), while yaml.v3 emits int/int64/
// uint64/float32; YAML timestamps decode as time.Time where the Node `yaml`
// parser keeps core-schema timestamps as strings rendered through isoDate.
func normalizeYAMLValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeYAMLValue(item)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[yamlKeyString(key)] = normalizeYAMLValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = normalizeYAMLValue(item)
		}
		return out
	case int:
		return float64(typed)
	case int32:
		return float64(typed)
	case int64:
		return float64(typed)
	case uint:
		return float64(typed)
	case uint64:
		return float64(typed)
	case float32:
		return float64(typed)
	case time.Time:
		return typed.UTC().Format("2006-01-02T15:04:05.000") + "Z"
	default:
		return value
	}
}

// yamlKeyString renders non-string mapping keys the way JavaScript object
// keys coerce (numbers, booleans).
func yamlKeyString(key any) string {
	if text, ok := key.(string); ok {
		return text
	}
	return fmt.Sprintf("%v", key)
}
