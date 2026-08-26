interface AccountBaseUrlValidationPolicy {
  allowedProtocols: readonly ('http:' | 'https:')[]
}

const defaultPolicy: AccountBaseUrlValidationPolicy = {
  allowedProtocols: ['http:', 'https:']
}

const openAIEndpointPathPrefixes = [
  ['models'],
  ['responses'],
  ['messages'],
  ['messages', 'count_tokens'],
  ['chat', 'completions'],
  ['images', 'generations'],
  ['images', 'edits'],
  ['images', 'variations'],
  ['embeddings'],
  ['audio', 'transcriptions'],
  ['audio', 'translations'],
  ['audio', 'speech'],
  ['files'],
  ['batches'],
  ['fine_tuning', 'jobs'],
  ['moderations'],
  ['vector_stores']
] as const

export function validateOpenAICompatibleBaseUrl(value: string, policy: AccountBaseUrlValidationPolicy = defaultPolicy): string | undefined {
  const input = value.trim()
  if (!input) return '请填写 Base URL'
  if (/[^\S ]|\s/.test(input) || /[\u0000-\u001f\u007f]/.test(input)) return 'Base URL 不能包含空白字符'
  if (input.includes('\\')) return 'Base URL 不能包含反斜杠'

  const parts = parseRawAbsoluteUrlParts(input)
  if (!parts.valid) return parts.message
  if (parts.authority.includes('@')) return 'Base URL 不能包含用户名或密码'
  if (parts.query) return 'Base URL 不能包含查询参数'
  if (parts.hash) return 'Base URL 不能包含片段标识'
  if (/\/{2,}/.test(parts.path)) return 'Base URL 路径不能包含连续斜杠'

  const pathMessage = validatePathSegments(parts.path)
  if (pathMessage) return pathMessage

  let url: URL
  try {
    url = new URL(input)
  } catch {
    return 'Base URL 格式无效'
  }

  if (!policy.allowedProtocols.includes(url.protocol as 'http:' | 'https:')) {
    return 'Base URL 只允许 http 或 https 协议'
  }
  if (!url.hostname) return 'Base URL 必须包含主机名'
  if (url.username || url.password) return 'Base URL 不能包含用户名或密码'

  return validateOpenAICompatiblePath(parts.path)
}

interface RawUrlParseResult {
  valid: boolean
  message?: string
  authority: string
  path: string
  query: string
  hash: string
}

function parseRawAbsoluteUrlParts(input: string): RawUrlParseResult {
  const schemeMatch = /^([a-zA-Z][a-zA-Z0-9+.-]*):\/\//.exec(input)
  if (!schemeMatch) {
    return invalidRawUrl('Base URL 必须是完整绝对地址，例如 https://api.openai.com/v1')
  }
  const afterScheme = input.slice(schemeMatch[0].length)
  if (afterScheme.startsWith('/')) {
    return invalidRawUrl('Base URL 协议后只能保留两个斜杠')
  }
  const authorityEnd = firstIndexOfAny(afterScheme, ['/', '?', '#'])
  const authority = authorityEnd >= 0 ? afterScheme.slice(0, authorityEnd) : afterScheme
  const suffix = authorityEnd >= 0 ? afterScheme.slice(authorityEnd) : ''
  if (!authority) return invalidRawUrl('Base URL 必须包含主机名')
  const hashIndex = suffix.indexOf('#')
  const beforeHash = hashIndex >= 0 ? suffix.slice(0, hashIndex) : suffix
  const hash = hashIndex >= 0 ? suffix.slice(hashIndex) : ''
  const queryIndex = beforeHash.indexOf('?')
  const path = queryIndex >= 0 ? beforeHash.slice(0, queryIndex) : beforeHash
  return {
    valid: true,
    authority,
    path: path || '/',
    query: queryIndex >= 0 ? beforeHash.slice(queryIndex) : '',
    hash
  }
}

function invalidRawUrl(message: string): RawUrlParseResult {
  return {
    valid: false,
    message,
    authority: '',
    path: '/',
    query: '',
    hash: ''
  }
}

function firstIndexOfAny(value: string, needles: string[]): number {
  const indexes = needles.map((needle) => value.indexOf(needle)).filter((index) => index >= 0)
  return indexes.length ? Math.min(...indexes) : -1
}

function validatePathSegments(path: string): string | undefined {
  const segments = path.split('/').filter(Boolean)
  for (const segment of segments) {
    let decoded: string
    try {
      decoded = decodeURIComponent(segment)
    } catch {
      return 'Base URL 路径编码无效'
    }
    if (/[\\/]/.test(decoded)) return 'Base URL 路径不能包含编码后的斜杠'
    if (decoded === '.' || decoded === '..') return 'Base URL 路径不能包含 . 或 .. 段'
  }
  return undefined
}

function validateOpenAICompatiblePath(path: string): string | undefined {
  const segments = path
    .split('/')
    .filter(Boolean)
    .map((segment) => decodeURIComponent(segment).toLowerCase())
  if (segments.length === 0) return undefined
  const finalV1Index = segments.lastIndexOf('v1')
  if (finalV1Index >= 0 && finalV1Index < segments.length - 1 && matchesOpenAIEndpointPath(segments.slice(finalV1Index + 1))) {
    return 'Base URL 不能包含 /v1 后的具体接口路径'
  }
  if (containsOpenAIEndpointPath(segments)) {
    return 'Base URL 不能填写具体接口路径，请填写服务根地址或 /v1 版本根地址'
  }
  return undefined
}

function containsOpenAIEndpointPath(segments: string[]): boolean {
  return segments.some((_segment, index) => matchesOpenAIEndpointPath(segments.slice(index)))
}

function matchesOpenAIEndpointPath(segments: string[]): boolean {
  return openAIEndpointPathPrefixes.some((prefix) => {
    if (segments.length < prefix.length) return false
    return prefix.every((part, index) => segments[index] === part)
  })
}
