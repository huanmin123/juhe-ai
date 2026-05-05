import { cleanupAuditLogsBefore } from '../../storage/repositories.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { readAuditLogSettings } from './audit-log-settings.js'

let cleanupRunning = false

export function cleanupExpiredAuditLogs(): number {
  if (cleanupRunning) return 0
  cleanupRunning = true
  try {
    const retentionDays = readAuditLogSettings().retentionDays
    const cutoff = new Date(Date.now() - retentionDays * 24 * 60 * 60_000).toISOString()
    const deletedCount = cleanupAuditLogsBefore(cutoff)
    if (deletedCount > 0) {
      logger.info({
        event: 'audit_log_cleanup_completed',
        deletedCount,
        cutoff,
        retentionDays
      }, 'Audit log cleanup completed')
    }
    return deletedCount
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'audit_log_cleanup_failed' }), 'Audit log cleanup failed')
    return 0
  } finally {
    cleanupRunning = false
  }
}
