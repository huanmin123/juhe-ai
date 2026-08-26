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
assert.match(jenkinsfile, /def preflightTestRelease\(\)[\s\S]*?ALERTS\{namespace="juhe-ai-test",alertstate="firing"\}/,
  'test 前置检查必须拒绝仍有 juhe-ai-test firing 告警的节点/Pod')

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

assert.match(jenkinsfile, /def sourceUsesDirectJ3aManagement\(\)[\s\S]*?manual_admin\.go[\s\S]*?proxy-latency-handover\.ts/,
  'J3a capability 必须由 direct-Go 源码与已移除 Node handover 共同判定')
assert.match(jenkinsfile, /writeReverseReleaseState\(env\.SOURCE_COMMIT, env\.NODE_DIGEST, env\.JOBS_DIGEST, env\.GATEWAY_DIGEST, env\.J3A_MANAGEMENT_ENABLED\)/,
  '反向蓝绿候选必须携带 J3a capability flag')
assert.match(jenkinsfile, /candidateJ3aManagementEnabled/,
  '反向蓝绿 metadata 必须记录 candidate J3a capability flag')
assert.match(jenkinsfile, /def verifyJ3aRelease\(environmentName, enabled\)[\s\S]*?proxyLatencyEnabled[\s\S]*?proxyLatencyReady[\s\S]*?proxyLatencyOwnerHeld/,
  'J3a 启用时必须直接验证 Go health 的 enabled/ready/owner 字段')
assert.match(jenkinsfile, /node_health=[\s\S]*?proxyLatency[\s\S]*?enabled[\s\S]*?false[\s\S]*?active-path-zero/,
  'J3a 启用时必须验证 Node 旧 proxyLatency active-path-zero')
assert.match(jenkinsfile, /withCredentials\(\[string\(credentialsId: credentialID, variable: 'J3A_RELEASE_VERIFIER_TOKEN'\)\]\)/,
  'J3a 管理验证必须使用受控 Jenkins credential，而非源码或日志中的 token')
assert.match(jenkinsfile, /--subresource=portforward[\s\S]*?--resource-name=\\\$active_pod[\s\S]*?port-forward[\s\S]*?33050:3305/,
  'J3a Go health 必须经具体 Pod 的受限 port-forward 读取，不能由 Node health 代替')
assert.match(jenkinsfile, /\/__aisys__\/api\/proxies\/\$\{proxyID\}\/test[\s\S]*?\/__aisys__\/api\/operation-logs/,
  'J3a 启用时必须先执行精确管理 POST，再回读 F4 operation log')

const j3aTestVerification = jenkinsfile.indexOf("verifyJ3aRelease('test'")
assert(testIngress < j3aTestVerification && j3aTestVerification < testVerification,
  'test 只能在 J3a Go/management/F4 验证通过后标记 passed')
const j3aProdVerification = jenkinsfile.indexOf("verifyJ3aRelease('prod'")
assert(prodIngress < j3aProdVerification && j3aProdVerification < prodVerification,
  'prod 只能在 J3a Go/management/F4 验证通过后标记 passed')

assert.match(jenkinsfile, /def waitForArgoApplication\(applicationName, expectedRevision\)[\s\S]*RELEASE_OBSERVER_KUBECONFIG/,
  'Argo 观察必须使用受限 release observer kubeconfig')
assert.match(jenkinsfile, /Synced\|Healthy\|Succeeded\|\$\{expectedRevision\}/,
  '只有本次 release-state Git revision 已同步、健康并完成时才允许继续发布验证')
assert.match(jenkinsfile, /def waitForArgoApplication\(applicationName, expectedRevision\)[\s\S]*?while \[ \\\$i -lt 60 \]/,
  'Argo release gate 必须以 5 分钟为内网 Harbor 拉取与 Pod startup 的硬上限')
assert.match(jenkinsfile, /operation_phase[\s\S]*?'Failed'[\s\S]*?'Error'[\s\S]*?health_status[\s\S]*?'Degraded'[\s\S]*?exit 1/,
  'Argo 明确 Failed、Error 或 Degraded 时必须立即失败，不能等待至超时')
assert.match(jenkinsfile, /停止等待并检查 Harbor、节点网络、镜像解压、gate、PVC 和 readiness/,
  '5 分钟超时必须输出可行动的分层排障范围')

assert.match(jenkinsfile, /def refreshPlatformReleaseWorkspace\(\)\s*\{[\s\S]*?retry\(3\)[\s\S]*?git clone --depth 1 --branch/,
  'release state 的受限 clone 必须为瞬时 Gitee 断链提供有界重试，且不能降级主机密钥校验')

console.log('Jenkins release Argo gate contract passed')
