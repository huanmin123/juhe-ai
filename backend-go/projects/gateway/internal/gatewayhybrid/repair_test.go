package gatewayhybrid

import (
	"strings"
	"testing"
)

func qualityOutcomeWithResult(result *HybridQualityScoreResult) *HybridQualityInspectionOutcome {
	return &HybridQualityInspectionOutcome{Triggered: true, Result: result}
}

func TestBuildHybridQualityRepairInstructionLines(t *testing.T) {
	outcome := qualityOutcomeWithResult(&HybridQualityScoreResult{
		Pass:                false,
		Score:               20,
		FailureType:         "missing_required_output",
		HasFailureType:      true,
		Reason:              strPtr("缺少结论"),
		RetryRecommendation: "upgrade_next_level",
	})
	text := BuildHybridQualityRepairInstruction(outcome)
	lines := strings.Split(text, "\n")
	if len(lines) != 7 {
		t.Fatalf("line count = %d:\n%s", len(lines), text)
	}
	if lines[0] != "上一次回答没有通过混合路由质量评分。请基于原始用户需求重新给出最终答案，不要解释评分过程。" {
		t.Fatalf("line 0 = %s", lines[0])
	}
	if lines[2] != "- 问题类型：missing_required_output" {
		t.Fatalf("line 2 = %s", lines[2])
	}
	if lines[3] != "- 质量分：20" {
		t.Fatalf("line 3 = %s", lines[3])
	}
	if lines[4] != "- 失败原因：缺少结论" {
		t.Fatalf("line 4 = %s", lines[4])
	}
	if lines[5] != "- 评分建议：upgrade_next_level" {
		t.Fatalf("line 5 = %s", lines[5])
	}
	if !strings.HasPrefix(lines[6], "修复要求：补齐遗漏内容") {
		t.Fatalf("line 6 = %s", lines[6])
	}

	// Empty reason/failureType drop (JS falsy filter): 5 lines remain.
	minimal := qualityOutcomeWithResult(&HybridQualityScoreResult{Score: 0, RetryRecommendation: "accept"})
	minimalText := BuildHybridQualityRepairInstruction(minimal)
	if strings.Count(minimalText, "\n") != 4 {
		t.Fatalf("minimal lines:\n%s", minimalText)
	}
	if strings.Contains(minimalText, "- 失败原因") {
		t.Fatal("empty reason must be dropped")
	}
}

func TestBuildHybridQualityRepairInstructionTruncatesAt2000UTF16(t *testing.T) {
	long := strings.Repeat("中", 3000)
	outcome := qualityOutcomeWithResult(&HybridQualityScoreResult{Score: 0, Reason: strPtr(long)})
	text := BuildHybridQualityRepairInstruction(outcome)
	// 2000 UTF-16 units + "..." — Chinese chars are single UTF-16 units.
	if utf16Length(text) != hybridQualityRepairInstructionMaxChars+3 {
		t.Fatalf("utf16 length = %d", utf16Length(text))
	}
	if !strings.HasSuffix(text, "...") {
		t.Fatal("truncation suffix missing")
	}
	if !strings.Contains(text, strings.Repeat("中", 1900)) {
		t.Fatal("kept content truncated too early")
	}
}

func TestAppendHybridQualityRepairInstructionBodyVariants(t *testing.T) {
	instructionContained := func(body *OrderedJSON) bool {
		return strings.Contains(NodeJSONStringify(body), "上一次回答没有通过混合路由质量评分")
	}
	outcome := qualityOutcomeWithResult(&HybridQualityScoreResult{Score: 33, RetryRecommendation: "retry_same_model"})

	t.Run("messages array append", func(t *testing.T) {
		view := &GatewayRequestView{RawBody: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)}
		body, changed := AppendHybridQualityRepairInstruction(view, outcome)
		if !changed || !instructionContained(body) {
			t.Fatalf("changed = %v body = %s", changed, NodeJSONStringify(body))
		}
		messages := OrderedChildArray(body, "messages")
		if len(messages) != 2 {
			t.Fatalf("messages = %d", len(messages))
		}
		entry, _ := messages[1].(*OrderedJSON)
		if OrderedString(entry, "role") != "user" {
			t.Fatalf("entry = %s", NodeJSONStringify(entry))
		}
		// Original body object keys keep their order.
		if body.Keys()[0] != "model" {
			t.Fatalf("keys = %v", body.Keys())
		}
	})

	t.Run("input string append", func(t *testing.T) {
		view := &GatewayRequestView{RawBody: []byte(`{"input":"原输入"}`)}
		body, changed := AppendHybridQualityRepairInstruction(view, outcome)
		if !changed {
			t.Fatal("expected change")
		}
		input, _ := body.Get("input")
		text, _ := input.(string)
		if !strings.HasPrefix(text, "原输入\n\n") || !strings.Contains(text, "- 质量分：33") {
			t.Fatalf("input = %q", text)
		}
	})

	t.Run("input array append", func(t *testing.T) {
		view := &GatewayRequestView{RawBody: []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)}
		body, changed := AppendHybridQualityRepairInstruction(view, outcome)
		if !changed {
			t.Fatal("expected change")
		}
		entries := OrderedChildArray(body, "input")
		if len(entries) != 2 {
			t.Fatalf("entries = %d", len(entries))
		}
		if !instructionContained(body) {
			t.Fatal("instruction missing")
		}
	})

	t.Run("no mutable surface returns false", func(t *testing.T) {
		view := &GatewayRequestView{RawBody: []byte(`{"unrelated":1}`)}
		if _, changed := AppendHybridQualityRepairInstruction(view, outcome); changed {
			t.Fatal("unrelated body must not change")
		}
		empty := &GatewayRequestView{}
		if _, changed := AppendHybridQualityRepairInstruction(empty, outcome); changed {
			t.Fatal("empty body must not change")
		}
	})

	t.Run("quality without result returns false", func(t *testing.T) {
		view := &GatewayRequestView{RawBody: []byte(`{"messages":[]}`)}
		if _, changed := AppendHybridQualityRepairInstruction(view, &HybridQualityInspectionOutcome{}); changed {
			t.Fatal("outcome without result must not change")
		}
		if _, changed := AppendHybridQualityRepairInstruction(view, nil); changed {
			t.Fatal("nil outcome must not change")
		}
	})

	t.Run("input numeric value not touched", func(t *testing.T) {
		view := &GatewayRequestView{RawBody: []byte(`{"input":42}`)}
		if _, changed := AppendHybridQualityRepairInstruction(view, outcome); changed {
			t.Fatal("numeric input must not change")
		}
	})
}
