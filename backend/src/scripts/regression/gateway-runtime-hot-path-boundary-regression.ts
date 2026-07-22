import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import { gatewayRuntimeDbServiceTimeoutMs } from '../../modules/gateway/runtime/runtime-cache.service.js'

assert.equal(gatewayRuntimeDbServiceTimeoutMs, 10_000, '网关运行态冷缓存 DB service 读取应使用 10s 独立超时')

const runtimeCacheSource = readFileSync(new URL('../../modules/gateway/runtime/runtime-cache.service.ts', import.meta.url), 'utf8')
const dbServiceIpcSource = readFileSync(new URL('../../modules/db-service/db-service-ipc.ts', import.meta.url), 'utf8')
assert.match(dbServiceIpcSource, /const requestTimeoutMs = 5000/, 'DB service 默认 IPC timeout 应保持短保护，避免普通请求堆积')
assert.match(runtimeCacheSource, /export const gatewayRuntimeDbServiceTimeoutMs = 10_000/, '网关运行态冷缓存应显式声明独立 timeout')
assert.match(runtimeCacheSource, /type:\s*'read_gateway_runtime'[\s\S]*timeoutMs:\s*gatewayRuntimeDbServiceTimeoutMs/, 'read_gateway_runtime 请求必须传入独立 timeout，避免冷缓存高峰过早 503')
assert.match(runtimeCacheSource, /gatewayRuntimeRetainTtlMs\s*=\s*10\s*\*\s*60_000/, '网关运行态缓存必须保留 stale 快照')
assert.match(runtimeCacheSource, /refreshGatewayRuntimeInBackground\(apiKey,\s*cacheKey\)/, '软过期运行态应后台刷新，不应让请求链路硬等 DB')
assert.match(runtimeCacheSource, /sanitizedGatewayRuntimeForDispatch\(cached\.runtime\)/, 'stale 快照返回前必须按当前时间过滤 API Key、授权和账号')
assert.match(runtimeCacheSource, /const pendingGatewayRuntimeLoads = new Map<string, Promise<DbServiceGatewayRuntime>>/, '同一 API Key 冷缓存读取必须具备 promise 合并保护')
assert.match(runtimeCacheSource, /pendingGatewayRuntimeLoads\.set\(cacheKey,\s*load\)/, '同一 API Key 冷缓存读取应复用同一个 DB service 请求')

console.log('gateway-runtime-hot-path-boundary-regression passed')
