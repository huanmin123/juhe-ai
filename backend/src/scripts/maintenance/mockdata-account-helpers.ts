import type { AccountSummary, SystemAccountSummary } from '../../domain/types.js'
import { getBusinessDatabase } from '../../storage/database.js'
import * as repositories from '../../storage/repositories.js'

export function refreshAccount(id: string): AccountSummary {
  const account = repositories.findAccountSummary(id, { systemAccountId: 'sys_admin', role: 'admin' })
  if (!account) throw new Error(`读取 Mockdata 账户失败：${id}`)
  return account
}

export function authorizationInstanceAccount(sourceAccount: AccountSummary, grantee: SystemAccountSummary): AccountSummary {
  const row = getBusinessDatabase()
    .prepare(`
      SELECT id
      FROM accounts
      WHERE authorization_instance_source_account_id = ?
        AND system_account_id = ?
      ORDER BY created_at DESC, id DESC
      LIMIT 1
    `)
    .get(sourceAccount.id, grantee.id) as unknown as { id?: string } | undefined
  if (!row?.id) {
    throw new Error(`未找到 Mockdata 授权账户实例：${sourceAccount.name} -> ${grantee.displayName || grantee.username}`)
  }
  return refreshAccount(row.id)
}
