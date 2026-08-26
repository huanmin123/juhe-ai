import { strict as assert } from 'node:assert'
import { writeFileSync } from 'node:fs'

interface ModelProbeResult {
  contentSnippet?: string
  durationMs: number
  error?: string
  model: string
  ok: boolean
  responseModel?: string
  status: number
}

const realApiKey = requiredEnv('JUHE_REAL_HYBRID_API_KEY', ['JUHE_REAL_HYBRID_QUALITY_API_KEY', 'HYBRID_REAL_API_KEY'])
const realBaseUrl = envText('JUHE_REAL_HYBRID_BASE_URL', ['JUHE_REAL_HYBRID_QUALITY_BASE_URL', 'HYBRID_REAL_BASE_URL']) || 'https://vsllm.com'
const requestTimeoutMs = positiveIntegerEnv('JUHE_REAL_HYBRID_MODEL_PROBE_TIMEOUT_MS') ?? 120_000
const requestIntervalMs = positiveIntegerEnv('JUHE_REAL_HYBRID_MODEL_PROBE_INTERVAL_MS') ?? 3_500
const outputPath = envText('JUHE_REAL_HYBRID_MODEL_PROBE_OUTPUT_PATH')
const candidates = candidateModels()

const results: ModelProbeResult[] = []
for (const model of candidates) {
  if (results.length && requestIntervalMs > 0) {
    await wait(requestIntervalMs)
  }
  results.push(await probeModel(model))
}

const availableModels = results.filter((item) => item.ok).map((item) => item.model)
const summary = {
  ok: availableModels.length > 0,
  baseUrl: sanitizeBaseUrl(realBaseUrl),
  candidateCount: candidates.length,
  availableModels,
  unavailableModels: results.filter((item) => !item.ok).map((item) => ({
    model: item.model,
    status: item.status,
    error: item.error
  })),
  results
}

if (outputPath) {
  writeFileSync(outputPath, `${JSON.stringify(summary, null, 2)}\n`, 'utf8')
}
console.log(JSON.stringify(summary, null, 2))
assert(availableModels.length > 0, '真实模型探测没有任何可用模型')

async function probeModel(model: string): Promise<ModelProbeResult> {
  const startedAt = Date.now()
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), requestTimeoutMs)
  timer.unref()
  try {
    const response = await fetch(chatCompletionsUrl(realBaseUrl), {
      method: 'POST',
      headers: {
        authorization: `Bearer ${realApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model,
        messages: [
          {
            role: 'system',
            content: '你是模型可用性探针。只输出一行 JSON，不要 Markdown。'
          },
          {
            role: 'user',
            content: `请返回 {"ok":true,"model":"${model}"}`
          }
        ],
        stream: false,
        max_tokens: 80,
        temperature: 0
      }),
      signal: controller.signal
    })
    const text = await response.text()
    const body = safeJsonObject(text)
    const content = firstAssistantContent(body)
    return {
      contentSnippet: content ? content.slice(0, 200) : undefined,
      durationMs: Date.now() - startedAt,
      error: response.ok && content ? undefined : sanitizeErrorSnippet(text),
      model,
      ok: response.ok && Boolean(content),
      responseModel: typeof body.model === 'string' ? body.model : undefined,
      status: response.status
    }
  } catch (error) {
    return {
      durationMs: Date.now() - startedAt,
      error: sanitizeErrorSnippet(error instanceof Error ? error.message : String(error)),
      model,
      ok: false,
      status: 0
    }
  } finally {
    clearTimeout(timer)
  }
}

function candidateModels(): string[] {
  const configured = envText('JUHE_REAL_HYBRID_MODEL_PROBE_MODELS')
  const values = configured
    ? configured.split(',').map((item) => item.trim()).filter(Boolean)
    : [
      'gpt-5.4-mini',
      'deepseek-ai-v4-flash',
      'glm-5.1',
      'glm-5.2',
      'gpt-5.4',
      'gpt-5.5',
      'claude-opus-4-6',
      'claude-opus-4-7',
      'claude-opus-4-8'
    ]
  return [...new Set(values)]
}

function chatCompletionsUrl(baseUrl: string): string {
  const url = new URL(baseUrl)
  const normalizedPath = url.pathname.replace(/\/+$/, '')
  url.pathname = `${normalizedPath.endsWith('/v1') ? normalizedPath : `${normalizedPath}/v1`}/chat/completions`
  return url.toString()
}

function safeJsonObject(text: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(text) as unknown
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
      ? parsed as Record<string, unknown>
      : {}
  } catch {
    return {}
  }
}

function firstAssistantContent(body: Record<string, unknown>): string | undefined {
  const choices = Array.isArray(body.choices) ? body.choices : []
  const first = choices[0] as { message?: { content?: unknown, reasoning_content?: unknown } } | undefined
  if (typeof first?.message?.content === 'string' && first.message.content.trim()) {
    return first.message.content
  }
  if (typeof first?.message?.reasoning_content === 'string' && first.message.reasoning_content.trim()) {
    return first.message.reasoning_content
  }
  return choices.length > 0 ? '[non-empty-choice]' : undefined
}

function requiredEnv(name: string, aliases: string[] = []): string {
  const value = envText(name, aliases)
  if (!value) {
    throw new Error(`缺少环境变量 ${name}`)
  }
  return value
}

function envText(name: string, aliases: string[] = []): string | undefined {
  for (const key of [name, ...aliases]) {
    const value = process.env[key]?.trim()
    if (value) return value
  }
  return undefined
}

function positiveIntegerEnv(name: string): number | undefined {
  const value = envText(name)
  if (!value) return undefined
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed > 0 ? parsed : undefined
}

function sanitizeErrorSnippet(value: string): string {
  return value.replaceAll(realApiKey, '[redacted-real-api-key]').slice(0, 800) || 'empty response'
}

function sanitizeBaseUrl(value: string): string {
  try {
    const url = new URL(value)
    url.username = ''
    url.password = ''
    return url.toString().replace(/\/$/, '')
  } catch {
    return value.replaceAll(realApiKey, '[redacted-real-api-key]')
  }
}

function wait(ms: number): Promise<void> {
  if (ms <= 0) return Promise.resolve()
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}
