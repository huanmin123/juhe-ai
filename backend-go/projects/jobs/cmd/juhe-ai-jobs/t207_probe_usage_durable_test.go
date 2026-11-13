package main

// T207-C2：jobs 探针用量改走 EnqueueDurable——队列满且溢出落盘失败时返回
// 错误（durable「未接收」语义，上游 recordProbeUsage 按失败留痕），不再经
// best-effort Enqueue 的溢出丢弃臂静默丢失。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-platform/usagewriter"
)

// t207FailingStore 是 ShardStore 测试替身：WriteBatch 恒失败（本测试只考察
// 入队臂，writer 不 Start、无 flush 消费队列）。
type t207FailingStore struct{}

func (t207FailingStore) WriteBatch(ctx usagewriter.Ctx, plan usagewriter.WritePlan) (int, error) {
	return 0, errors.New("db down")
}

// t207FailingSpool 是 OverflowSpool 测试替身：Persist 恒失败（模拟磁盘满）。
type t207FailingSpool struct{}

func (t207FailingSpool) Persist(ctx usagewriter.Ctx, input usagewriter.UsageRecordInput) error {
	return errors.New("spool disk full")
}

// t207FailingDriver 让 resolveScope 的业务库查询立即失败（查询错误按既有契
// 约静默跳过作用域解析）。
type t207FailingDriver struct{}

func (t207FailingDriver) Open(name string) (driver.Conn, error) {
	return nil, errors.New("db unavailable")
}

var t207RegisterDriverOnce sync.Once

func t207RegisterFailingDriver() {
	t207RegisterDriverOnce.Do(func() {
		sql.Register("t207-failing-driver", t207FailingDriver{})
	})
}

func TestT207RecordProbeUsageDurableEnqueueRejectsOnFullQueueAndSpoolFailure(t *testing.T) {
	t207RegisterFailingDriver()
	db, err := sql.Open("t207-failing-driver", "probe-usage")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	writer := usagewriter.NewWriter(usagewriter.Config{
		QueueMaxItems:   1,
		QueueMaxBytes:   1 << 20,
		BatchSize:       100,
		FlushIntervalMs: 60_000,
	}, t207FailingStore{}, usagewriter.SystemClock{},
		usagewriter.WithOverflowSpool(t207FailingSpool{}))
	probe := &probeUsageWriter{
		business: &businessDB{db: db},
		writer:   writer,
		clock:    usagewriter.SystemClock{},
	}
	// 占满队列（不 Start，队列状态确定）。
	if err := writer.Enqueue(context.Background(), usagewriter.UsageRecordInput{
		TrafficSource:   usagewriter.TrafficSourceGateway,
		SystemAccountID: "sys1",
		Success:         true,
	}); err != nil {
		t.Fatal(err)
	}
	// 第二条：队列满 + 溢出落盘失败 → EnqueueDurable 返回错误（记录未被接
	// 收）；best-effort Enqueue 在同场景会静默返回 nil 并丢弃。
	err = probe.RecordProbeUsage(context.Background(), accounthealth.ProbeUsageObservation{
		AccountID:     "acc1",
		TrafficSource: accounthealth.ProbeTrafficSourceAccountHealthCheck,
		Endpoint:      "chat_completions",
		Model:         "gpt-5",
		Success:       true,
		ObservedAt:    time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("durable enqueue must surface the rejection, got nil")
	}
}
