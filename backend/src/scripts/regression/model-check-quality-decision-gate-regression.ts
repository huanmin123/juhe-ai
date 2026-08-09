import { strict as assert } from 'node:assert'

import { resolveModelQualityDecisionGate } from '../../modules/model-checks/model-checks.service.js'

const ordinaryContentAnomalyAboveThreshold = resolveModelQualityDecisionGate({
  completed: true,
  unavailable: false,
  score: 86,
  threshold: 70,
  mappingStatus: 'direct',
  gpt56JuiceStrongRepeated: false
})
assert.deepEqual(ordinaryContentAnomalyAboveThreshold, {
  hardFailure: false,
  qualityFailed: false
}, 'HTTP 200 普通内容异常只能扣分，分数仍高于阈值不得自动处罚')

const ordinaryContentAnomalyBelowThreshold = resolveModelQualityDecisionGate({
  completed: true,
  unavailable: false,
  score: 69,
  threshold: 70,
  mappingStatus: 'direct',
  gpt56JuiceStrongRepeated: false
})
assert.deepEqual(ordinaryContentAnomalyBelowThreshold, {
  hardFailure: false,
  qualityFailed: true
}, 'HTTP 200 普通内容异常扣分后低于阈值必须触发既有质量策略')

const modelMismatch = resolveModelQualityDecisionGate({
  completed: true,
  unavailable: false,
  score: 100,
  threshold: 70,
  mappingStatus: 'undeclared_mismatch',
  gpt56JuiceStrongRepeated: false
})
assert.deepEqual(modelMismatch, {
  hardFailure: true,
  qualityFailed: true
}, '未声明响应模型冲突必须保持硬失败')

const repeatedStrongJuice = resolveModelQualityDecisionGate({
  completed: true,
  unavailable: false,
  score: 100,
  threshold: 70,
  mappingStatus: 'direct',
  gpt56JuiceStrongRepeated: true
})
assert.deepEqual(repeatedStrongJuice, {
  hardFailure: true,
  qualityFailed: true
}, '连续复现的 Juice 强异常必须保持硬失败')

const unavailable = resolveModelQualityDecisionGate({
  completed: true,
  unavailable: true,
  score: 0,
  threshold: 70,
  mappingStatus: 'direct',
  gpt56JuiceStrongRepeated: false
})
assert.deepEqual(unavailable, {
  hardFailure: false,
  qualityFailed: false
}, '未形成质量证据的不可用 run 不得处罚')

console.log('模型检测质量决策门回归通过：HTTP 200 内容异常先扣分，只有模型字段冲突或连续 Juice 强异常可绕过阈值')
