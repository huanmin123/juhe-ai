import { isAdminRole, type SystemAccountListItem, type SystemAccountOptionSummary, type SystemAccountPrincipalSummary, type SystemAccountRole, type SystemAccountStatus, type SystemAccountSummary } from '../domain/types.js'
import { parseUserRequestLimitsJson } from '../domain/user-request-limits.js'

export interface SystemAccountRow {
  id: string
  username: string
  display_name: string
  description: string | null
  role: SystemAccountRole
  status: SystemAccountStatus
  password_hash: string
  must_change_password: number
  image_generation_enabled: number
  request_limits_json: string | null
  last_login_at: string | null
  created_at: string
  updated_at: string
}

export type SystemAccountSummaryRow = Omit<SystemAccountRow, 'password_hash'>

export function systemAccountSummaryFromRow(row: SystemAccountSummaryRow): SystemAccountSummary {
  return {
    id: row.id,
    username: row.username,
    displayName: row.display_name,
    description: row.description ?? undefined,
    role: row.role,
    status: row.status,
    mustChangePassword: effectiveMustChangePassword(row.role, row.must_change_password === 1),
    imageGenerationEnabled: row.image_generation_enabled === 1,
    requestLimits: parseUserRequestLimitsJson(row.request_limits_json),
    lastLoginAt: row.last_login_at ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

export function systemAccountListItemFromRow(row: Omit<SystemAccountSummaryRow, 'created_at' | 'updated_at'>): SystemAccountListItem {
  return {
    id: row.id,
    username: row.username,
    displayName: row.display_name,
    description: row.description ?? undefined,
    role: row.role,
    status: row.status,
    mustChangePassword: effectiveMustChangePassword(row.role, row.must_change_password === 1),
    imageGenerationEnabled: row.image_generation_enabled === 1,
    requestLimits: parseUserRequestLimitsJson(row.request_limits_json),
    lastLoginAt: row.last_login_at ?? undefined
  }
}

function effectiveMustChangePassword(role: SystemAccountRole, mustChangePassword: boolean): boolean {
  return mustChangePassword && !isAdminRole(role)
}

export function systemAccountPrincipalSummaryFromRow(row: Pick<SystemAccountRow, 'id' | 'username' | 'display_name' | 'status'>): SystemAccountPrincipalSummary {
  return {
    id: row.id,
    username: row.username,
    displayName: row.display_name,
    status: row.status
  }
}

export function systemAccountOptionSummaryFromRow(row: Pick<SystemAccountRow, 'id' | 'display_name' | 'status'>): SystemAccountOptionSummary {
  return {
    id: row.id,
    name: row.display_name,
    ...(row.status === 'disabled' ? { disabledReason: 'account_disabled' as const } : {})
  }
}
