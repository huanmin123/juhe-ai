import { errorLogFields, logger } from '../../shared/logger.js'
import type { UsageRecordListOptions, UsageRecordListResult, UsageRecordSummary } from '../../storage/usage-records.repository.js'
import { listUsageRecordsAsync } from '../../storage/usage-records.repository.js'
import {
  listUsageRecordFirstPagePrewarmCandidatesAsync,
  type UsageRecordFirstPagePrewarmCandidate
} from '../../storage/usage-record-first-page-prewarm.repository.js'
import { dateKey, startOfZonedDateKeyIso, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'
import { fixedUsageStatsDateKeys, nextDateKey } from '../../storage/usage-stats-window-helpers.js'
import {
  hasUsageRecordFirstPageForDate,
  seedUsageRecordFirstPageForDate
} from '../usage-records/usage-record-first-page-cache.service.js'

export const usageRecordFirstPagePrewarmCandidateLimit = 128
export const usageRecordFirstPagePrewarmBatchSize = 32
export const usageRecordFirstPagePrewarmHotCandidateCount = 8
export const usageRecordFirstPagePrewarmMaxRunMs = 10_000
export const usageRecordFirstPagePrewarmRotationIntervalMs = 30 * 60_000

interface SelectedUsageRecordFirstPagePrewarmCandidate {
  candidate: UsageRecordFirstPagePrewarmCandidate
  lane: 'hot' | 'rotating'
}

export interface UsageRecordFirstPagePrewarmDependencies {
  listCandidates: typeof listUsageRecordFirstPagePrewarmCandidatesAsync
  usageStatsTimezone: typeof usageStatsTimezoneAsync
  hasCachedPage: typeof hasUsageRecordFirstPageForDate
  listUsageRecords: (
    access: { systemAccountId: string; role: 'user' },
    options: UsageRecordListOptions
  ) => Promise<UsageRecordListResult>
  seedPage: (systemAccountId: string, date: string, rows: UsageRecordSummary[], options?: { signal?: AbortSignal; deadlineAtMs?: number }) => Promise<void>
  nowMs: () => number
}

export interface UsageRecordFirstPagePrewarmResult {
  outcome: 'success' | 'partial'
  warning?: string
  candidateCount: number
  selectedCount: number
  processedCount: number
  cacheHitCount: number
  seededCount: number
  failedCount: number
  budgetExhausted: boolean
  durationMs: number
  nextRotatingCursorId?: string
}

const defaultDependencies: UsageRecordFirstPagePrewarmDependencies = {
  listCandidates: listUsageRecordFirstPagePrewarmCandidatesAsync,
  usageStatsTimezone: usageStatsTimezoneAsync,
  hasCachedPage: hasUsageRecordFirstPageForDate,
  listUsageRecords: async (access, options) => await listUsageRecordsAsync(access, options),
  seedPage: seedUsageRecordFirstPageForDate,
  nowMs: Date.now
}

export async function runUsageRecordFirstPagePrewarmJob(
  dependencyOverrides: Partial<UsageRecordFirstPagePrewarmDependencies> = {},
  options: { signal?: AbortSignal } = {}
): Promise<UsageRecordFirstPagePrewarmResult> {
  const dependencies = { ...defaultDependencies, ...dependencyOverrides }
  const startedAtMs = dependencies.nowMs()
  const deadlineAtMs = startedAtMs + usageRecordFirstPagePrewarmMaxRunMs
  options.signal?.throwIfAborted()
  const timezone = await dependencies.usageStatsTimezone()
  options.signal?.throwIfAborted()
  const today = dateKey(new Date(startedAtMs), timezone)
  const fixedDates = fixedUsageStatsDateKeys(timezone, today)
  const startDate = fixedDates[Math.max(0, fixedDates.length - 7)] ?? today
  const tomorrow = nextDateKey(today)
  const listOptions: UsageRecordListOptions = {
    page: 1,
    pageSize: 20,
    sortBy: 'createdAt',
    sortOrder: 'desc',
    trafficSource: 'gateway',
    startAt: startOfZonedDateKeyIso(today, timezone),
    endAt: startOfZonedDateKeyIso(tomorrow, timezone)
  }
  const candidates = await dependencies.listCandidates({
    startDate,
    endDate: today,
    limit: usageRecordFirstPagePrewarmCandidateLimit
  })
  options.signal?.throwIfAborted()
  const rotationSlot = Math.floor(startedAtMs / usageRecordFirstPagePrewarmRotationIntervalMs)
  const selected = selectUsageRecordFirstPagePrewarmCandidates(candidates, rotationSlot)

  let processedCount = 0
  let cacheHitCount = 0
  let seededCount = 0
  let failedCount = 0
  let budgetExhausted = false

  for (const item of selected) {
    if (options.signal?.aborted || dependencies.nowMs() >= deadlineAtMs) {
      budgetExhausted = true
      break
    }
    const systemAccountId = item.candidate.systemAccountId
    try {
      if (await dependencies.hasCachedPage(systemAccountId, today, { signal: options.signal, deadlineAtMs })) {
        cacheHitCount += 1
      } else {
        const access = { systemAccountId, role: 'user' as const }
        const result = await dependencies.listUsageRecords(access, listOptions)
        await dependencies.seedPage(systemAccountId, today, result.items, { signal: options.signal, deadlineAtMs })
        seededCount += 1
      }
    } catch (error) {
      if (options.signal?.aborted) throw error
      failedCount += 1
      logger.warn(errorLogFields(error, {
        event: 'usage_record_first_page_prewarm_account_failed',
        systemAccountId,
        lane: item.lane
      }), '使用记录首屏预热单账户失败，继续处理后续候选')
    } finally {
      processedCount += 1
    }
  }

  const durationMs = Math.max(0, dependencies.nowMs() - startedAtMs)
  const outcome = failedCount > 0 || budgetExhausted ? 'partial' : 'success'
  const warningParts = [
    failedCount > 0 ? `失败 ${failedCount}` : '',
    budgetExhausted ? `超过 ${usageRecordFirstPagePrewarmMaxRunMs}ms 软预算` : ''
  ].filter(Boolean)
  const lastRotatingCandidateId = lastProcessedRotatingCandidateId(selected, processedCount)
  const summary: UsageRecordFirstPagePrewarmResult = {
    outcome,
    ...(warningParts.length ? { warning: `使用记录首屏预热部分完成：${warningParts.join('，')}` } : {}),
    candidateCount: candidates.length,
    selectedCount: selected.length,
    processedCount,
    cacheHitCount,
    seededCount,
    failedCount,
    budgetExhausted,
    durationMs,
    ...(lastRotatingCandidateId ? { nextRotatingCursorId: lastRotatingCandidateId } : {})
  }
  const fields = { event: 'usage_record_first_page_prewarm_completed', ...summary }
  if (outcome === 'partial') {
    logger.warn(fields, summary.warning)
  } else {
    logger.debug(fields, '使用记录首屏预热完成')
  }
  return summary
}

export function selectUsageRecordFirstPagePrewarmCandidates(
  candidates: UsageRecordFirstPagePrewarmCandidate[],
  rotationSlot = 0
): SelectedUsageRecordFirstPagePrewarmCandidate[] {
  const hot = candidates
    .slice(0, usageRecordFirstPagePrewarmHotCandidateCount)
    .map((candidate) => ({ candidate, lane: 'hot' as const }))
  const rotatingCandidates = candidates.slice(usageRecordFirstPagePrewarmHotCandidateCount)
  if (rotatingCandidates.length === 0) return hot

  const rotatingLimit = usageRecordFirstPagePrewarmBatchSize - hot.length
  const normalizedSlot = Number.isSafeInteger(rotationSlot) ? Math.max(0, rotationSlot) : 0
  const startIndex = ((normalizedSlot % rotatingCandidates.length) * rotatingLimit) % rotatingCandidates.length
  const rotatingCount = Math.min(rotatingLimit, rotatingCandidates.length)
  const rotating: SelectedUsageRecordFirstPagePrewarmCandidate[] = []
  for (let offset = 0; offset < rotatingCount; offset += 1) {
    rotating.push({
      candidate: rotatingCandidates[(startIndex + offset) % rotatingCandidates.length],
      lane: 'rotating'
    })
  }
  return [...hot, ...rotating]
}

function lastProcessedRotatingCandidateId(
  selected: SelectedUsageRecordFirstPagePrewarmCandidate[],
  processedCount: number
): string | undefined {
  return selected
    .slice(0, Math.max(0, processedCount))
    .filter((item) => item.lane === 'rotating')
    .at(-1)
    ?.candidate.systemAccountId
}
