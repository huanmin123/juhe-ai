package main

// 用量交接链（E2E-FINDING #1 修复）单元层验证：spooledUsageRecorder 入口
// 归一化让 spool 落盘记录携带稳定 id/createdAt（jobs usagespooldrain
// parseSpoolRecord 的前置契约），ID 工厂产出 shard 格式 id。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// usageRecordShardIDPattern 对照 generateUsageRecordId 契约：
// usage_<YYYYMMDD>_sNN_<unixmilli>_<entropy 字母数字，≤24 位>。
var usageRecordShardIDPattern = regexp.MustCompile(`^usage_(\d{8})_s(\d{2})_(\d+)_([a-zA-Z0-9]{1,24})$`)

func TestUsageShardRecordIDFactory生成Shard格式稳定Id(t *testing.T) {
	factory := usageShardRecordIDFactory{now: func() time.Time {
		return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	}}
	用例 := []struct {
		名称        string
		createdAt string
	}{
		{名称: "UTC 毫秒 instant", createdAt: "2026-09-17T12:00:00.000Z"},
		{名称: "带数值 offset 的 instant", createdAt: "2026-09-17T20:00:00.000+08:00"},
	}
	for _, 用例 := range 用例 {
		t.Run(用例.名称, func(t *testing.T) {
			id := factory.GenerateUsageRecordID(用例.createdAt)
			match := usageRecordShardIDPattern.FindStringSubmatch(id)
			if match == nil {
				t.Fatalf("id = %q，不符合 shard 格式契约 usage_YYYYMMDD_sNN_ms_entropy", id)
			}
			// bucket 取 createdAt 的 UTC 日期：+08:00 的 20 点落到 09-17。
			if match[1] != "20260917" {
				t.Fatalf("bucketDateKey = %q，期望 20260917", match[1])
			}
			shardID := 0
			for _, r := range match[2] {
				shardID = shardID*10 + int(r-'0')
			}
			if shardID < 0 || shardID >= usageDefaultUsageShardCount {
				t.Fatalf("shard id = %d，超出 [0,%d)", shardID, usageDefaultUsageShardCount)
			}
			if match[3] != "1789646400000" {
				t.Fatalf("unixmilli = %q，期望注入时钟的毫秒值 1789646400000", match[3])
			}
		})
	}

	// 唯一性：随机 entropy 让两次生成的 id 不重复（重复投递幂等依赖 id 唯一）。
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		id := usageShardRecordIDFactory{}.GenerateUsageRecordID("2026-09-17T12:00:00.000Z")
		if seen[id] {
			t.Fatalf("第 %d 次生成重复 id = %q", i, id)
		}
		seen[id] = true
	}

	// 非法 instant：回落当前时间 bucket，保持 id 可路由（不 panic、不返回空）。
	if id := factory.GenerateUsageRecordID("not-a-time"); !strings.HasPrefix(id, "usage_20260917_s") {
		t.Fatalf("非法 instant 的 id = %q，期望回落注入时钟 bucket 20260917", id)
	}
}

// spooledUsageRecorder 入口归一化：无 id/createdAt 的记录经 EnqueueUsageRecord
// 落盘后必须带稳定 shard id 与 RFC3339 createdAt——jobs drain 对缺失值直接
// 判损坏隔离（E2E-FINDING #1 断点）。
func TestSpooledUsageRecorder补齐Spool记录稳定Id(t *testing.T) {
	dir := t.TempDir()
	spool := newUsageSpool(dir, gatewaypreauth.SystemClock{}, newTestSlogLogger(), usageSpoolCapacity{})
	recorder := newSpooledUsageRecorder(usageBridgeConfig{BufferCapacity: 8, Logger: newTestSlogLogger()}, spool)

	输入 := []gatewayusage.UsageRecordInput{
		{TraceID: "trace-1", TrafficSource: "gateway", Success: true},
		{TraceID: "trace-2", TrafficSource: "gateway", Success: false, CreatedAt: "2026-09-17T12:00:00.000Z"},
	}
	for index, record := range 输入 {
		if err := recorder.EnqueueUsageRecord(t.Context(), record); err != nil {
			t.Fatalf("第 %d 条记录入队 = %v，期望 nil", index, err)
		}
	}
	recorder.Close()

	entries, err := os.ReadDir(filepath.Join(dir, "gateway-chain"))
	if err != nil || len(entries) != len(输入) {
		t.Fatalf("spool 文件数 = %d（err=%v），期望 %d", len(entries), err, len(输入))
	}
	seenIDs := map[string]bool{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			t.Fatalf("spool 目录出现非 .json 文件：%s", entry.Name())
		}
		content, err := os.ReadFile(filepath.Join(dir, "gateway-chain", entry.Name()))
		if err != nil {
			t.Fatalf("读取 spool 文件 = %v", err)
		}
		var record gatewayusage.UsageRecordInput
		if err := json.Unmarshal(content, &record); err != nil {
			t.Fatalf("解析 spool 记录 = %v", err)
		}
		if !usageRecordShardIDPattern.MatchString(record.ID) {
			t.Fatalf("spool 记录 id = %q，缺稳定 shard id（jobs drain 会判损坏）", record.ID)
		}
		if record.CreatedAt == "" {
			t.Fatalf("spool 记录缺 createdAt：%s", entry.Name())
		}
		if _, err := time.Parse(time.RFC3339, record.CreatedAt); err != nil {
			t.Fatalf("spool 记录 createdAt = %q 不是 RFC3339：%v", record.CreatedAt, err)
		}
		if seenIDs[record.ID] {
			t.Fatalf("spool 记录 id 重复：%q", record.ID)
		}
		seenIDs[record.ID] = true
	}
}

// 归一化失败（非法 createdAt）不阻塞调用方：返回 nil、不落盘，按采样告警面
// 处理（Node 本地路径从不向调用方暴露 enqueue 失败）。
func TestSpooledUsageRecorder归一化失败静默丢弃不落盘(t *testing.T) {
	dir := t.TempDir()
	spool := newUsageSpool(dir, gatewaypreauth.SystemClock{}, newTestSlogLogger(), usageSpoolCapacity{})
	recorder := newSpooledUsageRecorder(usageBridgeConfig{BufferCapacity: 8, Logger: newTestSlogLogger()}, spool)

	if err := recorder.EnqueueUsageRecord(t.Context(), gatewayusage.UsageRecordInput{
		TraceID: "trace-bad", TrafficSource: "gateway", CreatedAt: "not-a-time",
	}); err != nil {
		t.Fatalf("归一化失败应返回 nil（调用方无感），实际 = %v", err)
	}
	recorder.Close()

	entries, err := os.ReadDir(filepath.Join(dir, "gateway-chain"))
	if err == nil && len(entries) > 0 {
		t.Fatalf("归一化失败的记录不应落盘，实际 %d 个文件", len(entries))
	}
}
