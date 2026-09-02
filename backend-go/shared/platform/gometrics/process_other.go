//go:build !linux && !darwin

package gometrics

// RSS and descriptor counts are OS process metrics. Keep them unavailable on
// platforms without a portable standard-library source rather than reporting
// Go heap size as RSS or inventing a value.
func readRSSBytes() uint64 { return 0 }

func readFDCount() uint64 { return 0 }

func readProcessCPUSeconds() (float64, bool) { return 0, false }
