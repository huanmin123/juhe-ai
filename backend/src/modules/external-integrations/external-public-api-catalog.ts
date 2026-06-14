import type {
  ExternalPublicApiCatalog,
  ExternalPublicApiDocItem
} from './external-public-api-catalog.types.js'
import { externalPublicApiDocItems } from './external-public-api-catalog.items.js'
import { responseFieldsForPublicApiDocItem } from './external-public-api-response-fields.js'
import { scopeForPublicApiDocItem } from './external-public-api-scopes.js'

export type {
  ExternalPublicApiBody,
  ExternalPublicApiCatalog,
  ExternalPublicApiDocItem,
  ExternalPublicApiDocItemSeed,
  ExternalPublicApiField,
  ExternalPublicApiHeader,
  ExternalPublicApiMethod,
  ExternalPublicApiStatus
} from './external-public-api-catalog.types.js'

export function getExternalPublicApiCatalog(): ExternalPublicApiCatalog {
  return {
    basePath: '/__aipublic__',
    authType: 'Bearer',
    items: externalPublicApiDocItems.map((item): ExternalPublicApiDocItem => ({
      ...item,
      responseFields: responseFieldsForPublicApiDocItem(item.id),
      scope: scopeForPublicApiDocItem(item.id)
    }))
  }
}
