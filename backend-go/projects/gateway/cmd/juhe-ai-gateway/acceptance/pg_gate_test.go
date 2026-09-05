package acceptance

import (
	"os"
	"strings"
)

// pgDSN 读取 PG 验收门控环境变量。
func pgDSN() string {
	return strings.TrimSpace(os.Getenv("JUHE_AI_ACCEPTANCE_PG_DSN"))
}
