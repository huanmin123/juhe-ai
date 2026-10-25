package modelcheckowner

import (
	"regexp"
	"strings"
)

// failpointMatchQuery 把生产方言 SQL 归一成失败注入模式惯用的裸表名 + `?`
// 形态：去掉 schema 限定前缀、把 `$n` 占位符还原为 `?`。失败注入关心的是
// "命中了哪条语句"，与占位符编号和 schema 限定无关，因此匹配前统一归一。
var failpointPlaceholderPattern = regexp.MustCompile(`\$\d+`)

func failpointMatchQuery(query string) string {
	for _, prefix := range []string{"juhe_j3b.", "juhe_business.", "juhe_dataset.", "juhe_stats.", "juhe_chat."} {
		query = strings.ReplaceAll(query, prefix, "")
	}
	return failpointPlaceholderPattern.ReplaceAllString(query, "?")
}
