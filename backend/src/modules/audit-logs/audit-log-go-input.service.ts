import { createHmac } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import type { AuditLogInput } from '../../storage/audit-log-types.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { prepareAuditLogForGoInput } from './audit-log-go-input-budget.js'

export { auditLogGoInputMaxBytes } from './audit-log-go-input-budget.js'

export const auditLogGoInputPath = '/__aiinternal__/v1/audit-captures'
const auditLogGoInputSignatureDomain = 'juhe-ai/audit-log-input/v1'

// This is a one-shot RPC, deliberately without local buffering, retries,
// cross-process fallback, or a Node writer. The caller remains non-blocking and
// records only an observable dispatch failure when the Go owner is unavailable.
export function dispatchAuditLogToGo(input: AuditLogInput): void {
  const origin = runtimeConfig.auditLogInputUrl?.trim()
  if (!origin) {
    logger.error({
      event: 'audit_log_go_input_unconfigured',
      auditLogId: input.id,
      traceId: input.traceId
    }, 'F3 Go 审计输入地址未配置，本条审计未投递')
    return
  }
  let prepared: ReturnType<typeof prepareAuditLogForGoInput>
  try {
    prepared = prepareAuditLogForGoInput(input, {
      successFullBodyLimitBytes: runtimeConfig.auditLog.successFullBodyLimitBytes,
      problemFullBodyLimitBytes: runtimeConfig.auditLog.problemFullBodyLimitBytes
    })
  } catch (error) {
    logger.warn({
      ...errorLogFields(error, {
        event: 'audit_log_go_input_budget_failed',
        auditLogId: input.id,
        traceId: input.traceId
      })
    }, 'F3 Go 审计输入无法在 4MiB 内收敛，本条审计未投递')
    return
  }
  const { body } = prepared
  // Copy into an exact ArrayBuffer. Buffer may be a view into a larger pool.
  const requestBody = new Uint8Array(body).buffer
  const signature = createHmac('sha256', runtimeConfig.auditLogInputSecret!)
    .update(auditLogGoInputSignatureDomain)
    .update('\n')
    .update(body)
    .digest('hex')
  const endpoint = new URL(auditLogGoInputPath, `${origin}/`).toString()
  void fetch(endpoint, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      'content-length': String(body.byteLength),
      'x-juhe-ai-signature': `v1=${signature}`,
      ...(input.traceId ? { 'x-trace-id': input.traceId } : {})
    },
    body: requestBody,
    signal: AbortSignal.timeout(runtimeConfig.auditLogInputTimeoutMs!)
  }).then(async (response) => {
    try {
      await response.body?.cancel()
    } catch {
      // The status code carries the complete one-shot dispatch result.
    }
    if (response.status === 204) return
    logger.warn({
      event: 'audit_log_go_input_rejected',
      auditLogId: input.id,
      traceId: input.traceId,
      statusCode: response.status
    }, 'F3 Go 审计输入未接受本条记录')
  }).catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'audit_log_go_input_failed',
      auditLogId: input.id,
      traceId: input.traceId
    }), 'F3 Go 审计输入请求失败，本条记录按 best-effort 丢弃')
  })
}
