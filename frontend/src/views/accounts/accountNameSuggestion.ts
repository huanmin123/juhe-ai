export function accountNameFromBaseUrl(value: string): string {
  const input = value.trim()
  if (!input) return ''

  try {
    const url = new URL(input)
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return ''
    return url.hostname
  } catch {
    return ''
  }
}

export function accountNameRandomSuffix(length = 6): string {
  const alphabet = 'abcdefghijklmnopqrstuvwxyz0123456789'
  const bytes = new Uint8Array(length)
  const cryptoObj = globalThis.crypto
  if (cryptoObj?.getRandomValues) {
    cryptoObj.getRandomValues(bytes)
  } else {
    for (let i = 0; i < length; i += 1) bytes[i] = Math.floor(Math.random() * 256)
  }
  let suffix = ''
  for (let i = 0; i < length; i += 1) suffix += alphabet[bytes[i] % alphabet.length]
  return suffix
}

// 新建表单挂载时的默认名：供应商域名 + 6 位随机后缀（同供应商多账户靠后缀区分）。
export function defaultAccountNameFromBaseUrl(baseUrl: string): string {
  const host = accountNameFromBaseUrl(baseUrl)
  return host ? `${host}-${accountNameRandomSuffix()}` : ''
}

// 自动默认名的识别形态：<合法域名（含点）>-<6 位小写随机>。用户手动命名通常不含点分域名前缀，
// 不匹配即视为已手动命名并锁定，Base URL 变化不再覆盖。
const AUTO_DEFAULT_NAME_PATTERN = /^(.+)-([a-z0-9]{6})$/

function looksLikeAutoDefaultName(name: string): boolean {
  const match = name.match(AUTO_DEFAULT_NAME_PATTERN)
  return Boolean(match && match[1].includes('.'))
}

/**
 * Base URL 变化后的名称跟随规则（返回新名称；空串表示保持不动）：
 * - 名称为空 → 生成「域名-随机后缀」默认名；
 * - 名称仍是自动默认名且域名变化 → 只替换域名部分，保留后缀（键入过程中名称稳定不闪跳）；
 * - 用户已手动命名 → 不覆盖。
 */
export function followBaseUrlAccountName(currentName: string, baseUrl: string): string {
  const host = accountNameFromBaseUrl(baseUrl)
  if (!host) return ''
  const trimmed = currentName.trim()
  if (!trimmed) return `${host}-${accountNameRandomSuffix()}`
  const match = trimmed.match(AUTO_DEFAULT_NAME_PATTERN)
  if (match && match[1].includes('.') && match[1] !== host) return `${host}-${match[2]}`
  return ''
}
