package statreads

import (
	"strings"

	capabilitydomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
)

// AI 账户能力域拆分 M3 配套适配（计划 §M3 配套适配）：统计/用量/AI 性能/
// AI 健康读面的可选 `domain` 筛选参数。筛选在服务端执行（前端绕过无效），
// 只读查询、不触碰写路径；不携带（或 "all"）时全部查询与现状逐项一致
// （默认行为零变化）。

// capabilityDomainFilterParam 是统计读面可选域筛选的查询参数名。
const capabilityDomainFilterParam = "domain"

// optionalCapabilityDomainFilter 解析可选域筛选值：空或 "all" 归一为 ""
// （不过滤）；其余值必须是 accounts.capability_domain 的四枚举之一。第二返
// 回 false 表示值非法，调用方按各端点既有参数错误文案回答 400。
func optionalCapabilityDomainFilter(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" || value == "all" {
		return "", true
	}
	if capabilitydomain.Domain(value).Valid() {
		return value, true
	}
	return "", false
}

// intersectStringsPreserveOrder 返回 left ∩ right（保留 left 顺序、去重），
// 供关键词集合与 M3 域集合取交集（AND 语义）。空交集返回空切片。
func intersectStringsPreserveOrder(left, right []string) []string {
	set := make(map[string]bool, len(right))
	for _, value := range right {
		set[value] = true
	}
	out := []string{}
	seen := map[string]bool{}
	for _, value := range left {
		if !set[value] || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// capabilityDomainClause 渲染业务库 accounts 表的域过滤子句（alias 可空 =
// 主表 accounts）。占位符参数由调用方按 SQL 文本顺序追加返回值。
func capabilityDomainClause(alias string) string {
	if alias != "" {
		alias += "."
	} else {
		alias = "accounts."
	}
	return alias + "capability_domain = ?"
}
