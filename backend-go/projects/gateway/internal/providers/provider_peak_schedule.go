// provider_peak_schedule.go 承载供应商高峰时段的目录级权威定义（单一数据源）：
// 时间计划前端快捷预设从 GET /providers(/definitions) 的 peakWindows 动态
// 生成，不再在前端硬编码供应商高峰（2026-11-10 用户裁决：项目与调用方均在
// 国内，高峰口径按国内业务高峰维护，随目录一处更新、全端联动）。
//
// 口径登记：DeepSeek 高峰为工作日 09:00-12:00 与 14:00-18:00，周末全天低峰
// （2026-11-10 用户定稿；与官方 API 错峰优惠定价 00:30-08:30 是两套口径，
// 此处按用户业务口径维护）。新增/调整供应商高峰只改本文件并发布。
package providers

// ProviderPeakWindow 是单个高峰时段（daysOfWeek 1=周一..7=周日；start/end
// 为 HH:mm，跨午夜以 start>end 表达）。
type ProviderPeakWindow struct {
	DaysOfWeek []int  `json:"daysOfWeek"`
	Start      string `json:"start"`
	End        string `json:"end"`
}

// ProviderPeakWindows 是供应商高峰定义（timezone 为高峰口径所在时区，
// 调用方生成调度时段时应把时间计划时区置为同值）。
type ProviderPeakWindows struct {
	Timezone string               `json:"timezone"`
	Windows  []ProviderPeakWindow `json:"windows"`
}

// providerPeakWindows 是 code → 高峰定义 的目录常量。仅内置当前有明确口径
// 的供应商；没有定义的供应商不出现在映射中，前端也不生成其快捷预设。
var providerPeakWindows = map[string]ProviderPeakWindows{
	"deepseek": {
		Timezone: "Asia/Shanghai",
		Windows: []ProviderPeakWindow{
			{DaysOfWeek: []int{1, 2, 3, 4, 5}, Start: "09:00", End: "12:00"},
			{DaysOfWeek: []int{1, 2, 3, 4, 5}, Start: "14:00", End: "18:00"},
		},
	},
}

// peakWindowsForProvider 返回供应商的高峰定义；无定义返回 nil（DTO 省略字段）。
func peakWindowsForProvider(code string) *ProviderPeakWindows {
	if peak, ok := providerPeakWindows[code]; ok {
		copy := peak
		return &copy
	}
	return nil
}
