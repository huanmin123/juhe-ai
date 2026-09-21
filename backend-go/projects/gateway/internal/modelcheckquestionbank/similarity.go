package modelcheckquestionbank

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// SimilarityRejectThreshold 是题目内容查重的拒绝阈值：与现有
// （pending/approved）题目的最大字符二元组 Jaccard 相似度达到该值即拒绝
// 提交（计划 §2）。阈值与归一化规则收敛在本包内，由 similarity_test.go
// 表驱动固化。
const SimilarityRejectThreshold = 0.80

// LengthPreFilterRatio 是查重的长度预过滤比例：归一化后 rune 长度差占
// 较长者比例超过该值的候选直接跳过 Jaccard 比较，控制题库线性扫描的比较量。
const LengthPreFilterRatio = 0.5

// NormalizeText 归一化文本用于查重：unicode.NFKC → 去除所有空白与
// Unicode 标点/符号（unicode.IsPunct/IsSymbol）→ 小写。全角/半角、标点
// 与空白差异在归一化后不再影响标题查重与内容相似度。
func NormalizeText(s string) string {
	normalized := norm.NFKC.String(s)
	var b strings.Builder
	b.Grow(len(normalized))
	for _, r := range normalized {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// BigramSet 返回归一化文本的字符二元组集合（rune 粒度，适配中文）。
// 单字符文本以单字符自身作为唯一元素（否则单字之间永远不可比）；空文本
// 返回空集合，Jaccard 恒为 0，不会构成相似证据。
func BigramSet(s string) map[string]struct{} {
	return bigramSetOfRunes([]rune(NormalizeText(s)))
}

// bigramSetOfRunes 是已归一化 rune 序列的二元组集合实现，供
// BigramSet 与 FindMostSimilar 复用，避免归一化两次。
func bigramSetOfRunes(runes []rune) map[string]struct{} {
	set := make(map[string]struct{}, len(runes))
	if len(runes) == 1 {
		set[string(runes[0])] = struct{}{}
		return set
	}
	for i := 0; i+1 < len(runes); i++ {
		set[string(runes[i:i+2])] = struct{}{}
	}
	return set
}

// Jaccard 返回两个集合的 Jaccard 相似度 |A∩B| / |A∪B|。任一为空集时
// 返回 0：空文本之间不构成相似证据。
func Jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	small, large := a, b
	if len(small) > len(large) {
		small, large = large, small
	}
	intersection := 0
	for value := range small {
		if _, ok := large[value]; ok {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	return float64(intersection) / float64(union)
}

// StoredQuestionText 是参与内容查重比对的既有题目文本快照（题库为百级
// 规模，FindMostSimilar 一次全量线性扫描即可）。
type StoredQuestionText struct {
	ID    string
	Title string
	Text  string
}

// FindMostSimilar 在候选题目中寻找与 text 最相似的题目：先做长度预过滤
// （归一化长度差/较长者 > LengthPreFilterRatio 跳过），再逐一计算归一化
// 字符二元组 Jaccard。found 表示最高分达到 SimilarityRejectThreshold；
// score 返回未触发预过滤候选中的最高分（无候选时为 0）。
func FindMostSimilar(text string, candidates []StoredQuestionText) (bestID, bestTitle string, score float64, found bool) {
	target := []rune(NormalizeText(text))
	targetBigrams := bigramSetOfRunes(target)
	bestScore := 0.0
	for _, candidate := range candidates {
		candidateRunes := []rune(NormalizeText(candidate.Text))
		if !lengthPreFilterPasses(target, candidateRunes) {
			continue
		}
		candidateScore := Jaccard(targetBigrams, bigramSetOfRunes(candidateRunes))
		if candidateScore > bestScore {
			bestScore = candidateScore
			bestID, bestTitle = candidate.ID, candidate.Title
		}
	}
	return bestID, bestTitle, bestScore, bestScore >= SimilarityRejectThreshold
}

// lengthPreFilterPasses 判断两个已归一化 rune 序列是否进入 Jaccard 比较：
// 长度差/较长者 > LengthPreFilterRatio 时跳过。两侧都为空时无可比证据，
// 直接跳过。
func lengthPreFilterPasses(a, b []rune) bool {
	longer := len(a)
	if len(b) > longer {
		longer = len(b)
	}
	if longer == 0 {
		return false
	}
	difference := len(a) - len(b)
	if difference < 0 {
		difference = -difference
	}
	return float64(difference)/float64(longer) <= LengthPreFilterRatio
}
