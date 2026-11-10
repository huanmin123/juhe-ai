package main

// 用量记录投递替身（2026-11-09 T189）。生产用量记录由网关直投 PG
// （usageWriterDeliverer → usagewriter.Enqueue）；原文件补偿 spool 实现已删除。
// 本文件只保留链级测试使用的文件捕获投递器：把归一化后的
// 记录按一文件一条写到测试目录，供既有用例断言记账字段；它不是生产 spool，
// 不参与回放、容量或跨进程交接。

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// usageSpoolCapacity 保留旧调用点的参数形状；文件捕获投递器不设容量上限。
type usageSpoolCapacity struct{}

// fileUsageDeliverer 是 usageRecordDeliverer 的测试替身：每条记录写一个
// JSON 文件到 <directory>/<instance>/<token>.json。
type fileUsageDeliverer struct {
	directory string
	sequence  atomic.Uint64
}

func newUsageSpool(directory string, clock gatewaypreauth.Clock, logger *slog.Logger, capacity usageSpoolCapacity) *fileUsageDeliverer {
	if directory == "" {
		return nil
	}
	_ = clock
	_ = logger
	_ = capacity
	return &fileUsageDeliverer{directory: directory}
}

func (d *fileUsageDeliverer) Persist(ctx gatewayusage.Ctx, input gatewayusage.UsageRecordInput) error {
	encoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	instance := filepath.Join(d.directory, "gateway-chain")
	if err := os.MkdirAll(instance, 0o700); err != nil {
		return err
	}
	name := fmt.Sprintf("%020d-%d.json", d.sequence.Add(1), os.Getpid())
	return os.WriteFile(filepath.Join(instance, name), append(encoded, '\n'), 0o600)
}

// StopReplay 保留旧清理调用点：文件捕获投递器没有回放循环。
func (d *fileUsageDeliverer) StopReplay() {}

// failingUsageDeliverer 模拟持久投递面不可用，供失败计数与告警测试。
type failingUsageDeliverer struct{}

func (failingUsageDeliverer) Persist(gatewayusage.Ctx, gatewayusage.UsageRecordInput) error {
	return os.ErrDeadlineExceeded
}
