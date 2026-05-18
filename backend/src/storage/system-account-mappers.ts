import type { SystemAccountPrincipalSummary, SystemAccountRole, SystemAccountStatus, SystemAccountSummary } from '../domain/types.js'

export interface SystemAccountRow {
  id: string
  username: string
  display_name: string
  description: string | null
  role: SystemAccountRole
  status: SystemAccountStatus
  password_hash: string
  must_change_password: number
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
    mustChangePassword: row.must_change_password === 1,
    lastLoginAt: row.last_login_at ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

export function systemAccountPrincipalSummaryFromRow(row: Pick<SystemAccountRow, 'id' | 'username' | 'display_name' | 'status'>): SystemAccountPrincipalSummary {
  return {
    id: row.id,
    username: row.username,
    displayName: row.display_name,
    status: row.status
  }
}
