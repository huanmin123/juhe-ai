import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const jenkinsfile = readFileSync(resolve(import.meta.dirname, '../../Jenkinsfile'), 'utf8')

assert.match(jenkinsfile, /booleanParam\(name: 'DEPLOY_PROD'/,
  'Jenkins 必须保留 API 可触发的 prod 晋级参数')
assert.match(jenkinsfile, /booleanParam\(name: 'ROLLBACK_PROD'/,
  'Jenkins 必须保留 API 可触发的 prod 回滚参数')
assert.match(jenkinsfile, /string\(name: 'TARGET_PROD_SOURCE_COMMIT'/,
  '回滚目标必须可由 Jenkins API 通过 sourceCommit 参数传入')

const prodPromotionStart = jenkinsfile.indexOf("stage('写入 prod 晋级状态')")
const prodPromotionEnd = jenkinsfile.indexOf("stage('写入 prod 反向候选状态')")
const prodPromotion = jenkinsfile.slice(prodPromotionStart, prodPromotionEnd)
assert(prodPromotionStart >= 0 && prodPromotionEnd > prodPromotionStart)
assert.match(prodPromotion, /writeReleaseState\('prod'/,
  'DEPLOY_PROD 必须写入 prod release state')
assert.doesNotMatch(prodPromotion, /waitForArgoApplication|waitForIngress|verifyJ3aRelease|markReleaseVerified|assertStandardProdPromotionAllowed/,
  'prod 晋级不应等待 Argo、入口、J3a 或人工验证')

const rollbackStart = jenkinsfile.indexOf("stage('选择 prod 回滚版本')")
const rollbackEnd = jenkinsfile.indexOf('  }\n}', rollbackStart)
const rollback = jenkinsfile.slice(rollbackStart, rollbackEnd)
assert.match(rollback, /TARGET_PROD_SOURCE_COMMIT/,
  'prod 回滚必须从 API 参数读取目标版本')
assert.doesNotMatch(rollback, /input\(/,
  'prod 回滚不应依赖 Jenkins UI input 步骤')
assert.match(rollback, /writeReleaseState\('prod'/,
  'prod 回滚必须写入 prod release state')

assert.doesNotMatch(jenkinsfile, /stage\('验证 test'\)|stage\('验证 prod'\)/,
  '上线流水线不再包含独立验证 stage')
assert.match(jenkinsfile, /def writeReleaseState\([\s\S]*?release-history\.tsv/,
  'prod 写入必须继续记录历史，保留无 UI 回滚能力')

console.log('Jenkins API release flow contract passed')
