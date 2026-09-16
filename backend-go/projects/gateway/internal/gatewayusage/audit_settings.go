package gatewayusage

// Audit capture settings mirroring
// backend/src/modules/audit-logs/audit-log-settings.ts.

// AuditLogSettings mirrors AuditLogSettings.
type AuditLogSettings struct {
	Enabled                  bool
	FullBodyCaptureEnabled   bool
	SuccessSampleRate        float64
	ActiveCaptureMaxBytes    int
	SuccessHotRetentionHours int
	SuccessRetentionDays     int
	ProblemRetentionDays     int
	SuccessFullBodyLimitBytes int
	ProblemFullBodyLimitBytes int
}

// auditLogMb mirrors the auditLogMb constant.
const auditLogMb = 1024 * 1024

// AuditLogSettingsSource ports readAuditLogSettings(): the runtime config is
// the single source of the audit master switch and its env-merged values.
// The G20 assembly adapts it to the Go runtime config.
type AuditLogSettingsSource interface {
	ReadAuditLogSettings() AuditLogSettings
}

// FixedAuditLogSettingsSource mirrors readAuditLogSettings returning the
// frozen fixedAuditLogSettings object.
type FixedAuditLogSettingsSource struct {
	// Settings is returned verbatim on every read.
	Settings AuditLogSettings
}

// ReadAuditLogSettings implements AuditLogSettingsSource.
func (s FixedAuditLogSettingsSource) ReadAuditLogSettings() AuditLogSettings {
	return s.Settings
}

// DefaultAuditCaptureHardLimitBytes mirrors auditActiveCaptureHardLimitBytes.
const DefaultAuditCaptureHardLimitBytes = 64 * 1024 * 1024

// 审计捕获默认上限（docs/functions/原始审计日志设计.md fixedAuditLogSettings
// 表）：设置源未提供 limit（<=0）时回落。生产设置源适配器当前只传递
// Enabled 位、无法表达显式 0（hash_only），因此任何 <=0 值均按「设置缺失」
// 处理；显式 hash_only 需由未来能传递数值的设置源承载。
const (
	// DefaultAuditSuccessFullBodyLimitBytes mirrors successFullBodyLimitBytes（512KB）。
	DefaultAuditSuccessFullBodyLimitBytes = 512 * 1024
	// DefaultAuditProblemFullBodyLimitBytes mirrors problemFullBodyLimitBytes（2MB）。
	DefaultAuditProblemFullBodyLimitBytes = 2 * auditLogMb
)

// ResolveAuditCaptureLimits mirrors the constructor bound extraction:
// activeCaptureMaxBytes is clamped by the hard limit. 未提供（<=0）回落
// 64MB 默认，避免活跃捕获预算为 0 把全部载荷判 overflow。
func ResolveAuditCaptureLimits(settings AuditLogSettings) (activeCaptureMaxBytes int) {
	active := settings.ActiveCaptureMaxBytes
	if active <= 0 {
		active = DefaultAuditCaptureHardLimitBytes
	}
	if active > DefaultAuditCaptureHardLimitBytes {
		active = DefaultAuditCaptureHardLimitBytes
	}
	return active
}

// ResolveAuditSuccessFullBodyLimitBytes 回落成功正文完整保留上限（512KB）。
func ResolveAuditSuccessFullBodyLimitBytes(settings AuditLogSettings) int {
	if settings.SuccessFullBodyLimitBytes <= 0 {
		return DefaultAuditSuccessFullBodyLimitBytes
	}
	return settings.SuccessFullBodyLimitBytes
}

// ResolveAuditProblemFullBodyLimitBytes 回落问题正文完整保留上限（2MB）。
func ResolveAuditProblemFullBodyLimitBytes(settings AuditLogSettings) int {
	if settings.ProblemFullBodyLimitBytes <= 0 {
		return DefaultAuditProblemFullBodyLimitBytes
	}
	return settings.ProblemFullBodyLimitBytes
}
