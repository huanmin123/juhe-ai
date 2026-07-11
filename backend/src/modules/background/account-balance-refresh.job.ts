import { errorLogFields, logger } from '../../shared/logger.js'
import { listAccountsDueForBalanceRefreshAsync } from '../../storage/account-balance.repository.js'
import { refreshAccountBalanceCandidate } from '../accounts/account-balance-query.service.js'

const refreshBatchSize = 100
const refreshConcurrency = 4

export async function runAccountBalanceRefresh(): Promise<void> {
  const candidates = await listAccountsDueForBalanceRefreshAsync({ limit: refreshBatchSize })
  let cursor = 0
  await Promise.all(Array.from({ length: Math.min(refreshConcurrency, candidates.length) }, async () => {
    while (cursor < candidates.length) {
      const candidate = candidates[cursor]
      cursor += 1
      try {
        await refreshAccountBalanceCandidate(candidate)
      } catch (error) {
        logger.warn(errorLogFields(error, {
          event: 'account_balance_refresh_failed',
          accountId: candidate.id,
          systemAccountId: candidate.systemAccountId
        }), 'AI 账户上游余额刷新失败')
      }
    }
  }))
}
