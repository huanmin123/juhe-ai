// 波次 w14j：statsagg PG 门禁的自愈 seed。共享覆盖库（w1cover）可能被
// 外部流程重置，这里从 maintenance 权威 schema 源（pg_schema.go）提取
// account_list_availability_* 函数族并 CREATE OR REPLACE 应用，保证依赖
// 数据库触发器函数的 PG 门控测试可自愈。仅加法幂等 DDL。
package statsagg

import (
	"database/sql"
	"os"
	"regexp"
	"strings"
	"testing"
)

// w14jEnsureAccountListAvailabilityFunctions 幂等补齐函数族（忽略已存在）。
func w14jEnsureAccountListAvailabilityFunctions(t *testing.T, db *sql.DB) {
	t.Helper()
	const source = "../../../../maintenance/internal/schema/pg_schema.go"
	data, err := os.ReadFile(source)
	if err != nil {
		t.Skipf("w14j: 无法读取权威 schema 源（%s）: %v", source, err)
	}
	text := strings.ReplaceAll(string(data), "`+\"`\"+`", "")
	re := regexp.MustCompile(`(?s)CREATE OR REPLACE FUNCTION account_list_availability_\w+\(.*?\$function\$;`)
	matches := re.FindAllString(text, -1)
	if len(matches) == 0 {
		t.Skip("w14j: 权威 schema 源中未找到函数族")
	}
	for _, match := range matches {
		statement := strings.TrimSpace(strings.ReplaceAll(match, "\r\n", "\n"))
		if _, err := db.Exec(`SET search_path = juhe_business; ` + statement); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				continue
			}
			t.Logf("w14j 函数族 seed 跳过一条: %v", err)
		}
	}
}
