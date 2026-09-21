package modelcheckquestionbank

import (
	"strings"
	"testing"
)

func TestNormalizeText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空串", "", ""},
		{"纯空白与标点归零", " ！！ 。， \t\n ", ""},
		{"中文去标点空白", "你好，世界！ 欢迎。", "你好世界欢迎"},
		{"英文小写化", "Hello World", "helloworld"},
		{"全角半角 NFKC 归一", "ＡＢＣａｂｃ：１２３", "abcabc123"},
		{"去符号", "a+b=c", "abc"},
		{"中英混合", "什么是 GPT-5.6 模型？", "什么是gpt56模型"},
		{"emoji 符号剔除", "模型🔥测试", "模型测试"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeText(tc.in); got != tc.want {
				t.Fatalf("NormalizeText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeTextPunctuationAndWhitespaceEquivalence(t *testing.T) {
	// 标点/空白/全半角差异不得影响归一化结果（标题查重的精确相等即依赖它）。
	if NormalizeText("什么是模型降智？") != NormalizeText("什么是模型降智") {
		t.Fatal("标点差异未被归一化剔除")
	}
	if NormalizeText("Hello,   World!") != NormalizeText("helloworld") {
		t.Fatal("空白/标点/大小写差异未被归一化剔除")
	}
	if NormalizeText("（ＡＢ）") != NormalizeText("ab") {
		t.Fatal("全角括号与全角字母未被 NFKC/标点规则归一")
	}
}

func TestBigramSet(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want map[string]struct{}
	}{
		{"空串", "", map[string]struct{}{}},
		{"单字符退化为自身", "甲", setOf("甲")},
		{"中文二元组", "你好世界", setOf("你好", "好世", "世界")},
		{"英文二元组", "abcd", setOf("ab", "bc", "cd")},
		{"归一化后构造", "A B,CD", setOf("ab", "bc", "cd")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BigramSet(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("BigramSet(%q) size = %d, want %d (%v)", tc.in, len(got), len(tc.want), got)
			}
			for value := range tc.want {
				if _, ok := got[value]; !ok {
					t.Fatalf("BigramSet(%q) missing %q (%v)", tc.in, value, got)
				}
			}
		})
	}
}

func setOf(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func TestJaccard(t *testing.T) {
	cases := []struct {
		name string
		a    map[string]struct{}
		b    map[string]struct{}
		want float64
	}{
		{"完全相同", setOf("ab", "bc"), setOf("ab", "bc"), 1},
		{"完全不同", setOf("ab"), setOf("cd"), 0},
		{"空集与空集", map[string]struct{}{}, map[string]struct{}{}, 0},
		{"空集与非空", map[string]struct{}{}, setOf("ab"), 0},
		{"四分之五边界恰为阈值", setOf("ab", "bc", "cd", "de"), setOf("ab", "bc", "cd", "de", "ed"), 0.8},
		{"四分之三低于阈值", setOf("ab", "bc", "cd"), setOf("ab", "bc", "cd", "de"), 0.75},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Jaccard(tc.a, tc.b); got != tc.want {
				t.Fatalf("Jaccard = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFindMostSimilar(t *testing.T) {
	candidates := []StoredQuestionText{
		{ID: "q-poem", Title: "静夜思作者", Text: "床前明月光疑是地上霜"},
		{ID: "q-math", Title: "加法题", Text: "请计算 137 乘以 24 的结果"},
		{ID: "q-english", Title: "翻译题", Text: "Translate the following sentence into Chinese please"},
	}
	cases := []struct {
		name        string
		text        string
		candidates  []StoredQuestionText
		wantFound   bool
		wantBestID  string
		wantBestAny string // 期望最相似候选标题包含的子串，空表示不校验
	}{
		{
			name:       "无候选不命中",
			text:       "任何题目",
			candidates: nil,
			wantFound:  false,
		},
		{
			name:        "完全相同命中且返回自身",
			text:        "请计算 137 乘以 24 的结果",
			candidates:  candidates,
			wantFound:   true,
			wantBestID:  "q-math",
			wantBestAny: "加法题",
		},
		{
			name:       "标点空白差异不影响命中",
			text:       "请计算137乘以24的结果！",
			candidates: candidates,
			wantFound:  true,
			wantBestID: "q-math",
		},
		{
			name:       "完全不同不命中",
			text:       "量子纠缠在通信协议中的作用",
			candidates: candidates,
			wantFound:  false,
		},
		{
			name:       "空文本不命中",
			text:       "！！！",
			candidates: candidates,
			wantFound:  false,
		},
		{
			name: "长度预过滤跳过完全相同但被拉长的候选",
			text: "短题目",
			candidates: []StoredQuestionText{
				{ID: "q-stretched", Title: "被拉长的同文本", Text: "短题目短题目短题目短题目"},
			},
			wantFound: false,
		},
		{
			name: "阈值边界 0.80 恰好命中",
			text: "abcde",
			candidates: []StoredQuestionText{
				{ID: "q-boundary", Title: "边界候选", Text: "abcded"},
			},
			wantFound:  true,
			wantBestID: "q-boundary",
		},
		{
			name: "略低于阈值不命中",
			text: "abcd",
			candidates: []StoredQuestionText{
				{ID: "q-below", Title: "低于阈值候选", Text: "abcde"},
			},
			wantFound: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bestID, bestTitle, score, found := FindMostSimilar(tc.text, tc.candidates)
			if found != tc.wantFound {
				t.Fatalf("found = %v (score=%v best=%q), want %v", found, score, bestID, tc.wantFound)
			}
			if tc.wantFound {
				if tc.wantBestID != "" && bestID != tc.wantBestID {
					t.Fatalf("bestID = %q, want %q", bestID, tc.wantBestID)
				}
				if tc.wantBestAny != "" && !strings.Contains(bestTitle, tc.wantBestAny) {
					t.Fatalf("bestTitle = %q, want containing %q", bestTitle, tc.wantBestAny)
				}
				if score < SimilarityRejectThreshold {
					t.Fatalf("found=true 但 score=%v 低于阈值", score)
				}
			}
		})
	}
}

func TestFindMostSimilarPicksHighestScore(t *testing.T) {
	candidates := []StoredQuestionText{
		{ID: "q-weak", Title: "弱相似", Text: "请计算 100 乘以 200 的结果"},
		{ID: "q-strong", Title: "强相似", Text: "请计算 137 乘以 24 的结果说明"},
	}
	bestID, _, score, found := FindMostSimilar("请计算 137 乘以 24 的结果", candidates)
	if !found {
		t.Fatalf("expected found, score=%v", score)
	}
	if bestID != "q-strong" {
		t.Fatalf("bestID = %q, want q-strong (score=%v)", bestID, score)
	}
}
