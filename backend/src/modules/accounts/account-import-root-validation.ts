import {
  appendUnknownFieldMessages,
  hasOwnField,
  importRootKeys,
  isRecord
} from './account-import-field-parser.js'

export interface AccountImportRootValidationOptions {
  maxAccounts: number
  maxProxies: number
  protocolType: string
  protocolVersion: number
}

export type AccountImportRootValidationResult =
  | { success: true; rawAccounts: unknown[]; rawProxies: unknown[] }
  | { success: false }

export function validateAccountImportRoot(
  data: unknown,
  messages: string[],
  options: AccountImportRootValidationOptions
): AccountImportRootValidationResult {
  if (!isRecord(data)) {
    messages.push('导入内容必须是 JSON 对象')
    return { success: false }
  }
  appendUnknownFieldMessages(data, importRootKeys, '导入内容', messages)
  if (data.type !== options.protocolType) {
    messages.push(`type 必须是 ${options.protocolType}`)
  }
  if (data.version !== options.protocolVersion) {
    messages.push(`version 必须是 ${options.protocolVersion}`)
  }
  if (messages.length > 0) {
    return { success: false }
  }

  const rawProxies = Array.isArray(data.proxies) ? data.proxies : []
  if (hasOwnField(data, 'proxies') && !Array.isArray(data.proxies)) {
    messages.push('proxies 必须是数组')
  }
  const rawAccounts = Array.isArray(data.accounts) ? data.accounts : undefined
  if (hasOwnField(data, 'accounts') && !Array.isArray(data.accounts)) {
    messages.push('accounts 必须是数组')
  }
  if (messages.length > 0) {
    return { success: false }
  }
  if (!rawAccounts || rawAccounts.length === 0) {
    messages.push('accounts 至少需要 1 条账户')
    return { success: false }
  }
  if (rawAccounts.length > options.maxAccounts) {
    messages.push(`accounts 单次最多导入 ${options.maxAccounts} 条`)
    return { success: false }
  }
  if (rawProxies.length > options.maxProxies) {
    messages.push(`proxies 单次最多导入 ${options.maxProxies} 条`)
    return { success: false }
  }

  return { success: true, rawAccounts, rawProxies }
}
