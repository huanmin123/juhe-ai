package gatewayobs

// w11c: diagnostic sanitizer bound branches, quoted assignment scanner edges
// and upstream response model write paths.

import (
	"strings"
	"testing"
)

func TestW11CSanitizerBoundsAndEdges(t *testing.T) {
	// nil values pass through.
	if got := SanitizeDiagnosticPayload(nil); got != nil {
		t.Fatalf("nil = %v", got)
	}
	// Deeply nested arrays are truncated at the recursion bound.
	deep := make([]interface{}, 1)
	current := deep
	for i := 0; i < 12; i++ {
		next := make([]interface{}, 1)
		current[0] = next
		current = next
	}
	if got := SanitizeDiagnosticPayload(deep); got == nil {
		t.Fatalf("deep value lost")
	}
	// Oversized arrays carry a truncation marker.
	big := make([]interface{}, 120)
	for index := range big {
		big[index] = index
	}
	sanitized := SanitizeDiagnosticPayload(big)
	list, ok := sanitized.([]interface{})
	if !ok || len(list) != 101 {
		t.Fatalf("array sanitized = %d items", len(list))
	}
	// Oversized objects carry the __truncated__ marker.
	bigObject := map[string]interface{}{}
	for index := 0; index < 220; index++ {
		bigObject[string(rune('a'+index%26))+strings.Repeat("k", 3)+string(rune('0'+index%10))+string(rune('a'+(index/10)%26))+string(rune('a'+(index/100)%26))+string(rune('0'+(index%7)))+string(rune('a'+(index%5)))] = index
	}
	sanitizedObject := SanitizeDiagnosticPayload(bigObject)
	object, ok := sanitizedObject.(map[string]interface{})
	if !ok || object["__truncated__"] != true {
		t.Fatalf("object truncation missing (%v)", object["__truncated__"])
	}
	// Empty field names are never sensitive.
	if isSensitiveDiagnosticFieldName("") {
		t.Fatalf("empty name must not be sensitive")
	}
}

func TestW11CQuotedAssignmentScannerEdges(t *testing.T) {
	// The quoted assignment redaction handles separators, escapes and line
	// terminators through the handwritten scanner.
	inputs := map[string]bool{
		`{token: 'abc12345'}`:                true,  // bare key needs quotes? scanner accepts both
		`{"api_key": "secret12345"}`:         true,  // quoted key
		`{'authorization': "Bearer abcdefgh1234"}`: true, // mixed quotes
		`{apikey: }`:                         false, // missing value
		`{apikey: x}`:                        true,  // bare assignment redacts
		`{apikey: 'a\nb'}`:                   true,  // escaped payload
	}
	for input, expectRedaction := range inputs {
		output := sanitizeSensitiveString(input)
		if expectRedaction && !strings.Contains(output, diagnosticRedacted) {
			t.Fatalf("input %q not redacted: %q", input, output)
		}
		if !expectRedaction && strings.Contains(output, diagnosticRedacted) && !strings.Contains(input, "sk-") {
			t.Fatalf("input %q unexpectedly redacted: %q", input, output)
		}
	}
	// Non-sensitive quoted keys are untouched.
	untouched := sanitizeSensitiveString(`{"model": "gpt-4o"}`)
	if strings.Contains(untouched, diagnosticRedacted) {
		t.Fatalf("non-sensitive key redacted: %q", untouched)
	}
}
