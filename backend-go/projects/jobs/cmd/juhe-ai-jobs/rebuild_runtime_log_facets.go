package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/runtimelog"
)

// rebuildRuntimeLogFacetsResult 是 --rebuild-runtime-log-facets 的 stdout
// JSON 结果契约：重建后 bucket 的聚合事实（与 runtime_log_facet_summary 行
// 逐字段一致），供运维核对。bucket 固定为 "current"（facetBucketKey）。
type rebuildRuntimeLogFacetsResult struct {
	Bucket       string `json:"bucket"`
	TotalCount   int64  `json:"totalCount"`
	EarliestTime string `json:"earliestTime"`
	LatestTime   string `json:"latestTime"`
}

// runRebuildRuntimeLogFacets 是 --rebuild-runtime-log-facets 的一次性分发
// 内核（main.go 在 owner/passive 判定之前调用：一次性运维操作与守护形态
// 解耦，passive env 下也不会被静默转成被动健康面）。
//
// 退出码契约（对齐既有「flag 用法错误 2 / runtime 失败 1」两档）：
//   - 0：重建完成，stdout 输出单个 JSON 结果（该分支无其他 stdout 输出）；
//   - 1：配置装载、store 打开、schema 校验或重建失败（stderr 单行错误）。
//     owner lease 被运行中的 juhe-ai-jobs 持有时按 1 显式失败（重建必须独占
//     F1 写侧），不等待、不重试。
func runRebuildRuntimeLogFacets(getenv func(string) string, stdout io.Writer, stderr io.Writer) int {
	config, err := runtimelog.LoadConfig(getenv)
	if err != nil {
		return failWith(stderr, fmt.Errorf("load F1 runtime-log-indexer config: %w", err))
	}
	store, err := runtimelog.OpenStore(context.Background(), config)
	if err != nil {
		return failWith(stderr, fmt.Errorf("open F1 runtime-log-indexer store: %w", err))
	}
	defer store.Close()
	if err := runtimelog.EnsureSchema(context.Background(), store); err != nil {
		return failWith(stderr, fmt.Errorf("initialize F1 runtime-log-indexer schema: %w", err))
	}
	if err := store.CheckSchema(context.Background()); err != nil {
		return failWith(stderr, fmt.Errorf("verify F1 runtime-log-indexer schema: %w", err))
	}
	rebuilt, err := runtimelog.RebuildPostgresFacets(context.Background(), store, config)
	if err != nil {
		return failWith(stderr, fmt.Errorf("rebuild F1 runtime-log facets: %w", err))
	}
	if err := json.NewEncoder(stdout).Encode(rebuildRuntimeLogFacetsResult{
		Bucket:       rebuilt.BucketKey,
		TotalCount:   rebuilt.TotalCount,
		EarliestTime: rebuilt.EarliestTime,
		LatestTime:   rebuilt.LatestTime,
	}); err != nil {
		return failWith(stderr, fmt.Errorf("encode rebuild-runtime-log-facets result: %w", err))
	}
	return 0
}
