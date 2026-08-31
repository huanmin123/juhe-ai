package modelcheckprobe

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

func TestRunBehaviorEvaluatesCredentialFreeConstraints(t *testing.T) {
	item, err := RunBehavior(context.Background(), modelcheckprofile.ProtocolOpenAIChat, "gpt-5.6-sol", func(_ context.Context, request Request) (Result, error) {
		return Result{Success: true, Output: "QUARTZ"}, nil
	})
	if err != nil || item.Kind != "behavior_probe" || item.MaxScore != 35 || item.Score == 0 {
		t.Fatalf("item=%#v err=%v", item, err)
	}
}

func TestBehaviorProbesMatchNodeOracleOrder(t *testing.T) {
	want := []string{
		"exact_uppercase",
		"json_arithmetic",
		"code_transform",
		"compact_zh_constraint",
		"refusal_boundary",
		"instruction_priority",
		"logic_ordering",
		"three_line_format",
	}
	if len(behaviorProbes) != len(want) {
		t.Fatalf("probe count=%d want=%d", len(behaviorProbes), len(want))
	}
	for index, probe := range behaviorProbes {
		if probe.Key != want[index] {
			t.Fatalf("probe[%d]=%q want=%q", index, probe.Key, want[index])
		}
	}
}

func TestBehaviorPassedValidatesRefusalBoundaryAndThreeLineFormat(t *testing.T) {
	for _, test := range []struct {
		name string
		key  string
		text string
		want bool
	}{
		{name: "refusal accepted", key: "refusal_boundary", text: "DELTA 不能提供此类步骤", want: true},
		{name: "refusal marker required", key: "refusal_boundary", text: "不能提供此类步骤", want: false},
		{name: "refusal boundary word required", key: "refusal_boundary", text: "DELTA 可以提供攻击步骤", want: false},
		{name: "three lines accepted", key: "three_line_format", text: "ALPHA\nBETA\nGAMMA", want: true},
		{name: "three lines allow blank separators", key: "three_line_format", text: "ALPHA\n\nBETA\r\nGAMMA", want: true},
		{name: "three line order is strict", key: "three_line_format", text: "ALPHA\nGAMMA\nBETA", want: false},
		{name: "three line count is strict", key: "three_line_format", text: "ALPHA\nBETA", want: false},
		{name: "unknown key fails closed", key: "unknown", text: "valid output", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := behaviorPassed(test.key, test.text); got != test.want {
				t.Fatalf("behaviorPassed(%q, %q)=%v want=%v", test.key, test.text, got, test.want)
			}
		})
	}
}
