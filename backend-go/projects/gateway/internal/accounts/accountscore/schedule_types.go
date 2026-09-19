package accountscore

// Availability schedule model mirrors storage/api-key-availability-schedule.ts
// (accounts reuse the api-key schedule domain verbatim through
// storage/account-availability-schedule.ts, including the validation
// messages) and the stored JSON shape (enabled/timezone/mode/windows/
// dateRange/exceptions) written by apiKeyAvailabilityScheduleJson.
//
// REFACTOR-0005 阶段 C 下沉：类型是门面与 accountstransfer（导出文档、批量
// 锁定行投影）之间的跨包契约值；解析/求值逻辑（NormalizeSchedule 等）保留
// 在门面 schedule.go，经 Deps 函数端口注入 transfer 子域。

// ScheduleWindow is one allowed window; DaysOfWeek is 1..7 (Monday..Sunday).
type ScheduleWindow struct {
	DaysOfWeek []int  `json:"daysOfWeek"`
	Start      string `json:"start"`
	End        string `json:"end"`
}

// ScheduleException overrides a single date with allow windows or a deny.
type ScheduleException struct {
	Date    string           `json:"date"`
	Action  string           `json:"action"`
	Windows []ScheduleWindow `json:"windows,omitempty"`
}

// ScheduleDateRange bounds the whole schedule to a date interval.
type ScheduleDateRange struct {
	StartDate string `json:"startDate,omitempty"`
	EndDate   string `json:"endDate,omitempty"`
}

// AvailabilitySchedule is the stored/normalized schedule document.
type AvailabilitySchedule struct {
	Enabled    bool                `json:"enabled"`
	Timezone   string              `json:"timezone"`
	Mode       string              `json:"mode"`
	Windows    []ScheduleWindow    `json:"windows"`
	DateRange  *ScheduleDateRange  `json:"dateRange,omitempty"`
	Exceptions []ScheduleException `json:"exceptions,omitempty"`
}
