import type {
  ExternalIntegrationSourceListRow,
  ExternalIntegrationSourceSummary,
  ExternalIntegrationSourceTokenListRow,
  ExternalIntegrationSourceTokenSummary
} from './external-integration-source-types.js'
import {
  isBuiltInExternalIntegrationTestSourceId,
  isBuiltInExternalIntegrationTestTokenId
} from './external-integration-source-constants.js'
import {
  decodeRateLimits,
  decodeScopes,
  normalizeSourceStatus,
  normalizeTokenStatus
} from './external-integration-source-normalizers.js'

export function mapSourceSummary(row: ExternalIntegrationSourceListRow, tokens: ExternalIntegrationSourceTokenSummary[]): ExternalIntegrationSourceSummary {
  return {
    id: row.id,
    name: row.name,
    status: normalizeSourceStatus(row.status),
    scopes: decodeScopes(row.scopes_json),
    rateLimits: decodeRateLimits(row.rate_limits_json),
    expiresAt: row.expires_at ?? undefined,
    notes: row.notes ?? undefined,
    lastUsedAt: row.last_used_at ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    tokenCount: Number(row.token_count ?? tokens.length),
    activeTokenCount: Number(row.active_token_count ?? tokens.filter((token) => token.status === 'active').length),
    tokens,
    isBuiltIn: isBuiltInExternalIntegrationTestSourceId(row.id)
  }
}

export function mapSourceListItem(row: ExternalIntegrationSourceListRow, primaryToken?: ExternalIntegrationSourceTokenSummary): ExternalIntegrationSourceSummary {
  return {
    id: row.id,
    name: row.name,
    status: normalizeSourceStatus(row.status),
    scopes: decodeScopes(row.scopes_json),
    rateLimits: decodeRateLimits(row.rate_limits_json),
    expiresAt: row.expires_at ?? undefined,
    notes: row.notes ?? undefined,
    lastUsedAt: row.last_used_at ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    tokenCount: Number(row.token_count ?? 0),
    activeTokenCount: Number(row.active_token_count ?? 0),
    primaryToken,
    isBuiltIn: isBuiltInExternalIntegrationTestSourceId(row.id)
  }
}

export function mapTokenSummary(row: ExternalIntegrationSourceTokenListRow): ExternalIntegrationSourceTokenSummary {
  return {
    id: row.id,
    name: row.name,
    tokenPrefix: row.token_prefix,
    tokenSuffix: row.token_suffix,
    status: normalizeTokenStatus(row.status),
    scopes: decodeScopes(row.scopes_json),
    expiresAt: row.expires_at ?? undefined,
    lastUsedAt: row.last_used_at ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    revokedAt: row.revoked_at ?? undefined,
    isBuiltIn: isBuiltInExternalIntegrationTestTokenId(row.id)
  }
}
