import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const jenkinsfile = readFileSync(resolve(import.meta.dirname, '../../Jenkinsfile'), 'utf8')

assert.match(jenkinsfile, /INGRESS_ENDPOINT = 'http:\/\/127\.0\.0\.1:32080'/,
  'Jenkins 发布验证必须走 infra-linux 本机 NodePort，不能依赖 app-mac-vm LAN 路径')
assert.match(jenkinsfile, /stage\('test 发布前置检查'\)[\s\S]*?preflightTestRelease\(\)/,
  'test 构建前必须执行基础环境门禁')
assert.match(jenkinsfile, /def preflightTestRelease\(\)[\s\S]*?juhe-ai-test[\s\S]*?pg_blocking_sessions_blocked_sessions[\s\S]*?pg_stat_activity_max_tx_duration/,
  'test 前置检查必须覆盖 observer 权限、Argo、入口、数据库锁与长事务')

assert.match(jenkinsfile, /env\.TEST_RELEASE_STATE_REVISION = writeReleaseState\('test'/,
  'test 必须保存本次 release-state Git revision')
assert.match(jenkinsfile, /env\.PROD_RELEASE_STATE_REVISION = writeReleaseState\('prod'/,
  'prod 晋级和回滚必须保存本次 release-state Git revision')

const testGate = jenkinsfile.indexOf("waitForArgoApplication('juhe-ai-test', env.TEST_RELEASE_STATE_REVISION)")
const testIngress = jenkinsfile.indexOf("waitForIngress('test')")
const testVerification = jenkinsfile.indexOf("markReleaseVerified('test'")
assert(testGate >= 0 && testGate < testIngress && testIngress < testVerification,
  'test 必须先等待 Argo Synced/Healthy/Succeeded，再验证入口并标记 release passed')

const prodGate = jenkinsfile.indexOf("waitForArgoApplication('juhe-ai-prod', env.PROD_RELEASE_STATE_REVISION)")
const prodIngress = jenkinsfile.indexOf("waitForIngress('prod')")
const prodVerification = jenkinsfile.indexOf("markReleaseVerified('prod'")
assert(prodGate >= 0 && prodGate < prodIngress && prodIngress < prodVerification,
  'prod 必须先等待 Argo Synced/Healthy/Succeeded，再验证入口并标记 release passed')

assert.match(jenkinsfile, /def waitForArgoApplication\(applicationName, expectedRevision\)[\s\S]*RELEASE_OBSERVER_KUBECONFIG/,
  'Argo 观察必须使用受限 release observer kubeconfig')
assert.match(jenkinsfile, /Synced\|Healthy\|Succeeded\|\$\{expectedRevision\}/,
  '只有本次 release-state Git revision 已同步、健康并完成时才允许继续发布验证')

assert.match(jenkinsfile, /def refreshPlatformReleaseWorkspace\(\)\s*\{[\s\S]*?retry\(3\)[\s\S]*?git clone --depth 1 --branch/,
  'release state 的受限 clone 必须为瞬时 Gitee 断链提供有界重试，且不能降级主机密钥校验')

console.log('Jenkins release Argo gate contract passed')
