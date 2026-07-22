package httpapi

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestParsePublicAPIQueryUsesExtendedBracketShape(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]any
	}{
		{
			name: "nested object",
			raw:  "filter%5Bname%5D=alice&filter%5Bstatus%5D=active",
			want: map[string]any{"filter": map[string]any{"name": "alice", "status": "active"}},
		},
		{
			name: "repeated and array keys",
			raw:  "tag=a&tag=b&items%5B%5D=x&items%5B%5D=y",
			want: map[string]any{"tag": []any{"a", "b"}, "items": []any{"x", "y"}},
		},
		{
			name: "indexed array compacts holes",
			raw:  "items%5B0%5D=a&items%5B2%5D=c",
			want: map[string]any{"items": []any{"a", "c"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parsePublicAPIQuery(test.raw); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parsePublicAPIQuery(%q) = %#v, want %#v", test.raw, got, test.want)
			}
		})
	}
}

func TestParsePublicAPIQueryKeepsMalformedEscapesAsRawValues(t *testing.T) {
	want := map[string]any{"bad": "%E0 A", "alsoBad": "%ZZ"}
	got := parsePublicAPIQuery("bad=%E0+A&alsoBad=%ZZ")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsePublicAPIQuery malformed escapes = %#v, want %#v", got, want)
	}
}

func TestParsePublicAPIQueryDecodesBracketsBeforeKeepingMalformedKeyEscapes(t *testing.T) {
	got := parsePublicAPIQuery("a%5Bb%5D%ZZ=x&%5Broot%5D%ZZ=y&value=%5Bx%5D%ZZ&a=x%5D=y")
	want := map[string]any{
		"a":     map[string]any{"b": "x"},
		"root":  "y",
		"value": "[x]%ZZ",
		"a=x]":  "y",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("malformed key escapes = %#v, want %#v", got, want)
	}
}

func TestParsePublicAPIQueryBoundsDepthAndArrayIndexes(t *testing.T) {
	got := parsePublicAPIQuery("deep%5Ba%5D%5Bb%5D%5Bc%5D%5Bd%5D%5Be%5D%5Bf%5D%5Bg%5D=value&arr%5B20%5D=outside&arr%5B19%5D=inside")
	want := map[string]any{
		"deep": map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": map[string]any{"e": map[string]any{"[f][g]": "value"}}}}}},
		"arr":  map[string]any{"20": "outside", "19": "inside"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bounded query = %#v, want %#v", got, want)
	}
}

func TestParsePublicAPIQueryDropsPrototypePathSegments(t *testing.T) {
	got := parsePublicAPIQuery("constructor%5Bprototype%5D%5Bpolluted%5D=yes&safe=value&unsafe%5B__proto__%5D%5Bpolluted%5D=yes")
	want := map[string]any{
		"constructor": map[string]any{"prototype": map[string]any{"polluted": "yes"}},
		"safe":        "value",
		"unsafe":      map[string]any{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prototype-safe query = %#v, want %#v", got, want)
	}
}

func TestParsePublicAPIQueryUsesQSCombineRulesForMixedShapes(t *testing.T) {
	tests := []struct {
		raw  string
		want map[string]any
	}{
		{
			raw:  "a=1&a%5Bb%5D=2",
			want: map[string]any{"a": []any{"1", map[string]any{"b": "2"}}},
		},
		{
			raw:  "a%5Bb%5D=2&a=1",
			want: map[string]any{"a": map[string]any{"1": true, "b": "2"}},
		},
		{
			raw:  "a%5B%5D=1&a%5Bb%5D=2",
			want: map[string]any{"a": map[string]any{"0": "1", "b": "2"}},
		},
		{
			raw:  "a%5B%5D%5Bb%5D=1&a%5B%5D%5Bb%5D=2&a%5B%5D%5Bc%5D=3",
			want: map[string]any{"a": []any{map[string]any{"b": []any{"1", "2"}, "c": "3"}}},
		},
	}

	for _, test := range tests {
		if got := parsePublicAPIQuery(test.raw); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("parsePublicAPIQuery(%q) = %#v, want %#v", test.raw, got, test.want)
		}
	}
}

func TestParsePublicAPIQueryHonorsParameterLimitWithoutCombiningRemainder(t *testing.T) {
	parts := make([]string, publicAPIQueryParameterLimit+1)
	for index := range parts {
		parts[index] = "item" + strconv.Itoa(index) + "=value" + strconv.Itoa(index)
	}
	got := parsePublicAPIQuery(strings.Join(parts, "&"))
	if len(got) != publicAPIQueryParameterLimit {
		t.Fatalf("parsed parameter count = %d, want %d", len(got), publicAPIQueryParameterLimit)
	}
	if got["item999"] != "value999" || got["item1000"] != nil {
		t.Fatalf("parameter limit result = %#v", got)
	}
}

func TestParsePublicAPIQueryConvertsRepeatedValuesBeyondQSArrayLimit(t *testing.T) {
	parts := make([]string, publicAPIQueryArrayLimit+2)
	for index := range parts {
		parts[index] = "tag=" + strconv.Itoa(index)
	}
	got := parsePublicAPIQuery(strings.Join(parts, "&"))
	tag, ok := got["tag"].(map[string]any)
	if !ok {
		t.Fatalf("tag = %#v, want qs overflow object", got["tag"])
	}
	if len(tag) != publicAPIQueryArrayLimit+2 || tag["0"] != "0" || tag["20"] != "20" || tag["21"] != "21" {
		t.Fatalf("overflow tag = %#v", tag)
	}

	for index := range parts {
		parts[index] = "tag%5B%5D=" + strconv.Itoa(index)
	}
	got = parsePublicAPIQuery(strings.Join(parts, "&"))
	tag, ok = got["tag"].(map[string]any)
	if !ok || len(tag) != publicAPIQueryArrayLimit+2 || tag["21"] != "21" {
		t.Fatalf("overflow append tag = %#v", got["tag"])
	}
}

func TestParsePublicAPIQueryCombinesRepeatedExplicitArrayIndexInPlace(t *testing.T) {
	got := parsePublicAPIQuery("a%5B0%5D=x&a%5B1%5D=y&a%5B0%5D=z")
	want := map[string]any{"a": []any{[]any{"x", "z"}, "y"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit index merge = %#v, want %#v", got, want)
	}
}

func TestParsePublicAPIQueryMatchesQSBracketScanning(t *testing.T) {
	got := parsePublicAPIQuery("a%5Bb%5Dx%5Bc%5D=1&%5Broot%5D=2&%5B%5D=3&weird%5B%5Bb%5D%5D=4")
	want := map[string]any{
		"a":      map[string]any{"b": map[string]any{"c": "1"}},
		"root":   "2",
		"0":      "3",
		"weird[": map[string]any{"b": "4"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bracket scanning = %#v, want %#v", got, want)
	}
}

func TestParsePublicAPIQueryUsesJavaScriptIntegerKeyEnumerationOrder(t *testing.T) {
	for _, raw := range []string{"%5B0%5D=a&0=b", "0=b&%5B0%5D=a"} {
		got := parsePublicAPIQuery(raw)
		want := map[string]any{"0": []any{"b", "a"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("parsePublicAPIQuery(%q) = %#v, want %#v", raw, got, want)
		}
	}
}

func TestParsePublicAPIQueryPreservesOverflowMarkerAcrossMixedShapes(t *testing.T) {
	appendParts := make([]string, publicAPIQueryArrayLimit+1)
	wantOverflow := map[string]any{}
	wantScalarFirst := map[string]any{"0": "x"}
	for index := range appendParts {
		appendParts[index] = "a%5B%5D=" + strconv.Itoa(index)
		wantOverflow[strconv.Itoa(index)] = strconv.Itoa(index)
		wantScalarFirst[strconv.Itoa(index+1)] = strconv.Itoa(index)
	}
	wantAppendFirst := mergeTestMap(wantOverflow, "21", "x")

	tests := []struct {
		raw  string
		want map[string]any
	}{
		{raw: strings.Join(append([]string{"a=x"}, appendParts...), "&"), want: map[string]any{"a": wantScalarFirst}},
		{raw: strings.Join(append(appendParts, "a=x"), "&"), want: map[string]any{"a": wantAppendFirst}},
		{raw: strings.Join(append([]string{"a%5Bb%5D=x"}, appendParts...), "&"), want: map[string]any{"a": mergeTestMap(wantOverflow, "b", "x")}},
	}
	for _, test := range tests {
		if got := parsePublicAPIQuery(test.raw); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("parsePublicAPIQuery mixed overflow = %#v, want %#v", got, test.want)
		}
	}
}

func TestParsePublicAPIQueryMarksExplicitIndexesAtOrBeyondArrayLimitAsOverflow(t *testing.T) {
	tests := []struct {
		raw  string
		want map[string]any
	}{
		{raw: "a%5B20%5D=z&a=x", want: map[string]any{"a": map[string]any{"20": "z", "21": "x"}}},
		{raw: "a%5B999%5D=z&a=x", want: map[string]any{"a": map[string]any{"999": "z", "1000": "x"}}},
		{raw: "a=x&a%5B20%5D=z", want: map[string]any{"a": map[string]any{"0": "x", "21": "z"}}},
	}
	for _, test := range tests {
		if got := parsePublicAPIQuery(test.raw); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("parsePublicAPIQuery(%q) = %#v, want %#v", test.raw, got, test.want)
		}
	}
}

func mergeTestMap(source map[string]any, key string, value any) map[string]any {
	out := make(map[string]any, len(source)+1)
	for sourceKey, sourceValue := range source {
		out[sourceKey] = sourceValue
	}
	out[key] = value
	return out
}
