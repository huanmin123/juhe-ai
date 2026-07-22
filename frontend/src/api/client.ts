import { accountsApi, myAccountsApi } from './domains/accounts'
import { announcementsApi } from './domains/announcements'
import { apiKeysApi, myApiKeysApi } from './domains/apiKeys'
import { authApi } from './domains/auth'
import { authorizationsApi, myAuthorizationsApi } from './domains/authorizations'
import { authorizationOptionsApi, myAuthorizationOptionsApi } from './domains/authorizationOptions'
import { externalIntegrationSourcesApi } from './domains/externalIntegrationSources'
import { groupsApi, myGroupsApi } from './domains/groups'
import { ipStatsApi } from './domains/ipStats'
import { auditLogsApi, myOperationLogsApi, operationLogsApi, publicApiLogsApi, runtimeLogsApi } from './domains/logs'
import { modelChecksApi, myModelChecksApi } from './domains/modelChecks'
import { myOpenaiOAuthApi, openaiOAuthApi } from './domains/openaiOAuth'
import { providersApi } from './domains/providers'
import { proxiesApi } from './domains/proxies'
import { responseInspectionPoliciesApi } from './domains/responseInspectionPolicies'
import { myRouteStrategiesApi, routeStrategiesApi } from './domains/routeStrategies'
import { settingsApi } from './domains/settings'
import { myStatsApi, statsApi, tableMonitorApi } from './domains/stats'
import { systemAccountsApi } from './domains/systemAccounts'
import { myTeamsApi, systemTeamsApi } from './domains/systemTeams'
import { chatApi } from './domains/chat'
import { myUsageRecordsApi, usageRecordsApi } from './domains/usageRecords'

export { setMustChangePasswordHandler, setUnauthorizedHandler } from './http'
export { apiUrl } from './http'
export type * from './contracts'
export const api = {
  auth: authApi,
  systemAccounts: systemAccountsApi,
  authorizationOptions: authorizationOptionsApi,
  announcements: announcementsApi,
  myAuthorizationOptions: myAuthorizationOptionsApi,
  providers: providersApi,
  responseInspectionPolicies: responseInspectionPoliciesApi,
  accounts: accountsApi,
  myAccounts: myAccountsApi,
  groups: groupsApi,
  myGroups: myGroupsApi,
  systemTeams: systemTeamsApi,
  myTeams: myTeamsApi,
  authorizations: authorizationsApi,
  myAuthorizations: myAuthorizationsApi,
  apiKeys: apiKeysApi,
  myApiKeys: myApiKeysApi,
  routeStrategies: routeStrategiesApi,
  myRouteStrategies: myRouteStrategiesApi,
  openaiOAuth: openaiOAuthApi,
  myOpenaiOAuth: myOpenaiOAuthApi,
  proxies: proxiesApi,
  usageRecords: usageRecordsApi,
  myUsageRecords: myUsageRecordsApi,
  auditLogs: auditLogsApi,
  runtimeLogs: runtimeLogsApi,
  operationLogs: operationLogsApi,
  publicApiLogs: publicApiLogsApi,
  myOperationLogs: myOperationLogsApi,
  stats: statsApi,
  tableMonitor: tableMonitorApi,
  ipStats: ipStatsApi,
  externalIntegrationSources: externalIntegrationSourcesApi,
  modelChecks: modelChecksApi,
  myModelChecks: myModelChecksApi,
  myStats: myStatsApi,
  settings: settingsApi,
  chat: chatApi
}
