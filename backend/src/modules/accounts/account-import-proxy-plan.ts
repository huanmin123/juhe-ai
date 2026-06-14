import {
  appendUnknownFieldMessages,
  errorMessage,
  importProxyKeys,
  isRecord,
  normalizeProxyType,
  optionalBooleanField,
  optionalPositiveIntegerField,
  optionalTextField,
  type AccountImportProxyType
} from './account-import-field-parser.js'
import {
  canCreateImportProxy,
  findProxyOptionByName,
  type AccountImportProxyReferencePlan,
  type AccountImportResourceContext
} from './account-import-resource-resolver.js'
import type { AccountImportProxyItem } from './account-import.service.js'

export interface NormalizedImportProxy {
  index: number
  ref: string
  name: string
  type: AccountImportProxyType
  host: string
  port: number
  username?: string
  password?: string
  description?: string
  enabled: boolean
  messages: string[]
  warnings: string[]
}

export interface AccountImportProxyPlan extends AccountImportProxyReferencePlan {
  source: NormalizedImportProxy
  item: AccountImportProxyItem
}

export interface AccountImportProxyPlanResult {
  proxyPlans: AccountImportProxyPlan[]
  proxyByRef: Map<string, AccountImportProxyReferencePlan>
}

export function planImportProxies(rawProxies: unknown[], context: AccountImportResourceContext): AccountImportProxyPlanResult {
  const proxyPlans = rawProxies.map((item, index) => planImportProxy(item, index + 1, context))
  const proxyByRef = new Map<string, AccountImportProxyReferencePlan>()
  for (const proxy of proxyPlans) {
    if (proxy.source.ref && !proxyByRef.has(proxy.source.ref)) {
      proxyByRef.set(proxy.source.ref, proxy)
    } else if (proxy.source.ref) {
      proxy.item.action = 'failed'
      proxy.item.messages.push(`代理 ref 重复：${proxy.source.ref}`)
    }
  }
  return {
    proxyPlans,
    proxyByRef
  }
}

function planImportProxy(value: unknown, index: number, context: AccountImportResourceContext): AccountImportProxyPlan {
  const item: AccountImportProxyItem = { index, action: 'create', messages: [], warnings: [] }
  const source: NormalizedImportProxy = {
    index,
    ref: '',
    name: '',
    type: 'socks5h',
    host: '',
    port: 0,
    enabled: true,
    messages: item.messages,
    warnings: item.warnings
  }
  if (!isRecord(value)) {
    item.action = 'failed'
    item.messages.push('代理配置必须是对象')
    return { source, item }
  }
  appendUnknownFieldMessages(value, importProxyKeys, '代理配置', item.messages)
  source.ref = optionalTextField(value, 'ref', '代理 ref', item.messages) ?? ''
  source.name = optionalTextField(value, 'name', '代理名称', item.messages) ?? ''
  const proxyTypeInput = optionalTextField(value, 'type', '代理 type', item.messages)
  if (proxyTypeInput) {
    try {
      source.type = normalizeProxyType(proxyTypeInput)
    } catch (error) {
      item.messages.push(errorMessage(error))
    }
  }
  source.host = optionalTextField(value, 'host', '代理 host', item.messages) ?? ''
  source.port = optionalPositiveIntegerField(value, 'port', '代理 port', item.messages) ?? 0
  source.username = optionalTextField(value, 'username', '代理 username', item.messages)
  source.password = optionalTextField(value, 'password', '代理 password', item.messages)
  source.description = optionalTextField(value, 'description', '代理 description', item.messages)
  const proxyEnabled = optionalBooleanField(value, 'enabled', '代理 enabled', item.messages)
  if (proxyEnabled !== undefined) {
    source.enabled = proxyEnabled
  }
  item.ref = source.ref
  item.name = source.name

  if (!source.ref) item.messages.push('代理 ref 不能为空')
  if (!source.name) item.messages.push('代理名称不能为空')
  if (!proxyTypeInput) item.messages.push('代理 type 不能为空')
  if (!source.host) item.messages.push('代理 host 不能为空')
  if (source.port < 1 || source.port > 65535) item.messages.push('代理 port 必须是 1 到 65535 的整数')
  const existing = findProxyOptionByName(source.name, context)
  if (existing) {
    item.action = 'reuse'
    item.proxyProfileId = existing.id
  } else if (!canCreateImportProxy(context, item)) {
    item.action = item.messages.length > 0 ? 'failed' : 'skip'
  }
  if (item.messages.length > 0) {
    item.action = 'failed'
  }
  return {
    source,
    item,
    proxyProfileId: item.proxyProfileId
  }
}
