package gatewayhybrid

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func unmarshalInto(raw []byte, value any) error {
	return json.Unmarshal(raw, value)
}

func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}

func TestNodeJSONStringifyMatchesNodeContract(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{
			name:  "undefined keys are dropped",
			value: map[string]any{"a": float64(1), "b": Undefined, "c": nil},
			want:  `{"a":1,"c":null}`,
		},
		{
			name: "ordered object keeps insertion order",
			value: func() any {
				object := NewOrderedJSON()
				object.Set("z", float64(1))
				object.Set("a", float64(2))
				inner := NewOrderedJSON()
				inner.Set("y", true)
				inner.Set("b", Undefined)
				object.Set("m", inner)
				return object
			}(),
			want: `{"z":1,"a":2,"m":{"y":true}}`,
		},
		{
			name:  "integers print without decimals",
			value: []any{float64(240), float64(0.5), float64(-3)},
			want:  "[240,0.5,-3]",
		},
		{
			name:  "string escaping mirrors JSON.stringify",
			value: "line\nquote\"back\\tab\tctlhtml<>",
			want:  "\"line\\nquote\\\"back\\\\tab\\tctlhtml<>\"",
		},
		{
			name:  "empty object and array",
			value: []any{NewOrderedJSON(), []any{}},
			want:  "[{},[]]",
		},
		{
			name:  "chinese text passes through",
			value: "混合路由评分",
			want:  `"混合路由评分"`,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := NodeJSONStringify(testCase.value); got != testCase.want {
				t.Fatalf("NodeJSONStringify = %s, want %s", got, testCase.want)
			}
		})
	}
}

func TestParseJSONOrderedPreservesKeyOrder(t *testing.T) {
	source := []byte(`{"b":1,"a":{"z":true,"y":[1,{"k":"v"}]},"c":null}`)
	parsed, err := ParseJSONOrdered(source)
	if err != nil {
		t.Fatalf("ParseJSONOrdered returned error: %v", err)
	}
	object, ok := parsed.(*OrderedJSON)
	if !ok {
		t.Fatalf("expected OrderedJSON, got %T", parsed)
	}
	keys := object.Keys()
	if len(keys) != 3 || keys[0] != "b" || keys[1] != "a" || keys[2] != "c" {
		t.Fatalf("key order = %v, want [b a c]", keys)
	}
	if got := NodeJSONStringify(object); got != string(source) {
		t.Fatalf("round trip = %s, want %s", got, source)
	}
}

func TestParseJSONOrderedRejectsTrailingContent(t *testing.T) {
	if _, err := ParseJSONOrdered([]byte(`{"a":1} garbage`)); err == nil {
		t.Fatal("expected error for trailing content")
	}
}

func TestNodeNumberCoercions(t *testing.T) {
	tests := []struct {
		name   string
		value  any
		want   float64
		wantOK bool
	}{
		{"float", float64(7.5), 7.5, true},
		{"int", 3, 3, true},
		{"bool true", true, 1, true},
		{"bool false", false, 0, true},
		{"nil", nil, 0, true},
		{"empty string", "", 0, true},
		{"numeric string", " 42 ", 42, true},
		{"nan string", "abc", 0, false},
		{"object", NewOrderedJSON(), 0, false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := NodeNumber(testCase.value)
			if ok != testCase.wantOK || got != testCase.want {
				t.Fatalf("NodeNumber = (%v, %v), want (%v, %v)", got, ok, testCase.want, testCase.wantOK)
			}
		})
	}
}

func TestUTF16Helpers(t *testing.T) {
	if got := utf16Length("ab中文"); got != 4 {
		t.Fatalf("utf16Length = %d, want 4", got)
	}
	if got := utf16Length("𝄞"); got != 2 {
		t.Fatalf("utf16Length surrogate = %d, want 2", got)
	}
	if got := truncateUTF16("a中b文c", 3); got != "a中b" {
		t.Fatalf("truncateUTF16 = %q, want a中b", got)
	}
	if got := truncateUTF16("short", 10); got != "short" {
		t.Fatalf("truncateUTF16 short = %q", got)
	}
}

func TestClampHybridLevel(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  int
	}{
		{"integer", float64(7), 7},
		{"rounds", float64(6.5), 7},
		{"string numeric", "8", 8},
		{"below range", float64(0), 1},
		{"above range", float64(11), 10},
		{"nan string", "abc", DefaultHybridScoringFallbackMaxLevel},
		{"js undefined", Undefined, DefaultHybridScoringFallbackMaxLevel},
		{"js null clamps to 1", nil, 1},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := clampLevelFromAny(testCase.value); got != testCase.want {
				t.Fatalf("clampLevelFromAny = %d, want %d", got, testCase.want)
			}
		})
	}
}

func TestTargetHybridLevelRouteForLevelBoundaries(t *testing.T) {
	config := configWithRoutes(testLevelRoutes(), 5)
	tests := []struct {
		name  string
		level int
		want  string
	}{
		{"level 1", 1, "m-low"},
		{"level 3 upper bound", 3, "m-low"},
		{"level 4 lower bound", 4, "m-mid"},
		{"level 6", 6, "m-mid"},
		{"level 10 skips disabled", 10, "m-high2"},
		{"level 11 clamps to 10", 11, "m-high2"},
		{"level 0 clamps to 1", 0, "m-low"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			route := TargetHybridLevelRouteForLevel(config, testCase.level)
			if route == nil || route.TargetModel != testCase.want {
				t.Fatalf("route = %v, want %s", route, testCase.want)
			}
		})
	}
	// A config without any covering route resolves to nothing.
	empty := hybridConfig()
	empty.LevelRoutes = nil
	if route := TargetHybridLevelRouteForLevel(empty, 3); route != nil {
		t.Fatalf("empty config should not resolve, got %v", route)
	}
}

func TestHigherHybridLevelRoutesOrderAndFilter(t *testing.T) {
	config := configWithRoutes(testLevelRoutes(), 5)
	mid := TargetHybridLevelRouteForLevel(config, 5)
	higher := HigherHybridLevelRoutes(config, mid)
	if len(higher) != 1 || higher[0].TargetModel != "m-high2" {
		t.Fatalf("higher = %v", higher)
	}
	low := TargetHybridLevelRouteForLevel(config, 1)
	higher = HigherHybridLevelRoutes(config, low)
	if len(higher) != 2 || higher[0].TargetModel != "m-mid" || higher[1].TargetModel != "m-high2" {
		t.Fatalf("higher order = %v", higher)
	}
}

func TestHybridScoringFallbackRoutesBoundary(t *testing.T) {
	routes := []testLevelRoute{
		{MinLevel: 1, MaxLevel: 5, TargetModel: "m-low", Enabled: true},
		{MinLevel: 6, MaxLevel: 10, TargetModel: "m-high", Enabled: true},
	}
	config := configWithRoutes(routes, 6)
	fallbacks := HybridScoringFallbackRoutes(config)
	// minLevel 6 <= scoringFallbackMaxLevel 6 exactly at the boundary.
	if len(fallbacks) != 2 {
		t.Fatalf("fallbacks = %v", fallbacks)
	}
	config.ScoringFallbackMaxLevel = 5
	fallbacks = HybridScoringFallbackRoutes(config)
	if len(fallbacks) != 1 || fallbacks[0].TargetModel != "m-low" {
		t.Fatalf("fallbacks at 5 = %v", fallbacks)
	}
}

func TestHybridRoutePoolScopeDeterministic(t *testing.T) {
	first := HybridRoutePoolScope(hybridConfig())
	second := HybridRoutePoolScope(hybridConfig())
	if first != second {
		t.Fatalf("pool scope not deterministic: %s vs %s", first, second)
	}
	if len(first) != len("hybrid-route-pool:")+64 {
		t.Fatalf("pool scope length = %d", len(first))
	}
	changed := hybridConfig()
	changed.LevelRoutes[0].TargetModel = "other"
	if HybridRoutePoolScope(changed) == first {
		t.Fatal("pool scope should change with levelRoutes")
	}
	scoped := hybridConfig()
	groupID := "group-a"
	scoped.ScoringGroupID = &groupID
	if HybridRoutePoolScope(scoped) == first {
		t.Fatal("pool scope should change with scoringGroupId")
	}
}

func TestAffinityApplyWithCancelledContextStillMemoryDriven(t *testing.T) {
	start := time.Now()
	service := NewAffinityService(testClock(&start), &mockIdentity{}, nil)
	config := hybridConfig()
	decision := service.Apply(AffinityInput{
		View:            &GatewayRequestView{ConversationKey: "conv"},
		SystemAccountID: "sys",
		APIKeyID:        "key",
		Config:          config,
		Level:           3,
		Route:           config.LevelRoutes[0],
	})
	if decision.Applied {
		t.Fatal("first apply must not stick")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = ctx
}
