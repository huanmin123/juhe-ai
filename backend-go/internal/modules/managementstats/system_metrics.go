package managementstats

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const javaScriptISOStringLayout = "2006-01-02T15:04:05.000Z"

var systemMetricsProcessRoles = []string{
	"server",
	"ingest-worker",
	"stats-worker",
	"ops-worker",
	"db-service",
}

type SystemMetricsQuery struct {
	StartDate string
	EndDate   string
}

type SystemMetricsOverview struct {
	HourlyTrend                  []SystemMetricsHourly        `json:"hourlyTrend"`
	ProcessEventLoopLatestStatus []SystemMetricsProcessStatus `json:"processEventLoopLatestStatus"`
	ProcessEventLoopPeakStatus   []SystemMetricsProcessStatus `json:"processEventLoopPeakStatus"`
	ProcessEventLoopTrend        []SystemMetricsProcessTrend  `json:"processEventLoopTrend"`
}

type SystemMetricsHourly struct {
	StatHour                   string   `json:"statHour"`
	SampleCount                int64    `json:"sampleCount"`
	CPUPercentAvg              *float64 `json:"cpuPercentAvg,omitempty"`
	CPUPercentMax              *float64 `json:"cpuPercentMax,omitempty"`
	MemoryUsedPercentAvg       *float64 `json:"memoryUsedPercentAvg,omitempty"`
	MemoryUsedPercentMax       *float64 `json:"memoryUsedPercentMax,omitempty"`
	EventLoopLagMSSampleCount  int64    `json:"eventLoopLagMsSampleCount"`
	EventLoopLagMSAvg          *float64 `json:"eventLoopLagMsAvg,omitempty"`
	EventLoopLagMSMax          *float64 `json:"eventLoopLagMsMax,omitempty"`
	NetworkRXBytesPerSecondAvg *float64 `json:"networkRxBytesPerSecondAvg,omitempty"`
	NetworkRXBytesPerSecondMax *float64 `json:"networkRxBytesPerSecondMax,omitempty"`
	NetworkTXBytesPerSecondAvg *float64 `json:"networkTxBytesPerSecondAvg,omitempty"`
	NetworkTXBytesPerSecondMax *float64 `json:"networkTxBytesPerSecondMax,omitempty"`
	NetworkRXTotalBytesMax     *int64   `json:"networkRxTotalBytesMax,omitempty"`
	NetworkTXTotalBytesMax     *int64   `json:"networkTxTotalBytesMax,omitempty"`
	ProcessRSSBytesMax         *int64   `json:"processRssBytesMax,omitempty"`
	ProcessHeapUsedBytesMax    *int64   `json:"processHeapUsedBytesMax,omitempty"`
	DBFileBytesMax             *int64   `json:"dbFileBytesMax,omitempty"`
	StatsLagSecondsMax         *int64   `json:"statsLagSecondsMax,omitempty"`
}

type SystemMetricsProcessStatus struct {
	ProcessRole              string   `json:"processRole"`
	SampleAvailable          bool     `json:"sampleAvailable"`
	ProcessPID               *int64   `json:"processPid"`
	SampledAt                *string  `json:"sampledAt"`
	EventLoopLagMS           *float64 `json:"eventLoopLagMs"`
	ProcessRSSBytes          *int64   `json:"processRssBytes"`
	ProcessHeapUsedBytes     *int64   `json:"processHeapUsedBytes"`
	ProcessHeapTotalBytes    *int64   `json:"processHeapTotalBytes"`
	ProcessExternalBytes     *int64   `json:"processExternalBytes"`
	ProcessArrayBuffersBytes *int64   `json:"processArrayBuffersBytes"`
}

type SystemMetricsProcessTrend struct {
	StatHour                  string   `json:"statHour"`
	StatMinute                string   `json:"statMinute"`
	ProcessRole               string   `json:"processRole"`
	SampleCount               int64    `json:"sampleCount"`
	EventLoopLagMSSampleCount int64    `json:"eventLoopLagMsSampleCount"`
	EventLoopLagMSAvg         *float64 `json:"eventLoopLagMsAvg,omitempty"`
	EventLoopLagMSMax         *float64 `json:"eventLoopLagMsMax,omitempty"`
	ProcessRSSBytesAvg        *float64 `json:"processRssBytesAvg,omitempty"`
	ProcessRSSBytesMax        *int64   `json:"processRssBytesMax,omitempty"`
	ProcessHeapUsedBytesAvg   *float64 `json:"processHeapUsedBytesAvg,omitempty"`
	ProcessHeapUsedBytesMax   *int64   `json:"processHeapUsedBytesMax,omitempty"`
	ProcessHeapTotalBytesAvg  *float64 `json:"processHeapTotalBytesAvg,omitempty"`
	ProcessHeapTotalBytesMax  *int64   `json:"processHeapTotalBytesMax,omitempty"`
}

func (s *Service) SystemMetrics(ctx context.Context, query SystemMetricsQuery) (SystemMetricsOverview, error) {
	if s.systemMetricsStore == nil {
		return SystemMetricsOverview{}, fmt.Errorf("management system metrics store is required")
	}
	cacheNow := s.now()
	_, location, err := s.usageStatsTimezone(ctx, cacheNow)
	if err != nil {
		return SystemMetricsOverview{}, err
	}
	now := s.now()
	rangeValue := normalizeSystemMetricsRange(query, now, location)
	snapshot, err := s.systemMetricsStore.ReadManagementSystemMetrics(ctx, port.ManagementSystemMetricsReadInput{
		WindowKey:     rangeValue.StartDate + ":" + rangeValue.EndDate,
		StartDate:     rangeValue.StartDate,
		EndDate:       rangeValue.EndDate,
		Days:          rangeValue.Days,
		BucketHours:   systemMetricsBucketHours(rangeValue.Days),
		PeakStartedAt: now.Add(-24 * time.Hour).UTC().Truncate(time.Millisecond).Format(javaScriptISOStringLayout),
		ProcessRoles:  append([]string(nil), systemMetricsProcessRoles...),
	})
	if err != nil {
		return SystemMetricsOverview{}, err
	}
	return mapSystemMetricsOverview(snapshot), nil
}

func normalizeSystemMetricsRange(query SystemMetricsQuery, now time.Time, location *time.Location) UsageWindow {
	todayText := now.In(location).Format(time.DateOnly)
	today, _ := time.Parse(time.DateOnly, todayText)
	earliestSupported := today.AddDate(0, 0, -(usageWindowDays - 1))
	startText := systemMetricsTrimECMAScriptWhitespace(query.StartDate)
	endText := systemMetricsTrimECMAScriptWhitespace(query.EndDate)
	if startText == "" && endText != "" {
		startText = endText
	}
	if endText == "" && startText != "" {
		endText = startText
	}
	end, ok := parseSystemMetricsDate(endText)
	if !ok {
		end = today
	}
	end = clampSystemMetricsDate(end, earliestSupported, today)
	start, ok := parseSystemMetricsDate(startText)
	if !ok {
		start = today
	}
	start = clampSystemMetricsDate(start, earliestSupported, today)
	if start.After(end) {
		start = end
	}
	earliestStart := end.AddDate(0, 0, -(usageWindowDays - 1))
	if start.Before(earliestStart) {
		start = earliestStart
	}
	return UsageWindow{
		StartDate: start.Format(time.DateOnly),
		EndDate:   end.Format(time.DateOnly),
		Days:      int(end.Sub(start).Hours()/24) + 1,
		MaxDays:   usageWindowDays,
	}
}

func systemMetricsBucketHours(days int) int {
	if days <= 1 {
		return 1
	}
	if days <= 3 {
		return 6
	}
	return 24
}

func mapSystemMetricsOverview(snapshot port.ManagementSystemMetricsSnapshot) SystemMetricsOverview {
	overview := SystemMetricsOverview{
		HourlyTrend:                  make([]SystemMetricsHourly, 0, len(snapshot.HourlyTrend)),
		ProcessEventLoopLatestStatus: mapSystemMetricsProcessStatus(snapshot.ProcessLatest),
		ProcessEventLoopPeakStatus:   mapSystemMetricsProcessStatus(snapshot.ProcessPeak),
		ProcessEventLoopTrend:        make([]SystemMetricsProcessTrend, 0, len(snapshot.ProcessTrend)),
	}
	for _, row := range snapshot.HourlyTrend {
		overview.HourlyTrend = append(overview.HourlyTrend, SystemMetricsHourly{
			StatHour:                   row.StatHour,
			SampleCount:                row.SampleCount,
			CPUPercentAvg:              roundedSystemMetricAverage(row.CPUPercentSum, row.SampleCount),
			CPUPercentMax:              row.CPUPercentMax,
			MemoryUsedPercentAvg:       roundedSystemMetricAverage(row.MemoryUsedPercentSum, row.SampleCount),
			MemoryUsedPercentMax:       row.MemoryUsedPercentMax,
			EventLoopLagMSSampleCount:  row.EventLoopLagMSSampleCount,
			EventLoopLagMSAvg:          roundedSystemMetricAverage(row.EventLoopLagMSSum, row.EventLoopLagMSSampleCount),
			EventLoopLagMSMax:          row.EventLoopLagMSMax,
			NetworkRXBytesPerSecondAvg: roundedSystemMetricAverage(row.NetworkRXBytesPerSecondSum, row.NetworkRXBytesPerSecondCount),
			NetworkRXBytesPerSecondMax: row.NetworkRXBytesPerSecondMax,
			NetworkTXBytesPerSecondAvg: roundedSystemMetricAverage(row.NetworkTXBytesPerSecondSum, row.NetworkTXBytesPerSecondCount),
			NetworkTXBytesPerSecondMax: row.NetworkTXBytesPerSecondMax,
			NetworkRXTotalBytesMax:     row.NetworkRXTotalBytesMax,
			NetworkTXTotalBytesMax:     row.NetworkTXTotalBytesMax,
			ProcessRSSBytesMax:         row.ProcessRSSBytesMax,
			ProcessHeapUsedBytesMax:    row.ProcessHeapUsedBytesMax,
			DBFileBytesMax:             row.DBFileBytesMax,
			StatsLagSecondsMax:         row.StatsLagSecondsMax,
		})
	}
	for _, row := range snapshot.ProcessTrend {
		overview.ProcessEventLoopTrend = append(overview.ProcessEventLoopTrend, SystemMetricsProcessTrend{
			StatHour:                  row.StatHour,
			StatMinute:                row.StatHour,
			ProcessRole:               row.ProcessRole,
			SampleCount:               row.SampleCount,
			EventLoopLagMSSampleCount: row.EventLoopLagMSSampleCount,
			EventLoopLagMSAvg:         roundedSystemMetricAverage(row.EventLoopLagMSSum, row.EventLoopLagMSSampleCount),
			EventLoopLagMSMax:         row.EventLoopLagMSMax,
			ProcessRSSBytesAvg:        roundedSystemMetricAverage(float64(row.ProcessRSSBytesSum), row.SampleCount),
			ProcessRSSBytesMax:        row.ProcessRSSBytesMax,
			ProcessHeapUsedBytesAvg:   roundedSystemMetricAverage(float64(row.ProcessHeapUsedBytesSum), row.SampleCount),
			ProcessHeapUsedBytesMax:   row.ProcessHeapUsedBytesMax,
			ProcessHeapTotalBytesAvg:  roundedSystemMetricAverage(float64(row.ProcessHeapTotalBytesSum), row.SampleCount),
			ProcessHeapTotalBytesMax:  row.ProcessHeapTotalBytesMax,
		})
	}
	return overview
}

func mapSystemMetricsProcessStatus(rows []port.ManagementProcessMetricSample) []SystemMetricsProcessStatus {
	byRole := make(map[string]port.ManagementProcessMetricSample, len(rows))
	for _, row := range rows {
		if _, exists := byRole[row.ProcessRole]; !exists {
			byRole[row.ProcessRole] = row
		}
	}
	result := make([]SystemMetricsProcessStatus, 0, len(systemMetricsProcessRoles))
	for _, role := range systemMetricsProcessRoles {
		row, found := byRole[role]
		if !found {
			result = append(result, SystemMetricsProcessStatus{ProcessRole: role})
			continue
		}
		sampledAt := row.SampledAt
		result = append(result, SystemMetricsProcessStatus{
			ProcessRole:              role,
			SampleAvailable:          true,
			ProcessPID:               row.ProcessPID,
			SampledAt:                &sampledAt,
			EventLoopLagMS:           row.EventLoopLagMS,
			ProcessRSSBytes:          row.ProcessRSSBytes,
			ProcessHeapUsedBytes:     row.ProcessHeapUsedBytes,
			ProcessHeapTotalBytes:    row.ProcessHeapTotalBytes,
			ProcessExternalBytes:     row.ProcessExternalBytes,
			ProcessArrayBuffersBytes: row.ProcessArrayBuffersBytes,
		})
	}
	return result
}

func roundedSystemMetricAverage(sum float64, count int64) *float64 {
	if count <= 0 {
		return nil
	}
	value := math.Floor(sum/float64(count) + 0.5)
	return &value
}

func parseSystemMetricsDate(value string) (time.Time, bool) {
	if len(value) != len(time.DateOnly) || value[4] != '-' || value[7] != '-' {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.DateOnly, value)
	return parsed, err == nil
}

func clampSystemMetricsDate(value time.Time, earliest time.Time, latest time.Time) time.Time {
	if value.Before(earliest) {
		return earliest
	}
	if value.After(latest) {
		return latest
	}
	return value
}

func systemMetricsTrimECMAScriptWhitespace(value string) string {
	return strings.TrimFunc(value, systemMetricsECMAScriptWhitespace)
}

func systemMetricsECMAScriptWhitespace(character rune) bool {
	switch character {
	case '\u0009', '\u000A', '\u000B', '\u000C', '\u000D', '\u0020',
		'\u00A0', '\u1680', '\u2028', '\u2029', '\u202F', '\u205F',
		'\u3000', '\uFEFF':
		return true
	default:
		return character >= '\u2000' && character <= '\u200A'
	}
}
