import type { AuthorizationGranteeGroupOptionSummary, SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'
import type { AuthorizationGranteeGroupOptionsParams, AuthorizationPrincipalOptionsParams } from '../contracts'
import { http, unwrap } from '../http'
import { authorizationGranteeGroupOptionsParams, authorizationPrincipalOptionsParams } from '../params'

export const authorizationOptionsApi = {
  granteeAccounts: (params?: AuthorizationPrincipalOptionsParams) => unwrap<SystemAccountPrincipalSummary[]>(http.get('/authorization-options/grantee-accounts', { params: authorizationPrincipalOptionsParams(params) })),
  granteeTeams: (params?: AuthorizationPrincipalOptionsParams) => unwrap<SystemTeamPrincipalSummary[]>(http.get('/authorization-options/grantee-teams', { params: authorizationPrincipalOptionsParams(params) })),
  granteeGroups: (params: AuthorizationGranteeGroupOptionsParams) => unwrap<AuthorizationGranteeGroupOptionSummary[]>(http.get('/authorization-options/grantee-groups', { params: authorizationGranteeGroupOptionsParams(params) }))
}

export const myAuthorizationOptionsApi = {
  granteeAccounts: (params?: AuthorizationPrincipalOptionsParams) => unwrap<SystemAccountPrincipalSummary[]>(http.get('/my-authorization-options/grantee-accounts', { params: authorizationPrincipalOptionsParams(params) })),
  granteeTeams: (params?: AuthorizationPrincipalOptionsParams) => unwrap<SystemTeamPrincipalSummary[]>(http.get('/my-authorization-options/grantee-teams', { params: authorizationPrincipalOptionsParams(params) })),
  granteeGroups: (params: AuthorizationGranteeGroupOptionsParams) => unwrap<AuthorizationGranteeGroupOptionSummary[]>(http.get('/my-authorization-options/grantee-groups', { params: authorizationGranteeGroupOptionsParams(params) }))
}
