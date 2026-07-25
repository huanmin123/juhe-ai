import { strict as assert } from 'node:assert'

import { testItemStatusColor, testItemStatusText } from '@/views/proxies/proxyDisplay'

assert.equal(testItemStatusColor('passed'), 'green')
assert.equal(testItemStatusColor('warning'), 'gold')
assert.equal(testItemStatusColor('failed'), 'red')
assert.equal(testItemStatusColor('unknown'), 'default', '未形成真实 attempt 的检测项不得显示为失败红色')

assert.equal(testItemStatusText('passed'), '通过')
assert.equal(testItemStatusText('warning'), '告警')
assert.equal(testItemStatusText('failed'), '失败')
assert.equal(testItemStatusText('unknown'), '未知', '未形成真实 attempt 的检测项必须明确显示未知')

console.log('代理检测状态展示回归通过：unknown 不再伪装为失败')
