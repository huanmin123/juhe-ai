package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
)

// runJobsOnceEntry 是 --run-jobs-once stdout JSON 数组的元素：每个请求的
// 任务名一条结果。outcome 取 jobsched.TaskResult.Outcome（skipped 表示该名
// 在注册表为 GoWired 但当前部署形态未接线/未注册，本轮未执行）；failed 表示
// 任务执行返回错误。
type runJobsOnceEntry struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
	Warning string `json:"warning"`
}

// splitRunJobsOnceNames 把逗号分隔的任务名参数拆成有序清单；空片段（含仅传
// 逗号）按用法错误处理，由调用方收敛为 exit 2。
func splitRunJobsOnceNames(value string) ([]string, error) {
	names := make([]string, 0, 8)
	for _, raw := range strings.Split(value, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, fmt.Errorf("--run-jobs-once 任务名不能为空片段: %q", value)
		}
		names = append(names, name)
	}
	return names, nil
}

// runJobsOnceExit 是 --run-jobs-once 的分发内核（main.go 在 worker 装配完成
// 之后、health server 创建/监听之前调用；测试可直接构造 assembly 直调）。
//
// 退出码契约（对齐既有「flag 用法错误 2 / runtime 失败 1」两档）：
//   - 0：全部请求任务处理完成（注册表 GoWired 但当前部署形态 disabled 的名
//     按 jobsched.OutcomeSkipped 计入成功路径，warning 携带登记原因）；
//   - 1：任一任务执行返回错误——把已完成（含失败项）的结果 JSON 数组写到
//     stdout 后退出，让调用方拿到部分进度；
//   - 2：用法错误——任务名清单解析失败，或存在既未接线也不在 disabled
//     清单的未知任务名（stderr 列出全部 wiredJobs 供选择）。
//
// 名单先整体校验再执行：避免执行到一半才在后面的名字上撞未知任务名。
// SQLite 模式下 withLease 对非 postgres driver 直跑（不取 PG 租约）是既有
// 语义，本入口无需额外处理。存储关闭不在本函数：main.go 在装配成功后已
// defer worker.closeStores()。
func runJobsOnceExit(worker *workerAssembly, value string, stdout io.Writer, stderr io.Writer) int {
	names, err := splitRunJobsOnceNames(value)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	disabled := make(map[string]string, len(worker.disabledJobs))
	for _, entry := range worker.disabledJobs {
		disabled[entry.JobName] = entry.Reason
	}
	for _, name := range names {
		if _, wired := worker.wiredTasks[name]; wired {
			continue
		}
		if _, knownDisabled := disabled[name]; knownDisabled {
			continue
		}
		fmt.Fprintf(stderr, "unknown --run-jobs-once task %q; wired jobs: %v\n", name, worker.wiredJobs)
		return 2
	}
	results := make([]runJobsOnceEntry, 0, len(names))
	for _, name := range names {
		if _, wired := worker.wiredTasks[name]; !wired {
			// 预校验已保证非 wired 名必在 disabled 清单：登记原因透出到
			// warning，保持「不静默跳过」的注册表语义。
			results = append(results, runJobsOnceEntry{Name: name, Outcome: string(jobsched.OutcomeSkipped), Warning: disabled[name]})
			continue
		}
		result, runErr := worker.runWiredJobOnce(context.Background(), name)
		if runErr != nil {
			results = append(results, runJobsOnceEntry{Name: name, Outcome: "failed", Warning: runErr.Error()})
			_ = json.NewEncoder(stdout).Encode(results)
			return 1
		}
		outcome := string(result.Outcome)
		if outcome == "" {
			outcome = string(jobsched.OutcomeSuccess)
		}
		results = append(results, runJobsOnceEntry{Name: name, Outcome: outcome, Warning: result.Warning})
	}
	if err := json.NewEncoder(stdout).Encode(results); err != nil {
		fmt.Fprintf(stderr, "encode run-jobs-once results: %v\n", err)
		return 1
	}
	return 0
}
