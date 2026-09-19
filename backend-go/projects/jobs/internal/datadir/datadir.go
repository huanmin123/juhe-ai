// Package datadir 实现 jobs 侧的数据目录派生约定（2026-09-19 零配置决策）：
// 新 env JUHE_AI_DATA_DIR 指定数据根目录，缺省 ./data（相对进程 cwd，
// TrimSpace 后为空也视为未配置）；所有「路径类」env 未配置时派生为
// <DATA_DIR>/<固定名>，显式配置始终优先。gateway 进程对同名 env 使用相同
// 固定名，两进程靠相同 DATA_DIR 约定共享同一业务库/运行日志库。
//
// 派生 helper 在 jobs 模块内本地实现，不放 backend-go/shared：该约定只服务
// 空环境可启动的组合根默认值，不是平台级通用能力。
package datadir

import (
	"path/filepath"
	"strings"
)

// DefaultDir 是 JUHE_AI_DATA_DIR 未配置时的数据根目录（相对进程 cwd）。
const DefaultDir = "./data"

// Root 返回数据根目录：JUHE_AI_DATA_DIR（TrimSpace）非空时优先，否则 ./data。
func Root(getenv func(string) string) string {
	if getenv != nil {
		if dir := strings.TrimSpace(getenv("JUHE_AI_DATA_DIR")); dir != "" {
			return dir
		}
	}
	return DefaultDir
}

// Path 解析一个路径类 env：显式配置（TrimSpace 后非空）始终优先；未配置时
// 派生为 <Root>/<fixedName>。fixedName 使用 "/" 分隔的相对固定名
// （如 "codex-context/state-shards"），内部经 filepath.Join 转为平台分隔符。
func Path(getenv func(string) string, envName, fixedName string) string {
	if getenv != nil {
		if value := strings.TrimSpace(getenv(envName)); value != "" {
			return value
		}
	}
	return filepath.Join(Root(getenv), filepath.FromSlash(fixedName))
}
