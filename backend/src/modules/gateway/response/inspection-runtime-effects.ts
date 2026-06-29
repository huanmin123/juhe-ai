import type { GatewaySettings } from '../policy/account-error-policy.service.js'
import type { AuditCaptureContext } from '../audit/capture.service.js'
import { responseInspectionAuditMetadata } from '../audit/metadata.js'
import {
  recordGatewayAccountFailureForPrecheck,
  suppressGatewayAccountLocally
} from '../runtime/account-side-effects.service.js'
import {
  suppressGatewayUpstreamBucketLocallyForSeconds
} from '../runtime/proxy-health.service.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import type { ResponseInspectionDecision } from './inspection.js'
import type { GatewayUsageContext } from '../usage/records.js'

export async function applyResponseInspectionPolicyRuntimeSideEffects(
  decision: ResponseInspectionDecision,
  account: UpstreamAccount,
  settings: GatewaySettings,
  accountStateMutationEnabled: boolean,
  usageContext?: GatewayUsageContext
): Promise<void> {
  if (!accountStateMutationEnabled || decision.reason !== 'configured_response_policy' || decision.action === 'dry_run') {
    return
  }
  const reason = `响应检查策略命中：${decision.policyName ?? decision.policyId ?? decision.matchedValue ?? '未命名策略'}`
  if (decision.accountState === 'runtime_avoidance' || decision.accountSwitch === 'avoid_account_ttl') {
    const localSuppression = suppressGatewayAccountLocally(account, settings, reason)
    if (usageContext?.trafficSource === 'gateway') {
      recordGatewayAccountFailureForPrecheck(account, settings, {
        systemAccountId: usageContext.systemAccountId,
        groupId: usageContext.groupId,
        apiKeyId: usageContext.apiKeyId,
        clientIp: usageContext.clientIp,
        endpoint: usageContext.endpoint,
        reason,
        forcePrecheck: localSuppression.action === 'precheck_required'
      })
    }
  }
  if (decision.accountSwitch === 'avoid_upstream_bucket_ttl') {
    const ttlSeconds = Math.max(1, settings.defaultTemporaryUnschedulableMinutes * 60)
    suppressGatewayUpstreamBucketLocallyForSeconds(account, ttlSeconds, reason)
  }
}

export async function applyResponseInspectionObservationDecisions(
  observations: ResponseInspectionDecision[] | undefined,
  omittedCount: number | undefined,
  account: UpstreamAccount,
  settings: GatewaySettings,
  auditCapture: AuditCaptureContext,
  accountStateMutationEnabled: boolean,
  usageContext?: GatewayUsageContext
): Promise<void> {
  observations = observations ?? []
  if (observations.length === 0) {
    return
  }
  for (const observation of observations) {
    await applyResponseInspectionPolicyRuntimeSideEffects(observation, account, settings, accountStateMutationEnabled, usageContext)
  }
  auditCapture.addGatewayMetadata({
    label: 'response_inspection_observations',
    metadata: {
      count: observations.length,
      omittedCount,
      observations: observations.map(responseInspectionAuditMetadata)
    }
  })
}
