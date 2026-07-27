/**
 * Header policy is deliberately separate from session resolution.
 *
 * A header can be request-dynamic or sensitive without being a stable model
 * conversation identifier. Keep this registry focused on protocol boundaries;
 * session resolvers remain client-specific.
 */

/**
 * Headers whose semantics belong to a Codex Responses request and must not
 * leak into a reconstructed Chat Completions or Anthropic Messages request.
 * Capability-only values are included here even when they are not sensitive.
 */
export const codexResponsesScopedHeaderNames = [
  'openai-beta',
  'originator',
  'session-id',
  'thread-id',
  'x-client-request-id',
  'x-oai-attestation',
  'x-openai-internal-codex-responses-lite',
  'x-openai-memgen-request',
  'x-openai-subagent',
  'x-responsesapi-include-timing-metrics'
] as const

const codexResponsesScopedHeaderNameSet = new Set<string>(codexResponsesScopedHeaderNames)
const codexResponsesScopedHeaderPrefixes = ['x-codex-'] as const

export function isCodexResponsesScopedHeaderName(name: string): boolean {
  const normalized = normalizeHeaderName(name)
  return codexResponsesScopedHeaderNameSet.has(normalized)
    || codexResponsesScopedHeaderPrefixes.some((prefix) => normalized.startsWith(prefix))
}

export function stripCodexResponsesScopedHeaders(headers: Headers): void {
  stripHeaders(headers, isCodexResponsesScopedHeaderName)
}

export const anthropicMessagesScopedHeaderNames = [
  'anthropic-beta',
  'anthropic-dangerous-direct-browser-access',
  'anthropic-version',
  'x-api-key',
  'x-app',
  'x-claude-code-agent-id',
  'x-claude-code-session-id'
] as const

const anthropicMessagesScopedHeaderNameSet = new Set<string>(anthropicMessagesScopedHeaderNames)
const anthropicMessagesScopedHeaderPrefixes = ['anthropic-', 'x-claude-code-', 'x-stainless-'] as const

export function isAnthropicMessagesScopedHeaderName(name: string): boolean {
  const normalized = normalizeHeaderName(name)
  return anthropicMessagesScopedHeaderNameSet.has(normalized)
    || anthropicMessagesScopedHeaderPrefixes.some((prefix) => normalized.startsWith(prefix))
}

export function stripAnthropicMessagesScopedHeaders(headers: Headers): void {
  stripHeaders(headers, isAnthropicMessagesScopedHeaderName)
}

export const geminiGenerateContentScopedHeaderNames = [
  'x-gemini-api-privileged-user-id',
  'x-goog-api-client',
  'x-goog-api-key',
  'x-vertex-ai-llm-request-type',
  'x-vertex-ai-llm-shared-request-type'
] as const

const geminiGenerateContentScopedHeaderNameSet = new Set<string>(geminiGenerateContentScopedHeaderNames)
const geminiGenerateContentScopedHeaderPrefixes = ['x-gemini-', 'x-goog-', 'x-vertex-ai-'] as const

export function isGeminiGenerateContentScopedHeaderName(name: string): boolean {
  const normalized = normalizeHeaderName(name)
  return geminiGenerateContentScopedHeaderNameSet.has(normalized)
    || geminiGenerateContentScopedHeaderPrefixes.some((prefix) => normalized.startsWith(prefix))
}

export function stripGeminiGenerateContentScopedHeaders(headers: Headers): void {
  stripHeaders(headers, isGeminiGenerateContentScopedHeaderName)
}

function stripHeaders(headers: Headers, shouldStrip: (name: string) => boolean): void {
  for (const name of [...headers.keys()]) {
    if (shouldStrip(name)) headers.delete(name)
  }
}

function normalizeHeaderName(name: string): string {
  return name.trim().toLowerCase()
}
