export interface UpstreamBaseUrlValidationPolicy {
  allowedProtocols: readonly ('http:' | 'https:')[]
  allowHttpForPrivateHosts: boolean
  rejectQuery: boolean
  rejectHash: boolean
  rejectConsecutivePathSlashes: boolean
  rejectEncodedPathSeparators: boolean
  rejectDotSegments: boolean
  allowedPathPatterns?: readonly RegExp[]
  pathValidator?: (path: string) => string | undefined
}

export class UpstreamBaseUrlValidationError extends Error {
  constructor(message: string) {
    super(message)
  }
}

export class UpstreamBaseUrlValidator {
  constructor(readonly policy: UpstreamBaseUrlValidationPolicy = openAICompatibleBaseUrlPolicy) {}

  parse(value: string, options: { isPrivateHostAllowed?: boolean } = {}): URL {
    return parseAndValidateUpstreamUrl(value, this.policy, options)
  }
}

export const openAICompatibleBaseUrlPolicy: UpstreamBaseUrlValidationPolicy = {
  allowedProtocols: ['http:', 'https:'],
  allowHttpForPrivateHosts: true,
  rejectQuery: true,
  rejectHash: true,
  rejectConsecutivePathSlashes: true,
  rejectEncodedPathSeparators: true,
  rejectDotSegments: true,
  pathValidator: validateOpenAICompatibleBaseUrlPath
}

export const upstreamRequestUrlPolicy: UpstreamBaseUrlValidationPolicy = {
  allowedProtocols: ['http:', 'https:'],
  allowHttpForPrivateHosts: true,
  rejectQuery: false,
  rejectHash: true,
  rejectConsecutivePathSlashes: false,
  rejectEncodedPathSeparators: false,
  rejectDotSegments: true
}

export const openAICompatibleBaseUrlValidator = new UpstreamBaseUrlValidator(openAICompatibleBaseUrlPolicy)
export const upstreamRequestUrlValidator = new UpstreamBaseUrlValidator(upstreamRequestUrlPolicy)

interface ParsedRawUrlParts {
  protocol: string
  authority: string
  path: string
  query: string
  hash: string
}

export function parseAndValidateUpstreamUrl(
  value: string,
  policy: UpstreamBaseUrlValidationPolicy = openAICompatibleBaseUrlPolicy,
  options: { isPrivateHostAllowed?: boolean } = {}
): URL {
  const input = validateRawUrlString(value)
  const rawParts = parseRawAbsoluteUrlParts(input)
  assertRawUrlParts(rawParts, policy)

  let url: URL
  try {
    url = new URL(input)
  } catch {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 格式无效')
  }

  assertParsedUrl(url, rawParts, policy, Boolean(options.isPrivateHostAllowed))
  return url
}

function validateRawUrlString(value: string): string {
  if (typeof value !== 'string' || !value.trim()) {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 不能为空')
  }
  const input = value.trim()
  if (/[^\S ]|\s/.test(input)) {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 不能包含空白字符')
  }
  if (/[\u0000-\u001f\u007f]/.test(input)) {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 不能包含控制字符')
  }
  if (input.includes('\\')) {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 不能包含反斜杠')
  }
  return input
}

function parseRawAbsoluteUrlParts(input: string): ParsedRawUrlParts {
  const schemeMatch = /^([a-zA-Z][a-zA-Z0-9+.-]*):\/\//.exec(input)
  if (!schemeMatch) {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 必须是完整绝对地址，例如 https://api.openai.com/v1')
  }
  const protocol = `${schemeMatch[1].toLowerCase()}:`
  const afterScheme = input.slice(schemeMatch[0].length)
  if (afterScheme.startsWith('/')) {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 协议后只能保留两个斜杠')
  }
  const authorityEnd = firstIndexOfAny(afterScheme, ['/', '?', '#'])
  const authority = authorityEnd >= 0 ? afterScheme.slice(0, authorityEnd) : afterScheme
  const pathAndSuffix = authorityEnd >= 0 ? afterScheme.slice(authorityEnd) : ''
  if (!authority) {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 必须包含主机名')
  }
  const hashIndex = pathAndSuffix.indexOf('#')
  const beforeHash = hashIndex >= 0 ? pathAndSuffix.slice(0, hashIndex) : pathAndSuffix
  const hash = hashIndex >= 0 ? pathAndSuffix.slice(hashIndex) : ''
  const queryIndex = beforeHash.indexOf('?')
  const path = queryIndex >= 0 ? beforeHash.slice(0, queryIndex) : beforeHash
  const query = queryIndex >= 0 ? beforeHash.slice(queryIndex) : ''
  return {
    protocol,
    authority,
    path: path || '/',
    query,
    hash
  }
}

function firstIndexOfAny(value: string, needles: string[]): number {
  const indexes = needles
    .map((needle) => value.indexOf(needle))
    .filter((index) => index >= 0)
  return indexes.length ? Math.min(...indexes) : -1
}

function assertRawUrlParts(parts: ParsedRawUrlParts, policy: UpstreamBaseUrlValidationPolicy): void {
  if (parts.authority.includes('@')) {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 不能包含用户名或密码')
  }
  if (policy.rejectQuery && parts.query) {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 不能包含查询参数')
  }
  if (policy.rejectHash && parts.hash) {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 不能包含片段标识')
  }
  if (parts.path && !parts.path.startsWith('/')) {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 路径必须以 / 开头')
  }
  if (policy.rejectConsecutivePathSlashes && /\/{2,}/.test(parts.path)) {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 路径不能包含连续斜杠')
  }
  assertPathSegments(parts.path, policy)
}

function assertPathSegments(path: string, policy: UpstreamBaseUrlValidationPolicy): void {
  const segments = path.split('/').filter((segment) => segment.length > 0)
  for (const segment of segments) {
    const decodedSegment = decodePathSegment(segment)
    if (policy.rejectEncodedPathSeparators && /[\\/]/.test(decodedSegment)) {
      throw new UpstreamBaseUrlValidationError('上游 Base URL 路径不能包含编码后的斜杠')
    }
    if (policy.rejectDotSegments && (decodedSegment === '.' || decodedSegment === '..')) {
      throw new UpstreamBaseUrlValidationError('上游 Base URL 路径不能包含 . 或 .. 段')
    }
  }
}

function decodePathSegment(segment: string): string {
  try {
    return decodeURIComponent(segment)
  } catch {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 路径编码无效')
  }
}

function assertParsedUrl(
  url: URL,
  rawParts: ParsedRawUrlParts,
  policy: UpstreamBaseUrlValidationPolicy,
  isPrivateHostAllowed: boolean
): void {
  if (!policy.allowedProtocols.includes(url.protocol as 'http:' | 'https:')) {
    if (!(url.protocol === 'http:' && policy.allowHttpForPrivateHosts && isPrivateHostAllowed)) {
      throw new UpstreamBaseUrlValidationError(protocolErrorMessage(policy))
    }
  }
  if (!url.hostname) {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 必须包含主机名')
  }
  if (url.username || url.password) {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 不能包含用户名或密码')
  }
  if (policy.rejectQuery && url.search) {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 不能包含查询参数')
  }
  if (policy.rejectHash && url.hash) {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 不能包含片段标识')
  }
  if (policy.allowedPathPatterns && !policy.allowedPathPatterns.some((pattern) => pattern.test(rawParts.path))) {
    throw new UpstreamBaseUrlValidationError('上游 Base URL 只能填写服务根地址或 /v1 版本根地址')
  }
  const pathValidationMessage = policy.pathValidator?.(rawParts.path)
  if (pathValidationMessage) {
    throw new UpstreamBaseUrlValidationError(pathValidationMessage)
  }
}

function protocolErrorMessage(policy: UpstreamBaseUrlValidationPolicy): string {
  if (policy.allowedProtocols.length === 1 && policy.allowedProtocols[0] === 'https:') {
    return '上游 Base URL 只允许 https 协议'
  }
  return '上游 Base URL 只允许 http 或 https 协议'
}

function validateOpenAICompatibleBaseUrlPath(path: string): string | undefined {
  const segments = path
    .split('/')
    .filter((segment) => segment.length > 0)
    .map((segment) => decodePathSegment(segment).toLowerCase())
  if (segments.length === 0) return undefined
  const finalV1Index = segments.lastIndexOf('v1')
  if (finalV1Index >= 0 && finalV1Index < segments.length - 1) {
    const suffix = segments.slice(finalV1Index + 1)
    if (matchesOpenAIEndpointPath(suffix)) {
      return '上游 Base URL 不能包含 /v1 后的具体接口路径'
    }
  }
  if (containsOpenAIEndpointPath(segments)) {
    return '上游 Base URL 不能填写具体接口路径，请填写服务根地址或 /v1 版本根地址'
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

const openAIEndpointPathPrefixes = [
  ['models'],
  ['responses'],
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
