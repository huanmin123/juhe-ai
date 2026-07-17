import type { SafeChatStreamFailure } from './chat-long-session-failure.js'
import type { ChatLongSessionStreamProgressSnapshot } from './chat-long-session-stream-progress.js'

export interface ChatLongSessionAttemptResult {
  status: number
  terminalEvent: string | null
  turnId?: string
  firstDeltaMs: number | null
  totalMs: number
  eventCount: number
  failure?: SafeChatStreamFailure
  progress?: ChatLongSessionStreamProgressSnapshot
}

export interface ChatLongSessionAttemptMetric {
  attempt: number
  delayMs: number
  replacement: boolean
  status: number
  terminalEvent: string | null
  firstDeltaMs: number | null
  totalMs: number
  eventCount: number
  failure?: {
    type: SafeChatStreamFailure['type']
    code: string
    classification: 'transient' | 'deterministic'
  }
  progress?: ChatLongSessionStreamProgressSnapshot
}

export type ChatLongSessionTurnAttemptOutcome<T extends ChatLongSessionAttemptResult> =
  | { status: 'completed'; result: T; attempts: ChatLongSessionAttemptMetric[] }
  | { status: 'failed'; result: T; attempts: ChatLongSessionAttemptMetric[]; reason: 'deterministic_failure' | 'retry_exhausted' | 'missing_turn_id' }

export async function runChatLongSessionTurnAttempts<T extends ChatLongSessionAttemptResult>(input: {
  maxAttempts: number
  sleep: (delayMs: number) => Promise<void>
  submit: (attempt: { attempt: number; replaceTurnId?: string }) => Promise<T>
  resolveAcceptedTurnId: (attempt: { attempt: number }) => Promise<string | undefined>
}): Promise<ChatLongSessionTurnAttemptOutcome<T>> {
  const attempts: ChatLongSessionAttemptMetric[] = []
  let replaceTurnId: string | undefined
  for (let attempt = 1; attempt <= input.maxAttempts; attempt += 1) {
    const delayMs = attempt === 1 ? 0 : attempt === 2 ? 2_000 : 5_000
    if (delayMs > 0) await input.sleep(delayMs)
    const result = await input.submit({ attempt, ...(replaceTurnId ? { replaceTurnId } : {}) })
    const transientFailure = result.failure ? isTransientChatLongSessionFailure(result.failure) : false
    attempts.push({
      attempt,
      delayMs,
      replacement: Boolean(replaceTurnId),
      status: result.status,
      terminalEvent: result.terminalEvent,
      firstDeltaMs: result.firstDeltaMs,
      totalMs: result.totalMs,
      eventCount: result.eventCount,
      ...(result.failure ? { failure: { type: result.failure.type, code: result.failure.code, classification: transientFailure ? 'transient' : 'deterministic' } } : {}),
      ...(result.progress ? { progress: result.progress } : {})
    })
    if (result.status === 200 && result.terminalEvent === 'message.completed') {
      return { status: 'completed', result, attempts }
    }
    if (!result.failure || !transientFailure) {
      return { status: 'failed', result, attempts, reason: 'deterministic_failure' }
    }
    if (attempt >= input.maxAttempts) return { status: 'failed', result, attempts, reason: 'retry_exhausted' }
    replaceTurnId = result.turnId ?? await input.resolveAcceptedTurnId({ attempt })
    if (!replaceTurnId) return { status: 'failed', result, attempts, reason: 'missing_turn_id' }
  }
  throw new Error('chat_long_session_attempt_state_unreachable')
}

export function isTransientChatLongSessionFailure(failure: SafeChatStreamFailure): boolean {
  const code = failure.code.toLowerCase()
  const message = failure.message.trim()
  if (isDeterministicChatLongSessionFailure(code, message)) return false
  if (code === 'gateway_json_parser_busy' || code === 'gateway_json_parser_failed') return true
  if (message === '网关请求解析繁忙，请稍后重试') return true
  if (/^(?:service_unavailable|upstream_(?:temporarily_)?unavailable)$/.test(code)) return true
  if (/(?:^|_)(?:429|5\d\d|timeout|timed_out|etimedout|connection|econnreset|econnrefused|econnaborted|enetunreach|epipe)(?:_|$)/.test(code)) return true
  const exactConnectionTermination = /^(?:(?:typeerror|error):\s*)?(?:(?:upstream\s+)?stream\s+)?terminated[.!]?$/i.test(message)
    || /^(?:(?:typeerror|error):\s*)?socket hang up[.!]?$/i.test(message)
    || /^(?:(?:typeerror|error):\s*)?(?:(?:upstream\s+)?stream\s+)?premature(?:ly)? (?:close|closed)[.!]?$/i.test(message)
    || /^(?:read\s+)?econnreset[.!]?$/i.test(message)
  const transientMessage = /(?:上游暂时不可用|暂时不可用|temporar(?:y|ily) unavailable|(?:http|status(?:\s+code)?)\s*[:=]?\s*(?:429|5\d\d)\b|\b429\s+(?:too many requests|rate limit(?:ed)?)|\b5\d\d\s+(?:internal server error|service unavailable|bad gateway|gateway timeout)|\b(?:request|upstream|gateway|server|network|socket)\s+(?:timed out|timeout (?:occurred|exceeded|expired))|connection (?:reset|refused|aborted|closed|failed|error|timed out)|econnreset|econnrefused|econnaborted|enetunreach|etimedout|epipe|socket hang up|premature(?:ly)? (?:close|closed))/i.test(message)
  return code === 'gateway_stream_failed' && (exactConnectionTermination || transientMessage)
}

function isDeterministicChatLongSessionFailure(code: string, message: string): boolean {
  const deterministicCode = /(?:^|_)(?:invalid(?:_request|_api_key)?|policy|unsupported|forbidden|unauthorized|authentication|permission|content_filter|safety)(?:_|$)/.test(code)
  const deterministicMessage = /(?:\binvalid(?:[_ -](?:request|api key|api_key|parameter|argument))?\b|\bpolicy(?: violation)?\b|\bunsupported\b|\bnot supported\b|\bforbidden\b|\bunauthorized\b|\bauthentication failed\b|\bpermission denied\b|\binvalid_api_key\b|\bcontent[_ -](?:filter|policy)\b)/i.test(message)
  return deterministicCode || deterministicMessage
}
