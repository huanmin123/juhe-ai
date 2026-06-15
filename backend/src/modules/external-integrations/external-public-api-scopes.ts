import {
  externalIntegrationAccessInfoReadScope,
  externalIntegrationAccountAddWriteScope,
  externalIntegrationAccountDeleteWriteScope,
  externalIntegrationAccountListReadScope,
  externalIntegrationAccountUpdateWriteScope,
  externalIntegrationAccountUsageReadScope,
  externalIntegrationApiKeyAddWriteScope,
  externalIntegrationApiKeyDeleteWriteScope,
  externalIntegrationApiKeyListReadScope,
  externalIntegrationApiKeyUpdateWriteScope,
  externalIntegrationConsumptionRankingReadScope,
  externalIntegrationGroupAddWriteScope,
  externalIntegrationGroupDeleteWriteScope,
  externalIntegrationGroupListReadScope,
  externalIntegrationGroupUpdateWriteScope,
  externalIntegrationIpUsageReadScope,
  externalIntegrationSourceAuthDemoScope
} from '../../storage/external-integration-source-constants.js'

export function scopeForPublicApiDocItem(id: string): string {
  const scopesById: Record<string, string> = {
    'source-auth-demo': externalIntegrationSourceAuthDemoScope,
    'ip-usage': externalIntegrationIpUsageReadScope,
    'consumption-ranking': externalIntegrationConsumptionRankingReadScope,
    'account-usage': externalIntegrationAccountUsageReadScope,
    'access-info': externalIntegrationAccessInfoReadScope,
    'group-list': externalIntegrationGroupListReadScope,
    'api-key-list': externalIntegrationApiKeyListReadScope,
    'account-list': externalIntegrationAccountListReadScope,
    'group-add': externalIntegrationGroupAddWriteScope,
    'group-update': externalIntegrationGroupUpdateWriteScope,
    'group-delete': externalIntegrationGroupDeleteWriteScope,
    'api-key-add': externalIntegrationApiKeyAddWriteScope,
    'api-key-update': externalIntegrationApiKeyUpdateWriteScope,
    'api-key-delete': externalIntegrationApiKeyDeleteWriteScope,
    'account-add': externalIntegrationAccountAddWriteScope,
    'account-update': externalIntegrationAccountUpdateWriteScope,
    'account-delete': externalIntegrationAccountDeleteWriteScope
  }
  return scopesById[id] ?? ''
}
