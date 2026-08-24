// Package schedulejitter keeps the jobs import path stable while sharing the
// cross-process passive scheduling implementation with Go gateway sidecars.
package schedulejitter

import (
	"time"

	platformjitter "github.com/huanminabc/juhe-ai/backend-go-platform/schedulejitter"
)

const (
	SubMinuteWindow = platformjitter.SubMinuteWindow
	MinuteWindow    = platformjitter.MinuteWindow
	HourWindow      = platformjitter.HourWindow
	DayWindow       = platformjitter.DayWindow
	WeekWindow      = platformjitter.WeekWindow
)

func Window(interval time.Duration) time.Duration { return platformjitter.Window(interval) }
func Offset(interval time.Duration) time.Duration { return platformjitter.Offset(interval) }
func Delay(interval time.Duration) time.Duration  { return platformjitter.Delay(interval) }
