import { strict as assert } from 'node:assert'

import { normalRouteSpeedFirstAppliesToLane } from '../../modules/gateway/policy/speed-first-lane.js'

assert.equal(normalRouteSpeedFirstAppliesToLane('text'), true, '普通路由快速模式应继续应用于文本 lane')
assert.equal(normalRouteSpeedFirstAppliesToLane('image'), false, '图像 lane 不应记录快速模式慢样本或执行首字切号')

console.log('speed first lane regression passed')
