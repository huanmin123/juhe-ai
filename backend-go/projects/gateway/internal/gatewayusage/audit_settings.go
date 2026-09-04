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

// ResolveAuditCaptureLimits mirrors the constructor bound extraction:
// activeCaptureMaxBytes is clamped by the hard limit.
func ResolveAuditCaptureLimits(settings AuditLogSettings) (activeCaptureMaxBytes int) {
	active := settings.ActiveCaptureMaxBytes
	if active > DefaultAuditCaptureHardLimitBytes {
		active = DefaultAuditCaptureHardLimitBytes
	}
	return active
}
