// zod.go holds the zod v3 message shims (locales/en.cjs) shared by the
// schema mirrors across domains.
package policyreads

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// zod v3 message shims (locales/en.cjs) shared by the schema mirrors.
// ---------------------------------------------------------------------------

const zodRequired = "Required"

func zodReceived(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64, int, int64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

func zodInvalidType(expected string, value any) string {
	return "Expected " + expected + ", received " + zodReceived(value)
}

func zodEnumMessage(options []string, received string) string {
	quoted := make([]string, len(options))
	for i, option := range options {
		quoted[i] = "'" + option + "'"
	}
	return "Invalid enum value. Expected " + strings.Join(quoted, " | ") + ", received '" + received + "'"
}

func zodStringMin(n int) string {
	return fmt.Sprintf("String must contain at least %d character(s)", n)
}

func zodStringMax(n int) string {
	return fmt.Sprintf("String must contain at most %d character(s)", n)
}

func zodArrayMin(n int) string {
	return fmt.Sprintf("Array must contain at least %d element(s)", n)
}

func zodArrayMax(n int) string {
	return fmt.Sprintf("Array must contain at most %d element(s)", n)
}

func zodNumberMin(n int) string {
	return fmt.Sprintf("Number must be greater than or equal to %d", n)
}

func zodNumberMax(n int) string {
	return fmt.Sprintf("Number must be less than or equal to %d", n)
}

func zodUnrecognizedKeys(keys []string) string {
	sorted := append([]string{}, keys...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return "Unrecognized key(s) in object: " + strings.Join(sorted, ", ")
}

// asStringList bridges zod-transformed []any lists and already-normalized
// []string values inside match normalization.
func asStringList(value any) ([]string, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, true
	case []any:
		out := make([]string, len(typed))
		for i, item := range typed {
			text, isString := item.(string)
			if !isString {
				return nil, false
			}
			out[i] = text
		}
		return out, true
	case []string:
		return typed, true
	default:
		return nil, false
	}
}
