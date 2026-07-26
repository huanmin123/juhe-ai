import { logger } from '../../shared/logger.js'
import { requestBackgroundWorkerDbService } from './background-ipc.js'
import { runModelCheck, type ModelCheckProgressEvent } from '../model-checks/model-checks.service.js'

const scheduledModelQualityBatchSize = 3
const scheduledModelQualityRunTimeoutMs = 5 * 60_000

export interface ModelQualityScheduledCheckBatchResult {
  claimed: number
  completed: number
  failed: number
}

export async function runDueModelQualityScheduledChecks(): Promise<ModelQualityScheduledCheckBatchResult> {
  const ownerId = `ops-worker:${process.pid}:${Date.now()}`
  const claimResult = await requestBackgroundWorkerDbService({
    type: 'model_quality_command',
    command: {
      kind: 'claim_due_schedules',
      ownerId,
      now: new Date().toISOString(),
      limit: scheduledModelQualityBatchSize,
      leaseMinutes: 6
    }
  })
  const candidates = claimResult?.kind === 'claimed' ? claimResult.candidates : []
  const result: ModelQualityScheduledCheckBatchResult = { claimed: candidates.length, completed: 0, failed: 0 }
  for (const candidate of candidates) {
    let runId: string | undefined
    let status: 'completed' | 'failed' | 'canceled' = 'failed'
    try {
      const detail = await runModelCheck(
        {
          targetType: 'account',
          targetId: candidate.accountId,
          model: candidate.model,
          profile: candidate.policy.profile,
          trustedComparison: false
        },
        { systemAccountId: candidate.systemAccountId, role: 'user' },
        AbortSignal.timeout(scheduledModelQualityRunTimeoutMs),
        (event: ModelCheckProgressEvent) => {
          if (event.type === 'run_created') runId = event.runId
        },
        {
          triggerKind: 'scheduled',
          scheduleId: candidate.scheduleId,
          policy: candidate.policy
        }
      )
      runId = detail.id
      status = detail.status === 'running' ? 'failed' : detail.status
      if (status === 'completed') result.completed += 1
      else result.failed += 1
    } catch (error) {
      result.failed += 1
      logger.error({
        event: 'model_quality_scheduled_check_failed',
        scheduleId: candidate.scheduleId,
        accountId: candidate.accountId,
        runId,
        err: error
      }, '定时模型质量检查失败')
    } finally {
      const completion = await requestBackgroundWorkerDbService({
        type: 'model_quality_command',
        command: {
          kind: 'complete_schedule_run',
          input: {
            ownerId,
            scheduleId: candidate.scheduleId,
            scheduleRevision: candidate.scheduleRevision,
            intervalMinutes: candidate.intervalMinutes,
            runId,
            status,
            completedAt: new Date().toISOString()
          }
        }
      })
      if (completion?.kind !== 'completed' || !completion.completed) {
        logger.warn({
          event: 'model_quality_schedule_completion_stale',
          scheduleId: candidate.scheduleId,
          scheduleRevision: candidate.scheduleRevision,
          runId
        }, '定时模型质量检查完成写回未命中，计划可能已被编辑或删除')
      }
    }
  }
  return result
}

export async function runDueModelQualityRecoveries(): Promise<ModelQualityScheduledCheckBatchResult> {
  const ownerId = `ops-worker-recovery:${process.pid}:${Date.now()}`
  const claimResult = await requestBackgroundWorkerDbService({
    type: 'model_quality_command',
    command: {
      kind: 'claim_due_recoveries',
      ownerId,
      now: new Date().toISOString(),
      limit: 2,
      leaseMinutes: 6
    }
  })
  const candidates = claimResult?.kind === 'recoveries_claimed' ? claimResult.candidates : []
  const result: ModelQualityScheduledCheckBatchResult = { claimed: candidates.length, completed: 0, failed: 0 }
  for (const candidate of candidates) {
    try {
      const detail = await runModelCheck(
        {
          targetType: 'account',
          targetId: candidate.accountId,
          model: candidate.model,
          profile: candidate.policy.profile,
          trustedComparison: false
        },
        { systemAccountId: candidate.systemAccountId, role: 'user' },
        AbortSignal.timeout(scheduledModelQualityRunTimeoutMs),
        undefined,
        {
          triggerKind: 'quality_recovery',
          policy: candidate.policy,
          recovery: {
            ownerId,
            enforcementId: candidate.enforcementId,
            generation: candidate.generation
          }
        }
      )
      if (detail.status === 'completed') result.completed += 1
      else result.failed += 1
    } catch (error) {
      result.failed += 1
      logger.error({
        event: 'model_quality_recovery_check_failed',
        accountId: candidate.accountId,
        enforcementId: candidate.enforcementId,
        generation: candidate.generation,
        err: error
      }, '质量隔离恢复检查失败，租约到期后将自动重试')
    }
  }
  return result
}
