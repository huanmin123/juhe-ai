// w14d_zod_unit_test.go pins every zod-mirror parser and the hasAnyField
// refine across all of their branches: required/optional/nullable string
// chains, query string/enum/int coercion, body bool/int/enum fields, strict
// key checks and the zod issue message builders.
package aipublic

import (
	"net/url"
	"strings"
	"testing"
)

func TestW14dZodIssueMessages(t *testing.T) {
	if zodReceived(nil) != "null" || zodReceived(true) != "boolean" || zodReceived("s") != "string" ||
		zodReceived(1.5) != "number" || zodReceived([]any{}) != "array" || zodReceived(map[string]any{}) != "object" ||
		zodReceived(struct{}{}) != "unknown" {
		t.Fatal("zodReceived type names")
	}
	if zodInvalidType("string", 7) != "Expected string, received number" {
		t.Fatalf("invalid type: %s", zodInvalidType("string", 7))
	}
	if zodStringMin(2) != "String must contain at least 2 character(s)" {
		t.Fatal("string min")
	}
	if zodStringMax(4) != "String must contain at most 4 character(s)" {
		t.Fatal("string max")
	}
	if zodNumberMin(1) != "Number must be greater than or equal to 1" {
		t.Fatal("number min")
	}
	if zodNumberMax(9) != "Number must be less than or equal to 9" {
		t.Fatal("number max")
	}
	if got := zodEnumMessage([]string{"a", "b"}, "c"); got != "Invalid enum value. Expected 'a' | 'b', received 'c'" {
		t.Fatalf("enum message: %s", got)
	}
	if got := zodUnrecognizedKeys("z", "a", "m"); got != "Unrecognized key(s) in object: a, m, z" {
		t.Fatalf("unrecognized keys: %s", got)
	}
}

func TestW14dParseQueryString(t *testing.T) {
	values := url.Values{"k": {"  hello  "}}
	if text, issue := parseQueryString(values, "k", true, 1, 10); issue != "" || text != "hello" {
		t.Fatalf("trim: %q %q", text, issue)
	}
	if _, issue := parseQueryString(url.Values{}, "k", true, 1, 0); issue != "Required" {
		t.Fatalf("required: %q", issue)
	}
	if text, issue := parseQueryString(url.Values{}, "k", false, 1, 0); issue != "" || text != "" {
		t.Fatalf("optional absent: %q %q", text, issue)
	}
	if _, issue := parseQueryString(values, "k", true, 10, 0); issue != zodStringMin(10) {
		t.Fatalf("too short: %q", issue)
	}
	if _, issue := parseQueryString(values, "k", true, 1, 3); issue != zodStringMax(3) {
		t.Fatalf("too long: %q", issue)
	}
}

func TestW14dParseOptionalQueryString(t *testing.T) {
	if _, present, issue := parseOptionalQueryString(url.Values{}, "k", 1, 0); present || issue != "" {
		t.Fatalf("absent: %v %q", present, issue)
	}
	if _, present, issue := parseOptionalQueryString(url.Values{"k": {" "}}, "k", 1, 0); present || issue != zodStringMin(1) {
		t.Fatalf("blank: %v %q", present, issue)
	}
	if text, present, issue := parseOptionalQueryString(url.Values{"k": {" v "}}, "k", 0, 0); !present || issue != "" || text != "v" {
		t.Fatalf("present: %q %v %q", text, present, issue)
	}
	if _, present, issue := parseOptionalQueryString(url.Values{"k": {"abcdef"}}, "k", 1, 2); present || issue != zodStringMax(2) {
		t.Fatalf("too long: %v %q", present, issue)
	}
	if _, present, issue := parseOptionalQueryString(url.Values{"k": {"a"}}, "k", 3, 0); present || issue != zodStringMin(3) {
		t.Fatalf("too short: %v %q", present, issue)
	}
}

func TestW14dParseOptionalQueryEnum(t *testing.T) {
	options := []string{"a", "b"}
	if _, present, issue := parseOptionalQueryEnum(url.Values{}, "k", options); present || issue != "" {
		t.Fatalf("absent: %v %q", present, issue)
	}
	if text, present, issue := parseOptionalQueryEnum(url.Values{"k": {"b"}}, "k", options); !present || issue != "" || text != "b" {
		t.Fatalf("present: %q %v %q", text, present, issue)
	}
	if _, present, issue := parseOptionalQueryEnum(url.Values{"k": {"z"}}, "k", options); present || issue != zodEnumMessage(options, "z") {
		t.Fatalf("invalid: %v %q", present, issue)
	}
}

func TestW14dParseOptionalQueryInt(t *testing.T) {
	if _, present, issue := parseOptionalQueryInt(url.Values{}, "k", 1, 0); present || issue != "" {
		t.Fatalf("absent: %v %q", present, issue)
	}
	// Blank collapses through Number('') === 0.
	if _, present, issue := parseOptionalQueryInt(url.Values{"k": {" "}}, "k", 1, 0); present || issue != zodNumberMin(1) {
		t.Fatalf("blank below min: %v %q", present, issue)
	}
	if v, present, issue := parseOptionalQueryInt(url.Values{"k": {" "}}, "k", 0, 0); !present || issue != "" || v != 0 {
		t.Fatalf("blank zero: %d %v %q", v, present, issue)
	}
	if v, present, issue := parseOptionalQueryInt(url.Values{"k": {"7"}}, "k", 1, 10); !present || issue != "" || v != 7 {
		t.Fatalf("int: %d %v %q", v, present, issue)
	}
	if _, present, issue := parseOptionalQueryInt(url.Values{"k": {"abc"}}, "k", 1, 0); present || issue != "Expected number, received nan" {
		t.Fatalf("nan: %v %q", present, issue)
	}
	if _, present, issue := parseOptionalQueryInt(url.Values{"k": {"1.5"}}, "k", 1, 0); present || issue != "Expected integer, received float" {
		t.Fatalf("float: %v %q", present, issue)
	}
	if _, present, issue := parseOptionalQueryInt(url.Values{"k": {"0"}}, "k", 1, 0); present || issue != zodNumberMin(1) {
		t.Fatalf("below min: %v %q", present, issue)
	}
	if _, present, issue := parseOptionalQueryInt(url.Values{"k": {"11"}}, "k", 1, 10); present || issue != zodNumberMax(10) {
		t.Fatalf("above max: %v %q", present, issue)
	}
}

func TestW14dCoerceNumber(t *testing.T) {
	if v, ok := coerceNumber("42"); !ok || v != 42 {
		t.Fatalf("int coerce: %v %v", v, ok)
	}
	if v, ok := coerceNumber("1.25"); !ok || v == 1.25 {
		// The float arm returns the float64 value.
		if f, isFloat := v.(float64); !ok || !isFloat || f != 1.25 {
			t.Fatalf("float coerce: %v %v", v, ok)
		}
	}
	if _, ok := coerceNumber("x"); ok {
		t.Fatal("nan must fail")
	}
}

func TestW14dBodyHelpers(t *testing.T) {
	body := map[string]any{"s": " x ", "n": nil, "f": 1.5, "b": true, "e": "opt2"}
	if !bodyHas(body, "s") || bodyHas(body, "missing") {
		t.Fatal("bodyHas")
	}
	if text, ok := bodyString("v"); !ok || text != "v" {
		t.Fatal("bodyString")
	}
	if _, ok := bodyString(7); ok {
		t.Fatal("bodyString rejects non-string")
	}

	// trimmedBodyString arms.
	if got, issue := trimmedBodyString(" x ", true, 1, 5); issue != "" || got == nil || *got != "x" {
		t.Fatalf("trim: %v %q", got, issue)
	}
	if got, issue := trimmedBodyString(nil, false, 1, 5); got != nil || issue != "" {
		t.Fatalf("absent: %v %q", got, issue)
	}
	if _, issue := trimmedBodyString(7, true, 1, 5); issue != zodInvalidType("string", 7) {
		t.Fatalf("type: %q", issue)
	}
	if _, issue := trimmedBodyString("  ", true, 1, 5); issue != zodStringMin(1) {
		t.Fatalf("min: %q", issue)
	}
	if _, issue := trimmedBodyString("abcdef", true, 1, 3); issue != zodStringMax(3) {
		t.Fatalf("max: %q", issue)
	}

	// nullableTrimmedBodyString arms.
	if got, issue := nullableTrimmedBodyString(nil, true, 5); got != nil || issue != "" {
		t.Fatalf("null: %v %q", got, issue)
	}
	if got, issue := nullableTrimmedBodyString(nil, false, 5); got != nil || issue != "" {
		t.Fatalf("absent: %v %q", got, issue)
	}
	if got, issue := nullableTrimmedBodyString(" v ", true, 5); issue != "" || got == nil || *got != "v" {
		t.Fatalf("value: %v %q", got, issue)
	}
	if _, issue := nullableTrimmedBodyString(7, true, 5); issue != zodInvalidType("string", 7) {
		t.Fatalf("type: %q", issue)
	}
	if _, issue := nullableTrimmedBodyString("abcdef", true, 3); issue != zodStringMax(3) {
		t.Fatalf("max: %q", issue)
	}

	// bodyOptionalString / Bool / Int / Enum arms.
	if text, present, issue := bodyOptionalString("v", true); !present || text != "v" || issue != "" {
		t.Fatalf("string: %q %v %q", text, present, issue)
	}
	if _, present, issue := bodyOptionalString(7, true); present || issue != zodInvalidType("string", 7) {
		t.Fatalf("string type: %v %q", present, issue)
	}
	if _, present, issue := bodyOptionalString(nil, false); present || issue != "" {
		t.Fatalf("string absent: %v %q", present, issue)
	}
	if flag, present, issue := bodyOptionalBool(true, true); !present || !flag || issue != "" {
		t.Fatalf("bool: %v %v %q", flag, present, issue)
	}
	if _, present, issue := bodyOptionalBool("yes", true); present || issue != zodInvalidType("boolean", "yes") {
		t.Fatalf("bool type: %v %q", present, issue)
	}
	if v, present, issue := bodyOptionalInt(float64(3), true, 1, 5); !present || v != 3 || issue != "" {
		t.Fatalf("int: %d %v %q", v, present, issue)
	}
	if _, present, issue := bodyOptionalInt(float64(1.5), true, 1, 5); present || issue != "Expected integer, received float" {
		t.Fatalf("int float: %v %q", present, issue)
	}
	if _, present, issue := bodyOptionalInt(float64(0), true, 1, 5); present || issue != zodNumberMin(1) {
		t.Fatalf("int min: %v %q", present, issue)
	}
	if _, present, issue := bodyOptionalInt(float64(9), true, 1, 5); present || issue != zodNumberMax(5) {
		t.Fatalf("int max: %v %q", present, issue)
	}
	if _, present, issue := bodyOptionalInt("3", true, 1, 5); present || issue != zodInvalidType("number", "3") {
		t.Fatalf("int type: %v %q", present, issue)
	}
	if text, present, issue := bodyOptionalEnum("opt2", true, []string{"opt1", "opt2"}); !present || text != "opt2" || issue != "" {
		t.Fatalf("enum: %q %v %q", text, present, issue)
	}
	if _, present, issue := bodyOptionalEnum("nope", true, []string{"opt1"}); present || issue != zodEnumMessage([]string{"opt1"}, "nope") {
		t.Fatalf("enum invalid: %v %q", present, issue)
	}
	if _, present, issue := bodyOptionalEnum(7, true, []string{"opt1"}); present || issue != zodInvalidType("string", 7) {
		t.Fatalf("enum type: %v %q", present, issue)
	}
}

func TestW14dStrictObjectKeysAndWrappers(t *testing.T) {
	body := map[string]any{"a": 1, "c": 2, "z": 3}
	if unknown := strictObjectKeys(body, "a", "c", "z"); unknown != nil {
		t.Fatalf("known keys: %v", unknown)
	}
	if unknown := strictObjectKeys(body, "a"); len(unknown) != 2 || unknown[0] != "c" || unknown[1] != "z" {
		t.Fatalf("unknown keys: %v", unknown)
	}

	// requiredTrimmedBody arms.
	if text, issue := requiredTrimmedBody(map[string]any{"k": " v "}, "k", 1, 5); issue != "" || text != "v" {
		t.Fatalf("required trim: %q %q", text, issue)
	}
	if _, issue := requiredTrimmedBody(map[string]any{}, "k", 1, 5); issue != "Required" {
		t.Fatalf("required absent: %q", issue)
	}
	if _, issue := requiredTrimmedBody(map[string]any{"k": 7}, "k", 1, 5); issue != zodInvalidType("string", 7) {
		t.Fatalf("required type: %q", issue)
	}
	if _, issue := requiredTrimmedBody(map[string]any{"k": ""}, "k", 1, 5); issue != zodStringMin(1) {
		t.Fatalf("required min: %q", issue)
	}

	// optionalTrimmedBody arms (explicit null is invalid_type).
	if got, issue := optionalTrimmedBody(map[string]any{}, "k", 1, 5); got != nil || issue != "" {
		t.Fatalf("optional absent: %v %q", got, issue)
	}
	if _, issue := optionalTrimmedBody(map[string]any{"k": nil}, "k", 1, 5); issue != zodInvalidType("string", nil) {
		t.Fatalf("optional null: %q", issue)
	}
	if got, issue := optionalTrimmedBody(map[string]any{"k": " v "}, "k", 1, 5); issue != "" || got == nil || *got != "v" {
		t.Fatalf("optional value: %v %q", got, issue)
	}

	// nullableTrimmedBodyField / bodyOptionalBoolField / bodyOptionalEnumField.
	if got, issue := nullableTrimmedBodyField(map[string]any{"k": nil}, "k", 5); got != nil || issue != "" {
		t.Fatalf("nullable field null: %v %q", got, issue)
	}
	if got, issue := nullableTrimmedBodyField(map[string]any{}, "k", 5); got != nil || issue != "" {
		t.Fatalf("nullable field absent: %v %q", got, issue)
	}
	if flag, present, issue := bodyOptionalBoolField(map[string]any{"k": true}, "k"); !present || !flag || issue != "" {
		t.Fatalf("bool field: %v %v %q", flag, present, issue)
	}
	if _, present, issue := bodyOptionalBoolField(map[string]any{}, "k"); present || issue != "" {
		t.Fatalf("bool field absent: %v %q", present, issue)
	}
	if _, present, issue := bodyOptionalBoolField(map[string]any{"k": "x"}, "k"); present || issue != zodInvalidType("boolean", "x") {
		t.Fatalf("bool field type: %v %q", present, issue)
	}
	if text, present, issue := bodyOptionalEnumField(map[string]any{"k": "a"}, "k", []string{"a"}); !present || text != "a" || issue != "" {
		t.Fatalf("enum field: %q %v %q", text, present, issue)
	}
	if _, present, issue := bodyOptionalEnumField(map[string]any{}, "k", []string{"a"}); present || issue != "" {
		t.Fatalf("enum field absent: %v %q", present, issue)
	}
	if _, present, issue := bodyOptionalEnumField(map[string]any{"k": "b"}, "k", []string{"a"}); present || issue != zodEnumMessage([]string{"a"}, "b") {
		t.Fatalf("enum field invalid: %v %q", present, issue)
	}
}

func TestW14dHasAnyField(t *testing.T) {
	body := map[string]any{"a": 1, "b": nil}
	if !hasAnyField(body, []string{"a"}) {
		t.Fatal("present key counts")
	}
	if !hasAnyField(body, []string{"x", "b"}) {
		t.Fatal("a nil value still counts as present")
	}
	if hasAnyField(body, []string{"x", "y"}) {
		t.Fatal("missing keys must not match")
	}
	if hasAnyField(body, nil) {
		t.Fatal("empty key list must not match")
	}
}

func TestW14dRuneLenAndSortStrings(t *testing.T) {
	if runeLen("héllo") != 5 {
		t.Fatalf("runeLen: %d", runeLen("héllo"))
	}
	got := []string{"b", "a", "c"}
	sortStrings(got)
	if strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("sortStrings: %v", got)
	}
}
