import type { ResourceAuthorizationSourceSummary, ResourcePermissions } from '../domain/types.js'

export function ownerPermissions(): ResourcePermissions {
  return {
    canUse: true,
    canEdit: true,
    canDelete: true,
    canReturnAuthorization: false,
    canAuthorize: true,
    canViewCredentials: true,
    canManageAccounts: true,
    canBindToApiKey: true
  }
}

export function authorizedPermissions(): ResourcePermissions {
  return {
    canUse: true,
    canEdit: false,
    canDelete: false,
    canReturnAuthorization: false,
    canAuthorize: false,
    canViewCredentials: false,
    canManageAccounts: false,
    canBindToApiKey: false
  }
}

export function authorizedGroupPermissions(canBindToApiKey: boolean, canReturnAuthorization = false): ResourcePermissions {
  return {
    ...authorizedPermissions(),
    canEdit: true,
    canReturnAuthorization,
    canBindToApiKey
  }
}

export function authorizedAccountPermissions(canReturnAuthorization = false): ResourcePermissions {
  return {
    ...authorizedPermissions(),
    canReturnAuthorization
  }
}

export function hasActiveManualAuthorizationSource(sources?: ResourceAuthorizationSourceSummary[]): boolean {
  return sources?.some((source) => source.sourceType === 'manual' && source.status === 'active') ?? false
}
