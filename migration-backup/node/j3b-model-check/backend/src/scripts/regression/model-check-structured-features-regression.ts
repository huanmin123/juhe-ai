import { strict as assert } from 'node:assert'

import {
  extractStructuredIdentityFeatureVector,
  modelIdentityFeatureCategories,
  modelIdentityFeatureCount,
  modelIdentityFeatureVersion
} from '../../modules/model-checks/model-checks-identity-features.js'

assert.equal(modelIdentityFeatureCategories.length, 7)
assert.equal(new Set(modelIdentityFeatureCategories).size, 7)
assert(modelIdentityFeatureVersion.includes('seven-categories'))
for (const [index, category] of modelIdentityFeatureCategories.entries()) {
  const vector = extractStructuredIdentityFeatureVector(category, 'bounded output', { output_tokens: 32 }, true)
  assert.equal(vector.length, modelIdentityFeatureCount)
  assert.equal(vector[index], 1, `${category} 必须写入自己的确定性结构化维度`)
  assert.equal(vector.filter((value, featureIndex) => featureIndex < 7 && value > 0).length, 1)
  assert(vector[7] > 0 && vector[7] <= 1, '第八维只保存有界输出规模')
}
const failed = extractStructuredIdentityFeatureVector('reasoning', 'x'.repeat(10_000), { output_tokens: 10_000 }, false)
assert.equal(failed[2], 0)
assert.equal(failed[7], 1, '输出规模必须有上界')

console.log('模型可信结构化 feature 回归通过：七类确定性特征和有界向量符合预期')
