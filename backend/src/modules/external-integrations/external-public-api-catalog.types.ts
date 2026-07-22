export type ExternalPublicApiMethod = 'GET' | 'POST' | 'PATCH' | 'DELETE'
export type ExternalPublicApiStatus = 'available' | 'mock'

export interface ExternalPublicApiField {
  name: string
  type: string
  required: boolean
  description: string
  example?: unknown
}

export interface ExternalPublicApiHeader {
  name: string
  required: boolean
  description: string
  example: string
}

export interface ExternalPublicApiBody {
  contentType: string
  fields: ExternalPublicApiField[]
  example: unknown
}

export interface ExternalPublicApiDocItem {
  id: string
  name: string
  summary: string
  status: ExternalPublicApiStatus
  method: ExternalPublicApiMethod
  path: string
  scope?: string
  headers: ExternalPublicApiHeader[]
  query: ExternalPublicApiField[]
  requestBody?: ExternalPublicApiBody
  responseFields: ExternalPublicApiField[]
  responseExample: unknown
}

export interface ExternalPublicApiCatalog {
  basePath: string
  authType: 'Bearer'
  items: ExternalPublicApiDocItem[]
}

export type ExternalPublicApiDocItemSeed = Omit<ExternalPublicApiDocItem, 'scope' | 'responseFields'>

