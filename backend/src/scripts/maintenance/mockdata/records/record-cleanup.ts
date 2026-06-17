import { getDatasetDatabase, nowIso } from '../../../../storage/database.js'
import {
  dayMs,
  idPrefix,
  minuteMs,
  namePrefix,
  type RecordCleanupMockdataCounts
} from '../shared.js'

export function createRecordCleanupMockdata(): RecordCleanupMockdataCounts {
  const database = getDatasetDatabase()
  const now = nowIso()
  const accountTargets = [
    {
      accountId: `${idPrefix}display_deleted_account_01`,
      relatedAccountIds: [`${idPrefix}display_related_account_01`],
      authorizationIds: [`${idPrefix}display_authorization_01`],
      teamScopeIds: [`${idPrefix}display_team_scope_01`],
      attemptCount: 2,
      lastBlockedReason: `${namePrefix}等待 usage shard 短事务空闲`,
      lastErrorMessage: 'Mockdata 模拟账号相关记录清理遇到 SQLite 写锁'
    },
    {
      accountId: `${idPrefix}display_deleted_account_02`,
      relatedAccountIds: [],
      authorizationIds: [`${idPrefix}display_authorization_02`, `${idPrefix}display_authorization_03`],
      teamScopeIds: [`${idPrefix}display_team_scope_02`],
      attemptCount: 1,
      lastBlockedReason: `${namePrefix}等待授权窗口扣减完成`,
      lastErrorMessage: null
    },
    {
      accountId: `${idPrefix}display_deleted_account_03`,
      relatedAccountIds: [`${idPrefix}display_related_account_03`],
      authorizationIds: [],
      teamScopeIds: [],
      attemptCount: 0,
      lastBlockedReason: `${namePrefix}待后台维护任务处理`,
      lastErrorMessage: null
    }
  ]
  const apiKeyTargets = [
    { apiKeyId: `${idPrefix}display_deleted_api_key_01`, attemptCount: 2, reason: `${namePrefix}等待统计扣减重试`, error: 'Mockdata 模拟 API Key 清理锁竞争' },
    { apiKeyId: `${idPrefix}display_deleted_api_key_02`, attemptCount: 1, reason: `${namePrefix}等待数据集索引清理`, error: null },
    { apiKeyId: `${idPrefix}display_deleted_api_key_03`, attemptCount: 0, reason: `${namePrefix}待后台维护任务处理`, error: null }
  ]
  const insertAccount = database.prepare(`
    INSERT INTO account_record_cleanup_targets (
      account_id, system_account_id, related_account_ids_json, authorization_ids_json, team_scope_ids_json,
      created_at, updated_at, attempt_count, last_attempt_at, last_blocked_reason, last_error_message
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const insertApiKey = database.prepare(`
    INSERT INTO api_key_record_cleanup_targets (
      api_key_id, system_account_id, created_at, updated_at, attempt_count,
      last_attempt_at, last_blocked_reason, last_error_message
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
  `)
  database.exec('BEGIN')
  try {
    accountTargets.forEach((target, index) => {
      const createdAt = new Date(Date.now() - (index + 1) * dayMs).toISOString()
      insertAccount.run(
        target.accountId,
        '',
        JSON.stringify(target.relatedAccountIds),
        JSON.stringify(target.authorizationIds),
        JSON.stringify(target.teamScopeIds),
        createdAt,
        now,
        target.attemptCount,
        target.attemptCount > 0 ? new Date(Date.now() - (index + 2) * 60 * minuteMs).toISOString() : null,
        target.lastBlockedReason,
        target.lastErrorMessage
      )
    })
    apiKeyTargets.forEach((target, index) => {
      const createdAt = new Date(Date.now() - (index + 1) * dayMs).toISOString()
      insertApiKey.run(
        target.apiKeyId,
        '',
        createdAt,
        now,
        target.attemptCount,
        target.attemptCount > 0 ? new Date(Date.now() - (index + 3) * 60 * minuteMs).toISOString() : null,
        target.reason,
        target.error
      )
    })
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
  return {
    accountTargets: accountTargets.length,
    apiKeyTargets: apiKeyTargets.length
  }
}
