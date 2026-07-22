package postgres

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
)

const managementSystemMetricsHourlySQL = `
SELECT bucket_key AS stat_hour, sample_count, cpu_percent_sum, cpu_percent_max,
  memory_used_percent_sum, memory_used_percent_max,
  process_rss_bytes_max, process_heap_used_bytes_max,
  event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max,
  network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_max, network_rx_bytes_per_sec_count,
  network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_max, network_tx_bytes_per_sec_count,
  network_rx_total_bytes_max, network_tx_total_bytes_max,
  db_file_bytes_max, stats_lag_seconds_max
FROM juhe_stats.system_metrics_trend_windows
WHERE window_key = $1 AND start_date = $2 AND end_date = $3
ORDER BY bucket_key ASC
LIMIT $4`

const managementSystemMetricsProcessTrendSQL = `
SELECT bucket_key AS stat_hour, process_role, sample_count,
  event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max,
  process_rss_bytes_sum, process_rss_bytes_max,
  process_heap_used_bytes_sum, process_heap_used_bytes_max,
  process_heap_total_bytes_sum, process_heap_total_bytes_max
FROM juhe_stats.process_event_loop_trend_windows
WHERE window_key = $1 AND start_date = $2 AND end_date = $3
  AND process_role = ANY($4::text[])
ORDER BY bucket_key ASC, process_role ASC
LIMIT $5`

const managementSystemMetricsLatestSQL = `
SELECT DISTINCT ON (process_role)
  process_role, process_pid, sampled_at, event_loop_lag_ms,
  process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes,
  process_external_bytes, process_array_buffers_bytes
FROM juhe_stats.process_event_loop_samples
WHERE process_role = ANY($1::text[])
ORDER BY process_role, sampled_at DESC, id DESC
LIMIT $2`

const managementSystemMetricsPeakSQL = `
SELECT DISTINCT ON (process_role)
  process_role, process_pid, sampled_at, event_loop_lag_ms,
  process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes,
  process_external_bytes, process_array_buffers_bytes
FROM juhe_stats.process_event_loop_samples
WHERE process_role = ANY($1::text[])
  AND sampled_at >= $2
  AND event_loop_lag_ms IS NOT NULL
ORDER BY process_role, event_loop_lag_ms DESC, sampled_at DESC, id DESC
LIMIT $3`

const (
	maxManagementSystemMetricsHourlyRows       = 31 * 24
	maxManagementSystemMetricsProcessTrendRows = maxManagementSystemMetricsHourlyRows * 5
)

type managementSystemMetricsSource interface {
	listHourly(context.Context, port.ManagementSystemMetricsReadInput, int) ([]port.ManagementSystemMetricsHourlyAggregate, error)
	listProcessTrend(context.Context, port.ManagementSystemMetricsReadInput, int) ([]port.ManagementProcessMetricTrendAggregate, error)
	listLatest(context.Context, []string, int) ([]port.ManagementProcessMetricSample, error)
	listPeak(context.Context, []string, string, int) ([]port.ManagementProcessMetricSample, error)
}

type managementSystemMetricsPool interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type postgresManagementSystemMetricsSource struct {
	pool managementSystemMetricsPool
}

func (s *Store) ReadManagementSystemMetrics(ctx context.Context, input port.ManagementSystemMetricsReadInput) (port.ManagementSystemMetricsSnapshot, error) {
	return readManagementSystemMetrics(ctx, postgresManagementSystemMetricsSource{pool: s.pool}, input)
}

func readManagementSystemMetrics(ctx context.Context, source managementSystemMetricsSource, input port.ManagementSystemMetricsReadInput) (port.ManagementSystemMetricsSnapshot, error) {
	if source == nil {
		return port.ManagementSystemMetricsSnapshot{}, fmt.Errorf("management system metrics source is required")
	}
	if input.Days <= 0 || input.BucketHours <= 0 || len(input.ProcessRoles) == 0 {
		return port.ManagementSystemMetricsSnapshot{}, fmt.Errorf("management system metrics bounds are invalid")
	}
	hourlyLimit := min(maxManagementSystemMetricsHourlyRows, (input.Days*24+input.BucketHours-1)/input.BucketHours)
	processTrendLimit := min(maxManagementSystemMetricsProcessTrendRows, hourlyLimit*len(input.ProcessRoles))
	roleLimit := len(input.ProcessRoles)

	var snapshot port.ManagementSystemMetricsSnapshot
	errCh := make(chan error, 4)
	var wait sync.WaitGroup
	wait.Add(4)
	go func() {
		defer wait.Done()
		rows, err := source.listHourly(ctx, input, hourlyLimit)
		if err != nil {
			errCh <- fmt.Errorf("list management system metrics hourly trend: %w", err)
			return
		}
		snapshot.HourlyTrend = rows
	}()
	go func() {
		defer wait.Done()
		rows, err := source.listProcessTrend(ctx, input, processTrendLimit)
		if err != nil {
			errCh <- fmt.Errorf("list management process metrics trend: %w", err)
			return
		}
		snapshot.ProcessTrend = rows
	}()
	go func() {
		defer wait.Done()
		rows, err := source.listLatest(ctx, input.ProcessRoles, roleLimit)
		if err != nil {
			errCh <- fmt.Errorf("list latest management process metrics: %w", err)
			return
		}
		snapshot.ProcessLatest = rows
	}()
	go func() {
		defer wait.Done()
		rows, err := source.listPeak(ctx, input.ProcessRoles, input.PeakStartedAt, roleLimit)
		if err != nil {
			errCh <- fmt.Errorf("list peak management process metrics: %w", err)
			return
		}
		snapshot.ProcessPeak = rows
	}()
	wait.Wait()
	close(errCh)
	if err := <-errCh; err != nil {
		return port.ManagementSystemMetricsSnapshot{}, err
	}
	return snapshot, nil
}

func (s postgresManagementSystemMetricsSource) listHourly(ctx context.Context, input port.ManagementSystemMetricsReadInput, limit int) ([]port.ManagementSystemMetricsHourlyAggregate, error) {
	rows, err := s.pool.Query(ctx, managementSystemMetricsHourlySQL, input.WindowKey, input.StartDate, input.EndDate, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]port.ManagementSystemMetricsHourlyAggregate, 0)
	for rows.Next() {
		var row port.ManagementSystemMetricsHourlyAggregate
		if err := rows.Scan(
			&row.StatHour, &row.SampleCount, &row.CPUPercentSum, &row.CPUPercentMax,
			&row.MemoryUsedPercentSum, &row.MemoryUsedPercentMax,
			&row.ProcessRSSBytesMax, &row.ProcessHeapUsedBytesMax,
			&row.EventLoopLagMSSum, &row.EventLoopLagMSSampleCount, &row.EventLoopLagMSMax,
			&row.NetworkRXBytesPerSecondSum, &row.NetworkRXBytesPerSecondMax, &row.NetworkRXBytesPerSecondCount,
			&row.NetworkTXBytesPerSecondSum, &row.NetworkTXBytesPerSecondMax, &row.NetworkTXBytesPerSecondCount,
			&row.NetworkRXTotalBytesMax, &row.NetworkTXTotalBytesMax,
			&row.DBFileBytesMax, &row.StatsLagSecondsMax,
		); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s postgresManagementSystemMetricsSource) listProcessTrend(ctx context.Context, input port.ManagementSystemMetricsReadInput, limit int) ([]port.ManagementProcessMetricTrendAggregate, error) {
	rows, err := s.pool.Query(ctx, managementSystemMetricsProcessTrendSQL, input.WindowKey, input.StartDate, input.EndDate, input.ProcessRoles, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]port.ManagementProcessMetricTrendAggregate, 0)
	for rows.Next() {
		var row port.ManagementProcessMetricTrendAggregate
		if err := rows.Scan(
			&row.StatHour, &row.ProcessRole, &row.SampleCount,
			&row.EventLoopLagMSSum, &row.EventLoopLagMSSampleCount, &row.EventLoopLagMSMax,
			&row.ProcessRSSBytesSum, &row.ProcessRSSBytesMax,
			&row.ProcessHeapUsedBytesSum, &row.ProcessHeapUsedBytesMax,
			&row.ProcessHeapTotalBytesSum, &row.ProcessHeapTotalBytesMax,
		); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s postgresManagementSystemMetricsSource) listLatest(ctx context.Context, roles []string, limit int) ([]port.ManagementProcessMetricSample, error) {
	rows, err := s.pool.Query(ctx, managementSystemMetricsLatestSQL, roles, limit)
	if err != nil {
		return nil, err
	}
	return scanManagementProcessMetricSamples(rows)
}

func (s postgresManagementSystemMetricsSource) listPeak(ctx context.Context, roles []string, startedAt string, limit int) ([]port.ManagementProcessMetricSample, error) {
	rows, err := s.pool.Query(ctx, managementSystemMetricsPeakSQL, roles, startedAt, limit)
	if err != nil {
		return nil, err
	}
	return scanManagementProcessMetricSamples(rows)
}

func scanManagementProcessMetricSamples(rows pgx.Rows) ([]port.ManagementProcessMetricSample, error) {
	defer rows.Close()
	result := make([]port.ManagementProcessMetricSample, 0)
	for rows.Next() {
		var row port.ManagementProcessMetricSample
		if err := rows.Scan(
			&row.ProcessRole, &row.ProcessPID, &row.SampledAt, &row.EventLoopLagMS,
			&row.ProcessRSSBytes, &row.ProcessHeapUsedBytes, &row.ProcessHeapTotalBytes,
			&row.ProcessExternalBytes, &row.ProcessArrayBuffersBytes,
		); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

var _ port.ManagementSystemMetricsReader = (*Store)(nil)
