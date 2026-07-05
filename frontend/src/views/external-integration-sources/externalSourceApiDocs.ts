import type {
  ExternalPublicApiDocItem,
  ExternalPublicApiField,
  ExternalPublicApiStatus
} from '@/types/domain'

export type CurlCommandPlatform = 'windows' | 'posix'

export function resolvePublicApiBaseUrl(): string {
  return normalizePublicApiBaseUrl(
    (import.meta.env.VITE_JUHE_AI_GATEWAY_BASE_URL as string | undefined)
      || (import.meta.env.DEV ? import.meta.env.VITE_JUHE_AI_BACKEND_TARGET as string | undefined : undefined)
  )
}

export function buildCurl(
  item: ExternalPublicApiDocItem | undefined,
  publicApiBaseUrl: string,
  platform: CurlCommandPlatform
): string {
  if (!item) return ''
  const url = buildApiDocUrl(item, publicApiBaseUrl)
  const parts = [
    platform === 'windows' ? 'curl.exe' : 'curl',
    '-X',
    item.method,
    quoteShell(url, platform),
    '-H',
    quoteShell('Authorization: Bearer <source_token>', platform)
  ]
  if (item.requestBody) {
    parts.push('-H', quoteShell(`Content-Type: ${item.requestBody.contentType}`, platform))
    parts.push('--data', quoteShell(JSON.stringify(item.requestBody.example), platform))
  }
  return parts.join(' ')
}

export function buildApiMarkdown(
  item: ExternalPublicApiDocItem,
  publicApiBaseUrl: string,
  platform: CurlCommandPlatform
): string {
  const lines = [
    `# ${item.name}`,
    '',
    item.summary,
    '',
    '## 基本信息',
    '',
    `- 状态：${apiStatusText(item.status)}`,
    `- 方法：\`${item.method}\``,
    `- 路径：\`${item.path}\``,
    `- 接口资源授权：\`${item.scope || '-'}\``,
    `- 调用地址：\`${buildApiDocUrl(item, publicApiBaseUrl)}\``,
    '- 认证方式：`Authorization: Bearer <source_token>`',
    '',
    '## 请求头',
    '',
    markdownFieldTable(item.headers.map((header) => ({
      name: header.name,
      type: '-',
      required: header.required,
      description: header.description,
      example: header.example
    }))),
    '',
    '## 请求参数',
    '',
    item.query.length ? markdownFieldTable(item.query) : '无',
    '',
    '## 请求体',
    ''
  ]
  if (item.requestBody) {
    lines.push(
      `Content-Type：\`${item.requestBody.contentType}\``,
      '',
      '### 请求体字段',
      '',
      item.requestBody.fields.length ? markdownFieldTable(item.requestBody.fields) : '无字段说明',
      '',
      '### 请求体示例',
      '',
      markdownCodeBlock('json', formatJson(item.requestBody.example)),
      ''
    )
  } else {
    lines.push('无', '')
  }
  lines.push(
    '## 响应字段',
    '',
    item.responseFields.length ? markdownFieldTable(item.responseFields) : '无',
    '',
    '## 响应示例',
    '',
    markdownCodeBlock('json', formatResponseExample(item)),
    '',
    '## curl 示例',
    '',
    markdownCodeBlock(platform === 'windows' ? 'powershell' : 'bash', buildCurl(item, publicApiBaseUrl, platform)),
    ''
  )
  return lines.join('\n')
}

export function buildApiDocUrl(item: ExternalPublicApiDocItem, publicApiBaseUrl: string): string {
  const url = new URL(item.path, `${publicApiBaseUrl}/`)
  for (const field of item.query) {
    if (field.example !== undefined) {
      url.searchParams.set(field.name, String(field.example))
    }
  }
  return url.toString()
}

export function formatFieldExample(value: unknown): string {
  if (value === undefined || value === '') {
    return '-'
  }
  if (value === null) {
    return 'null'
  }
  if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') {
    return String(value)
  }
  return formatJson(value)
}

export function formatJson(value: unknown): string {
  return JSON.stringify(value, null, 2)
}

export function formatResponseExample(item: ExternalPublicApiDocItem | undefined): string {
  return formatJson(publicResponseExample(item))
}

export function apiStatusText(status: ExternalPublicApiStatus): string {
  return status === 'mock' ? 'Mock 数据' : '可用'
}

export function apiStatusColor(status: ExternalPublicApiStatus): string {
  return status === 'mock' ? 'orange' : 'green'
}

export function apiMarkdownFilename(item: ExternalPublicApiDocItem): string {
  const base = `${item.method}-${item.path.replace(/^\/+/, '').replace(/[/?#&=]+/g, '-')}`
  const safeBase = base.replace(/[<>:"\\|*]+/g, '-').replace(/-+/g, '-').replace(/^-|-$/g, '')
  return `${safeBase || item.id}.md`
}

export function downloadTextFile(filename: string, content: string): void {
  const blob = new Blob([content], { type: 'text/markdown;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
  URL.revokeObjectURL(url)
}

export function detectCurlCommandPlatform(): CurlCommandPlatform {
  if (typeof navigator === 'undefined') return 'posix'
  const userAgentData = (navigator as Navigator & { userAgentData?: { platform?: string } }).userAgentData
  const platform = [
    userAgentData?.platform,
    navigator.platform,
    navigator.userAgent
  ].filter(Boolean).join(' ').toLowerCase()
  return platform.includes('win') ? 'windows' : 'posix'
}

function normalizePublicApiBaseUrl(value?: string): string {
  const text = value?.trim().replace(/\/+$/, '')
  if (text && /^https?:\/\//i.test(text)) return text
  return inferPublicApiBaseUrl()
}

function inferPublicApiBaseUrl(): string {
  if (typeof window === 'undefined') return 'http://127.0.0.1:3000'
  if (import.meta.env.DEV) {
    return `${window.location.protocol}//${window.location.hostname}:3000`
  }
  return window.location.origin
}

function markdownFieldTable(fields: Array<ExternalPublicApiField | {
  name: string
  type: string
  required: boolean
  description: string
  example?: unknown
}>): string {
  const rows = [
    '| 名称 | 类型 | 必填 | 说明 | 示例 |',
    '| --- | --- | --- | --- | --- |'
  ]
  for (const field of fields) {
    rows.push([
      markdownTableCell(field.name),
      markdownTableCell(field.type),
      field.required ? '是' : '否',
      markdownTableCell(field.description),
      markdownTableCell(field.example === undefined ? '-' : field.example)
    ].join(' | ').replace(/^/, '| ').replace(/$/, ' |'))
  }
  return rows.join('\n')
}

function markdownTableCell(value: unknown): string {
  return formatFieldExample(value).replace(/\|/g, '\\|').replace(/\r?\n/g, '<br>')
}

function markdownCodeBlock(language: string, content: string): string {
  const fence = content.includes('```') ? '````' : '```'
  return `${fence}${language}\n${content}\n${fence}`
}

function quoteShell(value: string, platform: CurlCommandPlatform): string {
  if (platform === 'windows') {
    return `"${value.replace(/"/g, '""')}"`
  }
  return `'${value.replace(/'/g, "'\\''")}'`
}

function publicResponseExample(item: ExternalPublicApiDocItem | undefined): unknown {
  if (!item) return {}
  return item.responseExample
}
