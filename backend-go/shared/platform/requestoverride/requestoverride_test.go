package requestoverride

import (
	"reflect"
	"testing"
)

// T19：子集视图必须由真源派生（chat 去 none、自定义模型去 default）。
func TestSubsetHelpersDeriveFromSingleSource(t *testing.T) {
	if got := ReasoningEffortsWithout(FamilyOpenAI, "none"); !reflect.DeepEqual(got, []string{"minimal", "low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("openai reasoning without none = %v", got)
	}
	if got := ServiceTiersWithout(FamilyOpenAI, "default"); !reflect.DeepEqual(got, []string{"priority", "flex"}) {
		t.Fatalf("openai tiers without default = %v", got)
	}
	if got := ReasoningEffortSetWithout(FamilyOpenAI, "none"); got["none"] || !got["max"] || len(got) != 6 {
		t.Fatalf("reasoning set = %v", got)
	}
	if got := ServiceTierSetWithout(FamilyOpenAI, "default"); got["default"] || !got["priority"] || !got["flex"] || len(got) != 2 {
		t.Fatalf("tier set = %v", got)
	}
	// 返回副本：调用方修改不污染真源。
	efforts := ReasoningEffortsWithout(FamilyOpenAI)
	efforts[0] = "mutated"
	if ReasoningEfforts(FamilyOpenAI)[0] != "none" {
		t.Fatal("subset view must be a copy")
	}
	set := ReasoningEffortSet(FamilyOpenAI)
	set["mutated"] = true
	if ReasoningEffortSet(FamilyOpenAI)["mutated"] {
		t.Fatal("set view must be a copy")
	}
}
