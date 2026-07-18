import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { isUsageRecordFirstPageOptions } from '../../modules/usage-records/usage-record-first-page-cache.service.js'

assert.equal(isUsageRecordFirstPageOptions({ page: 1, pageSize: 20, sortBy: 'createdAt', sortOrder: 'desc', trafficSource: 'gateway' }), true)
assert.equal(isUsageRecordFirstPageOptions({ page: 2, pageSize: 20, sortBy: 'createdAt', sortOrder: 'desc', trafficSource: 'gateway' }), false)
assert.equal(isUsageRecordFirstPageOptions({ page: 1, pageSize: 20, sortBy: 'createdAt', sortOrder: 'desc', trafficSource: 'manual_account_test' }), false)
assert.equal(isUsageRecordFirstPageOptions({ page: 1, pageSize: 20, sortBy: 'createdAt', sortOrder: 'desc', trafficSource: 'gateway', model: 'gpt-5' }), false)

const routeSource = readFileSync(resolve('src/modules/usage-records/usage-records.routes.ts'), 'utf8')
assert.match(routeSource, /getUsageRecordFirstPage/, '使用记录路由必须优先读取首屏热列表')
assert.match(routeSource, /seedUsageRecordFirstPage/, '首屏热列表 miss 后必须回填')
assert.doesNotMatch(routeSource, /getUsageRecordListResponseWithCache/, '使用记录路由不得保留通用读取后响应缓存')

const cacheSource = readFileSync(resolve('src/modules/usage-records/usage-record-first-page-cache.service.ts'), 'utf8')
assert.match(cacheSource, /trafficSource === 'gateway'/, '首屏热列表只接收网关请求')
assert.match(cacheSource, /firstPageResponseSize = 20/, '首屏热列表必须固定为 20 条')
assert.match(cacheSource, /publishUsageRecordFirstPage/, '落库后必须更新首屏热列表')

console.log('使用记录首屏热列表回归通过：默认网关请求首批、写入驱动、miss 回填和通用响应缓存移除均已生效')
