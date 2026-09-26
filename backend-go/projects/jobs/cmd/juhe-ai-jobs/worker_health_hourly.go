package main

// J1 outcome → account_health_hourly 小时条带直写装配（BUG-0194 第二层）。
// go-only 形态下 J1 探针直连上游，不产生 account_health_check 使用记录，
// statsagg 聚合源不存在，AI 健康监控读面恒空；由投影面直写回归 J1 真相。
// 列结构与 upsert 口径对齐 statsagg/upserts.go 的 account_health_hourly
// 写入方（newest-wins：last_observed_at 毫秒 UTC 文本，文本比较即时间序）。

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"
)

// healthHourlyWriter 是统计库小时条带写入的双模句柄。
type healthHourlyWriter struct {
	db       *sql.DB
	pool     *pgpool.Handle
	postgres bool
	// timezone 解析统计时区（usageStatsTimezone，投影 stat_hour 键与读面
	// aihealth.go 的口径一致）。
	timezone func(ctx context.Context) (*time.Location, error)
	now      func() time.Time
}

// openHealthHourlyWriter 复用 stats 家族的时区源与连接池约定开统计库句柄。
// stats 家族未装配（statsStore 为 nil）时返回 (nil, nil)：条带写入合法缺席，
// 由装配期显式 warn 一次。
func openHealthHourlyWriter(a *workerAssembly, label string) (*healthHourlyWriter, error) {
	if a.statsStore == nil {
		a.logger.Warn("J1 探针小时条带无写入目标（stats 家族未装配），AI 健康监控将无数据",
			"event", "account_health_hourly_writer_missing")
		return nil, nil
	}
	writer := &healthHourlyWriter{
		timezone: statsTimezoneSource{store: a.statsStore}.StatsTimezone,
		now:      time.Now,
	}
	if a.config.Driver == "postgres" {
		handle, err := a.acquirePool(a.config.PostgresURL, label)
		if err != nil {
			return nil, err
		}
		writer.db = handle.DB()
		writer.pool = handle
		writer.postgres = true
		return writer, nil
	}
	db, err := a.openSQLite(a.config.StatsSQLitePath, label)
	if err != nil {
		return nil, err
	}
	writer.db = db
	return writer, nil
}

func (w *healthHourlyWriter) close() error {
	if w == nil || w.db == nil {
		return nil
	}
	if w.pool != nil {
		return w.pool.Close()
	}
	return w.db.Close()
}

func (w *healthHourlyWriter) bind(query string) string {
	if !w.postgres {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + strconv.Itoa(index))
			index++
			continue
		}
		out.WriteByte(query[i])
	}
	return out.String()
}

// RecordAccountHealthHourly 实现 accounthealth.AccountHealthHourlySink：
// newest-wins upsert。last_observed_at 统一为毫秒 UTC 文本（与 statsagg 的
// 使用记录 created_at 同格式），文本比较即时间序。
func (w *healthHourlyWriter) RecordAccountHealthHourly(ctx context.Context, observation accounthealth.AccountHealthHourlyObservation) error {
	if w == nil || w.db == nil {
		return nil
	}
	location, err := w.timezone(ctx)
	if err != nil {
		return err
	}
	status := "failure"
	if observation.Success {
		status = "success"
	}
	provider := observation.ProviderCode
	if provider == "" {
		provider = "unknown"
	}
	statHour := observation.ObservedAt.In(location).Format("2006-01-02T15")
	lastObservedAt := observation.ObservedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	updatedAt := w.now().UTC().Format(time.RFC3339Nano)
	table := statsTable(w.postgres, "account_health_hourly")
	query := w.bind(`INSERT INTO ` + table + ` (
		  account_id, system_account_id, provider_code, stat_hour, status,
		  last_observed_at, last_record_id, status_code, error_code, error_message, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, stat_hour) DO UPDATE SET
		  system_account_id = excluded.system_account_id,
		  provider_code = excluded.provider_code,
		  status = excluded.status,
		  last_observed_at = excluded.last_observed_at,
		  last_record_id = excluded.last_record_id,
		  status_code = excluded.status_code,
		  error_code = excluded.error_code,
		  error_message = excluded.error_message,
		  updated_at = excluded.updated_at
		WHERE excluded.last_observed_at > ` + table + `.last_observed_at
		   OR (excluded.last_observed_at = ` + table + `.last_observed_at
		       AND excluded.last_record_id > ` + table + `.last_record_id)`)
	var statusCode any
	if observation.StatusCode != 0 {
		statusCode = observation.StatusCode
	}
	var errorCode, errorMessage any
	if observation.ErrorCode != "" {
		errorCode = observation.ErrorCode
	}
	if observation.ErrorMessage != "" {
		errorMessage = observation.ErrorMessage
	}
	if _, err := w.db.ExecContext(ctx, query,
		observation.AccountID, observation.SystemAccountID, provider, statHour, status,
		lastObservedAt, observation.OutcomeID, statusCode, errorCode, errorMessage, updatedAt); err != nil {
		return err
	}
	return nil
}
