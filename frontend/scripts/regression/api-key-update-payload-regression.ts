import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const frontendRoot = resolve(import.meta.dirname, '../..')
const source = readFileSync(resolve(frontendRoot, 'src/api/domains/apiKeys.ts'), 'utf8')

assert(
  /quotaLimits\?\s*:\s*ApiKeyQuotaLimits\s*\|\s*null/.test(source),
  'API Key 更新 payload 必须允许 quotaLimits: null 清空额度限制'
)
assert(
  source.includes("http.patch(`/api-keys/${encodeURIComponent(id)}`, payload, { params })"),
  '管理员 API Key 更新必须使用 encodeURIComponent(id) 编码动态路径段'
)
assert(
  source.includes("http.patch(`/my-api-keys/${encodeURIComponent(id)}`, payload)"),
  '个人 API Key 更新必须使用 encodeURIComponent(id) 编码动态路径段'
)

console.log('API Key 更新 payload 回归测试通过')

function assert(condition: boolean, message: string): asserts condition {
  if (!condition) {
    throw new Error(message)
  }
}
