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
assert.match(jenkinsfile, /string\(name: 'PROD_FINAL_APPROVAL'/,
  '生产晋级必须带用户最终确认参数')
assert.match(jenkinsfile, /string\(name: 'RELEASE_MODE', defaultValue: 'single-active-stop'/,
  '本次默认生产发布模式必须是全停机单 active')
assert.match(jenkinsfile, /string\(name: 'SCHEMA_CHANGE_CLASS', defaultValue: 'requires-stop'/,
  '数据库变更分类必须显式进入发布参数')
assert.match(jenkinsfile, /stage\('检查 test\/prod GitOps 隔离'\)/,
  '任何 test/prod release state 写入前必须执行环境隔离检查')
assert.match(jenkinsfile, /def assertTestProdGitOpsIsolation\(\)/,
  'Jenkins 必须提供 test/prod GitOps 隔离硬门')
assert.match(jenkinsfile, /targetRevision ==~ \/\^\[0-9a-f\]\{40\}\$\//,
  '生产 Argo 必须固定到不可变 commit，禁止跟随 main 或未接入的分支')
assert.match(jenkinsfile, /生产 Argo targetRevision 未固定到不可变 40 位 commit/,
  '生产 targetRevision 未固定到不可变 commit 时必须 fail closed')
assert.match(jenkinsfile, /if \(environmentName in \['test', 'prod'\]\)[\s\S]*?assertTestProdGitOpsIsolation\(\)/,
  'release state 写入函数必须再次核对 test/prod 隔离，避免刷新竞态绕过硬门')

const prodPromotionStart = jenkinsfile.indexOf("stage('写入 prod 晋级状态')")
const prodPromotionEnd = jenkinsfile.indexOf("stage('选择 prod 回滚版本')")
const prodPromotion = jenkinsfile.slice(prodPromotionStart, prodPromotionEnd)
assert(prodPromotionStart >= 0 && prodPromotionEnd > prodPromotionStart)
assert.match(prodPromotion, /writeReleaseState\('prod'/,
  'DEPLOY_PROD 必须写入 prod release state')
assert.match(prodPromotion, /readTestRelease\(true\)/,
  'DEPLOY_PROD 必须读取并要求 test release state 的 verifier 通过')
assert.match(jenkinsfile, /releaseMode: metadataValue\('test', 'releaseMode'\)/,
  'Jenkins 必须读取 test releaseMode，防止旧双槽候选进入晋级链')
assert.match(jenkinsfile, /validateReleaseStrategy\(release\.releaseMode, release\.schemaChangeClass\)/,
  'Jenkins 必须按数据库变更分类校验 test release state')
assert.doesNotMatch(prodPromotion, /waitForArgoApplication|waitForIngress|verifyJ3aRelease|markReleaseVerified|assertStandardProdPromotionAllowed/,
  'Jenkins 不负责运行态观察；验证由 Jenkins 外部 AI/观测链路执行')
assert.match(jenkinsfile, /def readTestRelease\(boolean requireVerification = false\)/,
  'Jenkins 必须支持仅生产晋级启用 verifier 硬门的读取模式')
assert.match(jenkinsfile, /if \(requireVerification\)[\s\S]*?verificationStatus != 'passed'[\s\S]*?verificationSourceCommit != release\.sourceCommit[\s\S]*?validEvidenceRef/,
  'DEPLOY_PROD 必须硬性要求 verifier status/source/evidenceRef')
assert.match(jenkinsfile, /verificationEvidenceManifestDigest: metadataValueOptional\('test', 'verification\.evidenceManifestDigest'\)/,
  'DEPLOY_PROD 必须读取 verifier evidence manifest 摘要')
assert.match(jenkinsfile, /verificationVerifierIdentity: metadataValueOptional\('test', 'verification\.verifierIdentity'\)/,
  'DEPLOY_PROD 必须读取受控 verifier 身份')
assert.match(jenkinsfile, /verificationVerifiedAt: metadataValueOptional\('test', 'verification\.verifiedAt'\)/,
  'DEPLOY_PROD 必须读取 verifier UTC 时间')
assert.match(jenkinsfile, /verificationReleaseMode: metadataValueOptional\('test', 'verification\.releaseMode'\)/,
  'DEPLOY_PROD 必须读取 verifier 绑定的发布模式')
assert.match(jenkinsfile, /verificationSchemaChangeClass: metadataValueOptional\('test', 'verification\.schemaChangeClass'\)/,
  'DEPLOY_PROD 必须读取 verifier 绑定的数据库变更分类')
assert.match(jenkinsfile, /validSha256Hex\(release\.verificationEvidenceManifestDigest\)/,
  'DEPLOY_PROD 必须校验 verifier evidence manifest 摘要格式')
assert.match(jenkinsfile, /verificationVerifierIdentity ==~ \/\^\[A-Za-z0-9\._:@\\\/-\]\{1,128\}\$\//,
  'DEPLOY_PROD 必须校验 verifier 身份格式')
assert.match(jenkinsfile, /verificationVerifiedAt ==~ \/\^\\d\{4\}-\\d\{2\}-\\d\{2\}T/,
  'DEPLOY_PROD 必须校验 verifier UTC 时间格式')
assert.match(jenkinsfile, /verificationReleaseMode != release\.releaseMode \|\| release\.verificationSchemaChangeClass != release\.schemaChangeClass/,
  'verifier 必须绑定与候选一致的发布模式和数据库变更分类')
assert.match(jenkinsfile, /metadataValue\('test', 'verification\.status'\) != 'passed'[\s\S]*?metadataValue\('test', 'verification\.sourceCommit'\)[\s\S]*?validEvidenceRef\(metadataValue\('test', 'verification\.evidenceRef'\)\)/,
  '写 prod 前必须二次核对 verifier 字段，防止 test release state 竞态')
assert.match(jenkinsfile, /metadataValue\('test', 'verification\.evidenceManifestDigest'\)[\s\S]*?metadataValue\('test', 'verification\.verifierIdentity'\)[\s\S]*?metadataValue\('test', 'verification\.verifiedAt'\)/,
  '写 prod 前必须二次核对 verifier manifest/身份/时间，防止伪造 passed')
assert.match(jenkinsfile, /metadataValue\('test', 'verification\.releaseMode'\) != releaseMode[\s\S]*?metadataValue\('test', 'verification\.schemaChangeClass'\) != schemaChangeClass/,
  '写 prod 前必须二次核对 verifier 绑定的发布模式和数据库变更分类')
assert.doesNotMatch(jenkinsfile, /writeReverseReleaseState|candidateVerification|reverse-blue-green/,
  '当前单 active 发布契约不保留反向蓝绿 release state 写入实现')
assert.match(jenkinsfile, /def metadataValueOptional\(environmentName, key\)/,
  'release metadata 读取必须支持可选字段并保持字段边界')
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
assert.match(jenkinsfile, /params\.DEPLOY_PROD && requestedReleaseMode == 'single-active-stop' && !\(params\.PROD_APPROVAL_TICKET\?\.trim\(\)\)/,
  'single-active-stop prod 晋级缺少用户批准时必须 fail closed')
assert.match(jenkinsfile, /rollbackRequested\(\) \|\| \(params\.DEPLOY_PROD && requestedReleaseMode == 'single-active-stop'\)/,
  '停机晋级或回滚缺少精确用户确认短语时必须 fail closed')
assert.match(jenkinsfile, /def validApprovalTicket\(value\)/,
  '审批单号必须经过受控格式校验，避免参数注入')
assert.match(jenkinsfile, /def prodRollbackCandidates\(\) \{\s*\/\/ 回滚必须从本次构建新鲜读取平台仓库[\s\S]*?refreshPlatformReleaseWorkspace\(\)/,
  'prod 回滚必须先刷新平台仓库，禁止使用陈旧 workspace')
assert.match(jenkinsfile, /expectedPlatformRevision = null/,
  'prod 晋级与回滚必须支持校验平台 release revision，避免候选竞态')
assert.match(jenkinsfile, /selected\.platformRevision,/,
  'prod 回滚必须使用读取历史时的 release revision，避免陈旧 history')
assert.match(jenkinsfile, /if \(reverseDeployRequested\(\)\) \{\s*error 'REVERSE_DEPLOY_PROD 是历史反向入口，已禁用/s,
  '历史反向蓝绿参数必须硬拒绝，不能绕过发布状态机')
assert.match(jenkinsfile, /def blueGreenControlPlaneImplemented\(\) \{[\s\S]*?return false/,
  '在候选镜像、预览验证、Service 无空 Endpoint 切换和 owner handoff 状态机完成前，蓝绿参数必须 fail closed')
assert.match(jenkinsfile, /releaseMode == 'blue-green-additive' && !blueGreenControlPlaneImplemented\(\)/,
  'blue-green-additive 不能仅凭 schema 分类直接放行生产')

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
assert.match(jenkinsfile, /if \[ -f "\\\$runtime_config" \]; then/,
  'J3a 关闭时必须兼容尚未迁移 runtime-config.env 的旧平台 release state')
assert.match(jenkinsfile, /if \[ '\$\{environmentName\}' = 'prod' \] && \[ -f '.*release-history\.tsv'/,
  'release state 提交不得对可选历史文件执行不存在路径的 git add')
assert.match(jenkinsfile, /after_matches[\s\S]*?写入后回读命中数/,
  '镜像 digest 写入后必须回读目标块，避免 kustomization 与 metadata 不一致')
assert(
  jenkinsfile.includes('my \\$temporary = "\\$file.tmp.$$"')
    && jenkinsfile.includes('rename \\$temporary, \\$file'),
  '镜像 digest 写回必须使用临时文件原子替换，避免截断发布文件'
)
assert(jenkinsfile.includes('normalize_line_endings')
  && jenkinsfile.includes('< "\\$target" > "\\$temporary"'),
  'J3a 换行规范化必须读取目标文件内容，不能把空 stdin 写回 release 文件')
assert.match(jenkinsfile, /def writeReleaseState\([\s\S]*?assert_metadata_value sourceCommit/,
  'release metadata 写入必须对关键字段做唯一命中数与目标值回读，避免静默漂移')
assert.match(jenkinsfile, /assert_metadata_value releaseMode '\$\{releaseMode\}'[\s\S]*?assert_metadata_value schemaChangeClass '\$\{schemaChangeClass\}'/,
  'test/prod release state 写入必须回读发布模式和数据库变更分类')
assert.doesNotMatch(jenkinsfile, /assert_metadata_value verification\./,
  'release state 写入不能重置外部 AI 观测字段')
assert.match(jenkinsfile, /metadataValue\('test', 'releaseMode'\) != releaseMode[\s\S]*?metadataValue\('test', 'schemaChangeClass'\) != schemaChangeClass/,
  '生产晋级必须二次绑定 test 候选的 releaseMode 与数据库变更分类')
assert.match(jenkinsfile, /if \(releaseMode == 'single-active-stop'\)[\s\S]*?params\.PROD_FINAL_APPROVAL\?\.trim\(\) != 'I_APPROVE_PROD_SINGLE_ACTIVE_STOP'/,
  'writeReleaseState 本身必须对停机发布再次要求用户最终确认，避免未来调用点绕过 stage')
assert.match(jenkinsfile, /actor == 'jenkins-prod-promotion'[\s\S]*?params\.PROD_APPROVAL_TICKET/,
  'prod 晋级写状态必须在函数内部校验审批单')
assert.match(jenkinsfile, /actor == 'jenkins-prod-rollback'[\s\S]*?params\.ROLLBACK_SCHEMA_COMPATIBILITY_TICKET/,
  'prod 回滚写状态必须在函数内部校验回滚审批和 schema 兼容证据')
assert.match(jenkinsfile, /metadataValue\('test', 'sourceCommit'\)[\s\S]*?metadataValue\('test', 'jobsImageDigest'\)[\s\S]*?metadataValue\('test', 'gatewayImageDigest'\)/,
  '生产晋级二次读取必须继续绑定 test source commit 与全部组件 digest')
assert.match(jenkinsfile, /def validNodeDigest\(value\) \{ return value == '-' \|\| validDigest\(value\) \}/,
  'go-only 收口后 Node 镜像位必须用显式 - 哨兵并通过 validNodeDigest 校验')
assert.match(jenkinsfile, /if \(nodeDigest != '-'\) \{[\s\S]*?replaceDigest\("\$\{overlay\}\/kustomization\.yaml", 'juhe-ai', nodeDigest\)/,
  'go-only 候选不得回写已退役的 juhe-ai Node 镜像块，历史回滚候选仍按真实 digest 复原')
assert.doesNotMatch(jenkinsfile, /HARBOR_REPOSITORY_NODE|NODE_IMAGE|Dockerfile\.builder|stage\('构建前端与 Node 产物'\)/,
  'go-only 流水线不得保留 Node 镜像构建与推送入口')
console.log('Jenkins API release flow contract passed')
