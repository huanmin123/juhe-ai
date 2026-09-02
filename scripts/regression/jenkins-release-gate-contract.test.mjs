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
assert.match(jenkinsfile, /string\(name: 'ROLLBACK_APPROVAL_TICKET'/,
  '回滚必须带受控审批单号')
assert.match(jenkinsfile, /string\(name: 'ROLLBACK_SCHEMA_COMPATIBILITY_TICKET'/,
  '回滚必须带 schema 前向兼容证据单号')
assert.match(jenkinsfile, /string\(name: 'PROD_APPROVAL_TICKET'/,
  '生产晋级必须带用户最终批准单号')
assert.match(jenkinsfile, /string\(name: 'RELEASE_MODE', defaultValue: 'single-active-stop'/,
  '生产发布模式必须显式固定为全停机单 active')

const prodPromotionStart = jenkinsfile.indexOf("stage('写入 prod 晋级状态')")
const prodPromotionEnd = jenkinsfile.indexOf("stage('写入 prod 反向候选状态')")
const prodPromotion = jenkinsfile.slice(prodPromotionStart, prodPromotionEnd)
assert(prodPromotionStart >= 0 && prodPromotionEnd > prodPromotionStart)
assert.match(prodPromotion, /writeReleaseState\('prod'/,
  'DEPLOY_PROD 必须写入 prod release state')
assert.match(prodPromotion, /readTestRelease\(\)/,
  'DEPLOY_PROD 必须读取 test release state 中的 source/digest')
assert.doesNotMatch(prodPromotion, /waitForArgoApplication|waitForIngress|verifyJ3aRelease|markReleaseVerified|assertStandardProdPromotionAllowed/,
  'Jenkins 不负责运行态观察；验证由 Jenkins 外部 AI/观测链路执行')
assert.doesNotMatch(jenkinsfile, /verification\.status.*passed|verification\.sourceCommit|verification\.evidenceRef|独立 verifier/,
  'Jenkins 不得读取或要求运行态 verification 门禁')
assert.match(jenkinsfile, /def metadataValueOptional\(environmentName, key\)/,
  'release metadata 读取必须支持可选字段且不依赖运行态 verification')
assert.match(jenkinsfile, /grep -F -- '\$\{prefix\}' '\$\{file\}' \|\| true/,
  'release metadata 读取必须使用字面前缀，避免未转义正则键误匹配')
assert.match(jenkinsfile, /lines\.size\(\) > 1[\s\S]*?命中 .*拒绝使用不唯一字段/,
  'release metadata 重复键必须 fail closed，不能静默取第一行')

const rollbackStart = jenkinsfile.indexOf("stage('选择 prod 回滚版本')")
const rollbackEnd = jenkinsfile.indexOf('  }\n}', rollbackStart)
const rollback = jenkinsfile.slice(rollbackStart, rollbackEnd)
assert.match(rollback, /TARGET_PROD_SOURCE_COMMIT/,
  'prod 回滚必须从 API 参数读取目标版本')
assert.doesNotMatch(rollback, /input\(/,
  'prod 回滚不应依赖 Jenkins UI input 步骤')
assert.match(rollback, /writeReleaseState\('prod'/,
  'prod 回滚必须写入 prod release state')
assert.match(jenkinsfile, /if \(rollbackRequested\(\) && !\(params\.ROLLBACK_APPROVAL_TICKET\?\.trim\(\)\)\)/,
  'prod 回滚缺少审批单号时必须 fail closed')
assert.match(jenkinsfile, /if \(rollbackRequested\(\) && !\(params\.ROLLBACK_SCHEMA_COMPATIBILITY_TICKET\?\.trim\(\)\)\)/,
  'prod 回滚缺少 schema 兼容证据时必须 fail closed')
assert.match(jenkinsfile, /if \(params\.DEPLOY_PROD && !\(params\.PROD_APPROVAL_TICKET\?\.trim\(\)\)\)/,
  'prod 晋级缺少用户最终批准时必须 fail closed')
assert.match(jenkinsfile, /def validApprovalTicket\(value\)/,
  '审批单号必须经过受控格式校验，避免参数注入')
assert.match(jenkinsfile, /def prodRollbackCandidates\(\) \{\s*\/\/ 回滚必须从本次构建新鲜读取平台仓库[\s\S]*?refreshPlatformReleaseWorkspace\(\)/,
  'prod 回滚必须先刷新平台仓库，禁止使用陈旧 workspace')
assert.match(jenkinsfile, /expectedPlatformRevision = null/,
  'prod 晋级与回滚必须支持校验平台 release revision，避免候选竞态')
assert.match(jenkinsfile, /selected\.platformRevision\)/,
  'prod 回滚必须使用读取历史时的 release revision，避免陈旧 history')
assert.match(jenkinsfile, /if \(reverseDeployRequested\(\)\) \{\s*error 'REVERSE_DEPLOY_PROD 已被本次全停机单 active 发布策略明确禁止/s,
  '本次全停机单 active 发布必须硬拒绝反向蓝绿参数')

assert.doesNotMatch(jenkinsfile, /stage\('验证 test'\)|stage\('验证 prod'\)/,
  '上线流水线不再包含独立验证 stage')
assert.match(jenkinsfile, /def writeReleaseState\([\s\S]*?release-history\.tsv/,
  'prod 写入必须继续记录历史，保留无 UI 回滚能力')
assert.doesNotMatch(jenkinsfile, /sed -i 's\/replicas: 0\/replicas: 1\//,
  'release state 写入不得把单 active/停机候选静默恢复为双槽副本')
assert.match(jenkinsfile, /def sourceUsesDirectJ3aManagement\(\) \{[\s\S]*?return false/,
  'J3a 在独立迁移契约验收前必须保持显式关闭，不能由源码文件存在性自动推断开启')
assert.match(jenkinsfile, /route_count=.*grep -Fxc[\s\S]*?J3a IngressRoute resource must appear exactly once/,
  'J3a 路由和开关替换必须做命中数与回读校验，避免 metadata 与 kustomization 漂移')
assert.match(jenkinsfile, /def writeReleaseState\([\s\S]*?assert_metadata_value sourceCommit/,
  'release metadata 写入必须对关键字段做唯一命中数与目标值回读，避免静默漂移')
assert.match(jenkinsfile, /actor in \['jenkins-prod-promotion', 'jenkins-prod-rollback'\][\s\S]*?releaseMode.*single-active-stop/,
  '生产晋级和回滚必须拒绝旧的双槽/standby releaseMode')
console.log('Jenkins API release flow contract passed')
