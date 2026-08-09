import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

function source(relativePath: string): string {
  return readFileSync(fileURLToPath(new URL(`../../${relativePath}`, import.meta.url)), 'utf8')
}

function includes(text: string, fragment: string, label: string): void {
  assert(text.includes(fragment), `${label} 缺少源码证据：${fragment}`)
}

const client = source('modules/audit-logs/audit-log-go-input.service.ts')
const runtime = source('config/runtime.ts')

for (const contract of [
  "export const auditLogGoInputPath = '/__aiinternal__/v1/audit-captures'",
  'export function dispatchAuditLogToGo(input: AuditLogInput): void',
  "method: 'POST'",
  "'content-length'",
  "'x-juhe-ai-signature'",
  "createHmac('sha256', runtimeConfig.secret)",
  "AbortSignal.timeout(auditLogGoInputTimeoutMs)",
  'auditLogGoInputMaxBytes',
  'void fetch(endpoint',
  "response.status === 204"
]) {
  includes(client, contract, 'Node->Go F3 one-shot input RPC')
}
assert(!client.includes('enqueueAuditLog'), 'Go input client 不得依赖 Node audit queue')
assert(!client.includes('Redis'), 'Go input client 不得引入 Redis fallback')
assert(!client.includes('retry'), 'Go input client 不得自动重试')
for (const contract of [
  'auditLogInputUrl?: string',
  "'JUHE_AI_AUDIT_LOG_INPUT_URL'",
  'function auditLogInputUrlConfig',
  'loopback HTTP Origin'
]) {
  includes(runtime, contract, 'F3 input URL 配置门禁')
}

console.log('F3 Node->Go input RPC contract passed: loopback URL, HMAC, 4MiB budget, 204 acknowledgement, no retry/fallback.')
