package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/cleanuprepo"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/recordmaintenance"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// record_maintenance_jobs 交接表 drain 接线。
//
// gateway cleanup POST（internal/tablemonitor/enqueue.go）把 Node 形状的
// non_business_data_cleanup 任务写入该表：SQLite 落业务库文件
// （JUHE_AI_DATABASE_PATH，与 api_key_record_cleanup_targets 同放置约定）、
// PostgreSQL 落 juhe_dataset schema。jobs 侧按同源读取：
//   - SQLite：复用 retention 家族已打开的业务库句柄（business.DB，与
//     worker_business_db.go 的 getBusinessDatabase 等价句柄同文件共享，
//     WAL + busy_timeout 串行化跨进程读写）；
//   - PostgreSQL：复用 retention 家族的 dataset pool 句柄
//     （dataset.DB，schema 限定 juhe_dataset 后与 gateway 写入同表）。
//
// drain 循环对照 Node 本地队列 flush 语义（100ms 节拍、批次 10、失败固定
// 1s 退避、失败行保留重试），执行面复用 retention.RecordMaintenanceRunner
// （经 family.runMaintenanceOnce 与本地队列 flushLoop 单飞互斥）；停机排空
// 对照 flushRecordMaintenanceQueueForShutdown（有界批次、失败即停、剩余行
// 持久化待下次启动）。

// familyTableDrainRunner 把 retention 家族的执行面适配为 drain Runner。
type familyTableDrainRunner struct {
	family *retentionFamily
}

func (r familyTableDrainRunner) RunOnce(ctx context.Context, job retention.RecordMaintenanceJob) (map[string]any, error) {
	return r.family.runMaintenanceOnce(ctx, job)
}

func (a *workerAssembly) wireRecordMaintenanceTableDrain(family *retentionFamily, business *cleanuprepo.DB, dataset *cleanuprepo.DB) error {
	var db *sql.DB
	if family.postgres {
		db = dataset.DB
	} else {
		db = business.DB
	}
	store, err := recordmaintenance.OpenStore(db, family.postgres)
	if err != nil {
		return fmt.Errorf("open record_maintenance_jobs store: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = store.EnsureSchema(ctx)
	cancel()
	if err != nil {
		return fmt.Errorf("initialize record_maintenance_jobs schema: %w", err)
	}
	drainer := &recordmaintenance.Drainer{
		Store:     store,
		Runner:    familyTableDrainRunner{family: family},
		Logger:    a.logger,
		BatchSize: a.config.RecordMaintenanceBatchSize,
	}
	stopDrain := make(chan struct{})
	// 关闭顺序（closers 逆序执行）：本 closer 先停 drain 循环并做有界停机
	// 排空，随后既有 closer 才停本地队列 flush 与家族存储。
	a.addCloser(func() error {
		close(stopDrain)
		drainer.DrainShutdown(a.config.RecordMaintenanceShutdownFlushMaxBatches)
		return nil
	})
	go drainer.Run(stopDrain)
	return nil
}
