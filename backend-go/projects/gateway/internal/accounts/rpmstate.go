package accounts

// AccountRPMState 是账户列表运行态 RPM overlay 的单账户快照。
//
// 由网关进程内并发 tracker（生产单机 go-only 形态）按账户资源 ID 一次性批量
// 读取：Used 为本自然分钟已派发的请求次数，ResetAtMs 为当前分钟窗口结束时刻
// （Unix 毫秒）。未配置 RPM 上限的账户 Limit 不在此结构承载——列表行的配置值
// 来自 accounts.rpm_limit 投影（RPMLimit），本结构只承载运行态。
type AccountRPMState struct {
	Used      int   `json:"used"`
	ResetAtMs int64 `json:"resetAtMs"`
}
