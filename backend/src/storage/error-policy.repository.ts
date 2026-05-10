import type { ErrorPolicySummary } from '../domain/types.js'
import { buildSystemAccountWhereClause, type AccessScope } from './access-scope.js'
import { getDatabase } from './database.js'
import { parseJsonRules } from './value-utils.js'

interface ErrorPolicyRow {
  id: string
  system_account_id: string
  name: string
  enabled: number
  rules_json: string
}

export function listErrorPolicies(access?: AccessScope): ErrorPolicySummary[] {
  const scope = buildSystemAccountWhereClause(access)
  const rows = getDatabase().prepare(`SELECT id, system_account_id, name, enabled, rules_json FROM error_policies${scope.clause} ORDER BY name ASC, id ASC`).all(...scope.params) as unknown as ErrorPolicyRow[]
  return rows.map((row) => ({
    id: row.id,
    name: row.name,
    enabled: row.enabled === 1,
    rules: parseJsonRules(row.rules_json)
  }))
}
