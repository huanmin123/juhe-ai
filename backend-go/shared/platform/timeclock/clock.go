// Package timeclock 提供跨项目统一的时钟注入接口。
// 收敛各包自写的 Clock/SystemClock 定义（评估文档 A1：11 个包签名漂移），
// 统一签名为 Now() time.Time；Sleep/TimerFactory 等调度域扩展接口由各包
// 自行定义（如 statsverify 的 Sleep、jobsched 的 Timer），不并入本包。
package timeclock

import "time"

// Clock 是跨包统一的最小时钟接口。
type Clock interface {
	Now() time.Time
}

// SystemClock 用真实墙钟实现 Clock。
type SystemClock struct{}

// Now 返回当前墙钟时间。
func (SystemClock) Now() time.Time { return time.Now() }

// ClockFunc 适配函数为 Clock；nil 时退回 SystemClock。
type ClockFunc func() time.Time

// Now 实现 Clock。
func (f ClockFunc) Now() time.Time {
	if f == nil {
		return SystemClock{}.Now()
	}
	return f()
}
