import { strict as assert } from 'node:assert'

import { finiteNumberQueryValue, integerQueryValue, optionalQueryText } from '../../shared/query-values.js'

assert.equal(integerQueryValue(''), undefined, '空字符串整数 query 应按未传处理')
assert.equal(integerQueryValue('   '), undefined, '空白字符串整数 query 应按未传处理')
assert.equal(integerQueryValue(['', '20']), undefined, '数组 query 只读首个值，空首值不能被解析为 0')
assert.equal(integerQueryValue('20'), 20)
assert.equal(integerQueryValue('20.5'), undefined)
assert.equal(integerQueryValue(30), 30)

assert.equal(finiteNumberQueryValue(''), undefined, '空字符串数值 query 应按未传处理')
assert.equal(finiteNumberQueryValue('  '), undefined, '空白字符串数值 query 应按未传处理')
assert.equal(finiteNumberQueryValue('0'), 0)
assert.equal(finiteNumberQueryValue('12.5'), 12.5)
assert.equal(finiteNumberQueryValue(Number.NaN), undefined)

assert.equal(optionalQueryText('  abc  '), 'abc')
assert.equal(optionalQueryText(['', 'abc']), undefined)
assert.equal(optionalQueryText([' abc ']), 'abc')

console.log('查询参数取值回归通过：空 query 不再被误解析为 0，重复 route 解析已收敛到共享工具')
