import {
  UpstreamRequestAbortedError,
  UpstreamRequestTimeoutError,
  type GatewayUpstreamResponse
} from '../../../gateway/upstream/request.js'

const grokCliProxyHost = 'cli-chat-proxy.grok.com'
const grokOfficialApiHost = 'api.x.ai'
const grokFallbackBodyLimit = 64 << 10
const grokFallbackBodyInspectionTimeoutMs = 5_000
const grokCliOnlyHeaders = [
  'x-xai-token-auth',
  'x-grok-client-version',
  'x-grok-client-surface',
  'x-userid',
  'x-email',
  'user-agent'
] as const

export interface GrokAccessDeniedFallbackInput {
  upstreamUrl: string
  headers: Headers
  body?: Buffer | string
  response: GatewayUpstreamResponse
  signal?: AbortSignal
  bodyInspectionTimeoutMs?: number
  requestFallback(url: string, headers: Headers): Promise<GatewayUpstreamResponse>
}

export interface GrokAccessDeniedFallbackResult {
  response: GatewayUpstreamResponse
  usedFallback: boolean
}

export async function applyGrokAccessDeniedFallback(
  input: GrokAccessDeniedFallbackInput
): Promise<GrokAccessDeniedFallbackResult> {
  if (!isGrokAccessDeniedFallbackCandidate(input)) {
    return { response: input.response, usedFallback: false }
  }

  const inspected = await inspectSmallBody(
    input.response.body,
    grokFallbackBodyLimit,
    input.signal,
    positiveTimeout(input.bodyInspectionTimeoutMs)
  )
  const originalResponse: GatewayUpstreamResponse = {
    status: input.response.status,
    ok: input.response.ok,
    headers: input.response.headers,
    body: inspected.replayBody,
  }
  if (!inspected.complete || !inspected.bodyText.toLowerCase().includes('access denied')) {
    return { response: originalResponse, usedFallback: false }
  }

  const fallbackUrl = new URL(input.upstreamUrl)
  fallbackUrl.protocol = 'https:'
  fallbackUrl.hostname = grokOfficialApiHost
  fallbackUrl.port = ''
  const fallbackHeaders = new Headers(input.headers)
  for (const header of grokCliOnlyHeaders) fallbackHeaders.delete(header)

  let fallbackResponse: GatewayUpstreamResponse
  try {
    fallbackResponse = await input.requestFallback(fallbackUrl.toString(), fallbackHeaders)
  } catch {
    return { response: originalResponse, usedFallback: false }
  }
  if (!fallbackResponse.ok) {
    await closeBody(fallbackResponse.body)
    return { response: originalResponse, usedFallback: false }
  }
  return { response: fallbackResponse, usedFallback: true }
}

function isGrokAccessDeniedFallbackCandidate(input: GrokAccessDeniedFallbackInput): boolean {
  if (input.response.status !== 403 || input.body === undefined) return false
  if (input.headers.get('x-xai-token-auth')?.trim().toLowerCase() !== 'xai-grok-cli') return false
  if (!input.headers.get('authorization')?.trim().toLowerCase().startsWith('bearer ')) return false
  try {
    return new URL(input.upstreamUrl).hostname.toLowerCase() === grokCliProxyHost
  } catch {
    return false
  }
}

async function inspectSmallBody(
  body: AsyncIterable<Uint8Array> | null,
  limit: number,
  signal: AbortSignal | undefined,
  timeoutMs: number
): Promise<{ bodyText: string; complete: boolean; replayBody: AsyncIterable<Uint8Array> | null }> {
  if (!body) return { bodyText: '', complete: true, replayBody: null }
  const iterator = body[Symbol.asyncIterator]()
  const prefix: Buffer[] = []
  let bytes = 0
  let complete = false
  const deadlineAtMs = Date.now() + timeoutMs
  while (bytes <= limit) {
    const next = await nextBodyChunk(iterator, signal, deadlineAtMs)
    if (next.done) {
      complete = true
      break
    }
    const chunk = Buffer.from(next.value.buffer, next.value.byteOffset, next.value.byteLength)
    prefix.push(chunk)
    bytes += chunk.byteLength
    if (bytes > limit) break
  }
  return {
    bodyText: complete ? Buffer.concat(prefix, bytes).toString('utf8') : '',
    complete,
    replayBody: replayBody(prefix, complete ? undefined : iterator)
  }
}

async function nextBodyChunk(
  iterator: AsyncIterator<Uint8Array>,
  signal: AbortSignal | undefined,
  deadlineAtMs: number
): Promise<IteratorResult<Uint8Array>> {
  if (signal?.aborted) throw new UpstreamRequestAbortedError('请求已取消', true)
  const remainingMs = deadlineAtMs - Date.now()
  if (remainingMs <= 0) {
    void Promise.resolve(iterator.return?.()).catch(() => undefined)
    throw new UpstreamRequestTimeoutError('Grok CLI 403 响应体检查超时')
  }
  let timer: NodeJS.Timeout | undefined
  let abortListener: (() => void) | undefined
  try {
    return await Promise.race([
      iterator.next(),
      new Promise<never>((_resolve, reject) => {
        timer = setTimeout(() => reject(new UpstreamRequestTimeoutError('Grok CLI 403 响应体检查超时')), remainingMs)
        if (signal) {
          abortListener = () => reject(new UpstreamRequestAbortedError('请求已取消', true))
          signal.addEventListener('abort', abortListener, { once: true })
        }
      })
    ])
  } catch (error) {
    void Promise.resolve(iterator.return?.()).catch(() => undefined)
    throw error
  } finally {
    if (timer) clearTimeout(timer)
    if (signal && abortListener) signal.removeEventListener('abort', abortListener)
  }
}

async function* replayBody(
  prefix: readonly Buffer[],
  remainder?: AsyncIterator<Uint8Array>
): AsyncIterable<Uint8Array> {
  for (const chunk of prefix) yield chunk
  if (!remainder) return
  while (true) {
    const next = await remainder.next()
    if (next.done) return
    yield next.value
  }
}

async function closeBody(body: AsyncIterable<Uint8Array> | null): Promise<void> {
  if (!body) return
  const iterator = body[Symbol.asyncIterator]()
  await iterator.return?.()
}

function positiveTimeout(value: number | undefined): number {
  return Number.isFinite(value) && value! > 0
    ? Math.trunc(value!)
    : grokFallbackBodyInspectionTimeoutMs
}
