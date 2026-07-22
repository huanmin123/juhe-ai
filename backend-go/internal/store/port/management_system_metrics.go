package port

import "context"

type ManagementSystemMetricsReadInput struct {
	WindowKey     string
	StartDate     string
	EndDate       string
	Days          int
	BucketHours   int
	PeakStartedAt string
	ProcessRoles  []string
}

type ManagementSystemMetricsHourlyAggregate struct {
	StatHour                     string
	SampleCount                  int64
	CPUPercentSum                float64
	CPUPercentMax                *float64
	MemoryUsedPercentSum         float64
	MemoryUsedPercentMax         *float64
	ProcessRSSBytesMax           *int64
	ProcessHeapUsedBytesMax      *int64
	EventLoopLagMSSum            float64
	EventLoopLagMSSampleCount    int64
	EventLoopLagMSMax            *float64
	NetworkRXBytesPerSecondSum   float64
	NetworkRXBytesPerSecondMax   *float64
	NetworkRXBytesPerSecondCount int64
	NetworkTXBytesPerSecondSum   float64
	NetworkTXBytesPerSecondMax   *float64
	NetworkTXBytesPerSecondCount int64
	NetworkRXTotalBytesMax       *int64
	NetworkTXTotalBytesMax       *int64
	DBFileBytesMax               *int64
	StatsLagSecondsMax           *int64
}

type ManagementProcessMetricSample struct {
	ProcessRole              string
	ProcessPID               *int64
	SampledAt                string
	EventLoopLagMS           *float64
	ProcessRSSBytes          *int64
	ProcessHeapUsedBytes     *int64
	ProcessHeapTotalBytes    *int64
	ProcessExternalBytes     *int64
	ProcessArrayBuffersBytes *int64
}

type ManagementProcessMetricTrendAggregate struct {
	StatHour                  string
	ProcessRole               string
	SampleCount               int64
	EventLoopLagMSSum         float64
	EventLoopLagMSSampleCount int64
	EventLoopLagMSMax         *float64
	ProcessRSSBytesSum        int64
	ProcessRSSBytesMax        *int64
	ProcessHeapUsedBytesSum   int64
	ProcessHeapUsedBytesMax   *int64
	ProcessHeapTotalBytesSum  int64
	ProcessHeapTotalBytesMax  *int64
}

type ManagementSystemMetricsSnapshot struct {
	HourlyTrend   []ManagementSystemMetricsHourlyAggregate
	ProcessLatest []ManagementProcessMetricSample
	ProcessPeak   []ManagementProcessMetricSample
	ProcessTrend  []ManagementProcessMetricTrendAggregate
}

type ManagementSystemMetricsReader interface {
	ReadManagementSystemMetrics(ctx context.Context, input ManagementSystemMetricsReadInput) (ManagementSystemMetricsSnapshot, error)
}
