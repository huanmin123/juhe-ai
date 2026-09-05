import {
  externalIntegrationAccountAddWriteScope,
  externalIntegrationAccountDeleteWriteScope,
  externalIntegrationAccountListReadScope,
  externalIntegrationAccountUpdateWriteScope,
  externalIntegrationApiKeyAddWriteScope,
  externalIntegrationApiKeyDeleteWriteScope,
  externalIntegrationApiKeyListReadScope,
  externalIntegrationApiKeyUpdateWriteScope,
  externalIntegrationGroupAddWriteScope,
  externalIntegrationGroupDeleteWriteScope,
  externalIntegrationGroupListReadScope,
  externalIntegrationGroupUpdateWriteScope,
  externalIntegrationRouteStrategyAddWriteScope,
  externalIntegrationRouteStrategyDeleteWriteScope,
  externalIntegrationRouteStrategyListReadScope,
  externalIntegrationRouteStrategyUpdateWriteScope
} from '../../storage/external-integration-source-constants.js'

export function scopeForPublicApiDocItem(id: string): string {
  const scopesById: Record<string, string> = {
    'api-key-list': externalIntegrationApiKeyListReadScope,
    'route-strategy-list': externalIntegrationRouteStrategyListReadScope,
    'group-list': externalIntegrationGroupListReadScope,
    'account-list': externalIntegrationAccountListReadScope,
    'api-key-add': externalIntegrationApiKeyAddWriteScope,
    'api-key-update': externalIntegrationApiKeyUpdateWriteScope,
    'api-key-delete': externalIntegrationApiKeyDeleteWriteScope,
    'route-strategy-add': externalIntegrationRouteStrategyAddWriteScope,
    'route-strategy-update': externalIntegrationRouteStrategyUpdateWriteScope,
    'route-strategy-delete': externalIntegrationRouteStrategyDeleteWriteScope,
    'group-add': externalIntegrationGroupAddWriteScope,
    'group-update': externalIntegrationGroupUpdateWriteScope,
    'group-delete': externalIntegrationGroupDeleteWriteScope,
    'account-add': externalIntegrationAccountAddWriteScope,
    'account-update': externalIntegrationAccountUpdateWriteScope,
    'account-delete': externalIntegrationAccountDeleteWriteScope
  }
  return scopesById[id] ?? ''
}
