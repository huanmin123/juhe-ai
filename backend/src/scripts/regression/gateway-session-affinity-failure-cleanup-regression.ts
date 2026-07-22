import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

const routeSource = readFileSync(new URL('../../modules/gateway/routes.ts', import.meta.url), 'utf8')
const cleanupStart = routeSource.indexOf('const clearActiveDownstreamSessionAffinity = async (): Promise<void> =>')
const requestFailureCatch = routeSource.indexOf('  } catch (error) {\n    await clearActiveDownstreamSessionAffinity()')
const requestFailureAccounting = routeSource.indexOf('    await recordKnownClientIpRequestError(error, gatewayUsageContext, auditCapture)')

assert(cleanupStart >= 0, '已建立的下游会话亲和必须提供可等待的清理入口')
assert(
  routeSource.indexOf('await forgetOpenAIAccountForSessionAsync(binding.key, binding.accountId)', cleanupStart) > cleanupStart,
  '会话亲和清理入口必须等待绑定删除完成，不能在失败路径 fire-and-forget'
)
assert(requestFailureCatch >= 0, '网关请求失败总 catch 必须清理已建立的会话亲和')
assert(
  requestFailureAccounting > requestFailureCatch,
  '会话亲和必须在失败记账和错误响应前清理，避免同一 TTL 内继续偏向失败账号'
)

console.log('网关会话亲和失败清理回归通过：响应处理异常和请求失败会先清除已建立绑定')
