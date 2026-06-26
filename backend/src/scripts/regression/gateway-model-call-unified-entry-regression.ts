import { strict as assert } from 'node:assert'
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

interface SourceFinding {
  rule: string
  file: string
  line: number
  text: string
}

const scriptPath = fileURLToPath(import.meta.url)
const sourceRoot = resolve(dirname(scriptPath), '../..')
const backendRoot = resolve(sourceRoot, '..')
const repoRoot = resolve(backendRoot, '..')

const productionRoots = existingPaths([
  resolve(sourceRoot, 'modules'),
  resolve(sourceRoot, 'shared'),
  resolve(sourceRoot, 'storage'),
  resolve(sourceRoot, 'server.ts'),
  resolve(sourceRoot, 'worker.ts'),
  resolve(sourceRoot, 'db-service.ts')
])
const productionSourceFiles = collectSourceFiles(productionRoots)
const appSourceFiles = collectSourceFiles(existingPaths([
  sourceRoot,
  resolve(repoRoot, 'frontend', 'src')
]))

const findings: SourceFinding[] = []
const gatewayPreflightSource = readFileSync(resolve(sourceRoot, 'modules/gateway/request/preflight.ts'), 'utf8')
const codexCompactPreflightSource = readFileSync(resolve(sourceRoot, 'modules/gateway/codex-responses/compact-preflight.ts'), 'utf8')
const imageGenerationExecutorSource = readFileSync(resolve(sourceRoot, 'modules/openai-compatible-images/image-generation-executor.ts'), 'utf8')
const computerAdapterSource = readFileSync(resolve(sourceRoot, 'modules/openai-compatible-computer/computer-adapter.ts'), 'utf8')
const accountDispatchEntrypointPattern =
  /\b(?:listCachedOpenAIAccountsForGroupAsync|prepareOpenAIGatewayDispatchContext|prepareOpenAIGatewayDispatchAccounts|fetchFirstAvailableUpstream|requestUpstream)\b/

assertOnlyAllowedCallSites({
  rule: '上游真实请求只能由公共 dispatch attempt 层触发',
  files: productionSourceFiles,
  pattern: /\brequestUpstream\s*\(/g,
  allowedFiles: [
    'backend/src/modules/gateway/upstream/request.ts',
    'backend/src/modules/gateway/dispatch/upstream-attempts.ts'
  ]
})

assertOnlyAllowedCallSites({
  rule: '公共上游调度入口只能被网关主链路和已审计内部辅助链路调用',
  files: productionSourceFiles,
  pattern: /\bfetchFirstAvailableUpstream\s*\(/g,
  allowedFiles: [
    'backend/src/modules/gateway/dispatch/upstream-dispatch.ts',
    'backend/src/modules/gateway/routes.ts',
    'backend/src/modules/gateway/codex-responses/compact-preflight.ts',
    'backend/src/modules/gateway/hybrid/auxiliary-dispatch.service.ts'
  ]
})

assertOnlyAllowedCallSites({
  rule: '混合模型评分/质检只能通过统一的辅助调度服务发起',
  files: productionSourceFiles,
  pattern: /\bdispatchHybridAuxiliaryChatCompletion\s*\(/g,
  allowedFiles: [
    'backend/src/modules/gateway/hybrid/auxiliary-dispatch.service.ts',
    'backend/src/modules/gateway/hybrid/scoring.service.ts',
    'backend/src/modules/gateway/hybrid/quality-inspection.service.ts'
  ]
})

assertOnlyAllowedCallSites({
  rule: '公共账号调度准备只能由网关预检和已审计辅助调度服务调用',
  files: productionSourceFiles,
  pattern: /\bprepareOpenAIGatewayDispatchAccounts\s*\(/g,
  allowedFiles: [
    'backend/src/modules/gateway/dispatch/preparation.ts',
    'backend/src/modules/gateway/request/preflight.ts',
    'backend/src/modules/gateway/hybrid/auxiliary-dispatch.service.ts'
  ]
})

assertOnlyAllowedCallSites({
  rule: '账号候选读取只能出现在公共预检、路由候选和运行时缓存层',
  files: productionSourceFiles,
  pattern: /\b(?:listCachedOpenAIAccountsForGroupAsync|listFreshOpenAIAccountsForGroupAsync|listRecoverableUnavailableOpenAIAccountsForGroupAsync)\s*\(/g,
  allowedFiles: [
    'backend/src/modules/gateway/runtime/runtime-cache.service.ts',
    'backend/src/modules/gateway/request/preflight.ts',
    'backend/src/modules/gateway/dispatch/api-key-group-fallback-candidate.ts',
    'backend/src/modules/gateway/routing/model-target-group-selector.ts'
  ]
})

assertSourceOrder({
  rule: 'Codex compact 摘要请求必须在公共账号调度准备完成后执行',
  file: 'backend/src/modules/gateway/request/preflight.ts',
  source: gatewayPreflightSource,
  before: 'prepareOpenAIGatewayDispatchAccounts({',
  after: 'applyCodexResponsesChatBridgeCompactPreflight({'
})
assert.match(
  gatewayPreflightSource,
  /dispatchAccounts:\s*dispatchPreparation\.accounts/,
  'Codex compact 摘要请求必须使用公共调度准备后的账号列表'
)
assert.doesNotMatch(
  codexCompactPreflightSource,
  /\brawCandidateAccounts\b/,
  'Codex compact 摘要请求不能直接使用未准备的原始候选账号'
)

assertNoMatches({
  rule: '生产后端不允许直接 fetch 模型上游；配置型图像工具 provider 和 hosted tool runtime adapter 是非账号调度例外',
  files: productionSourceFiles,
  pattern: /\bfetch\s*\(/g,
  allowedFiles: [
    'backend/src/modules/openai-compatible-images/image-generation-executor.ts',
    'backend/src/modules/openai-compatible-computer/computer-adapter.ts'
  ]
})
assert.doesNotMatch(
  imageGenerationExecutorSource,
  accountDispatchEntrypointPattern,
  '配置型图像工具 provider 不能引入账号调度或上游传输入口'
)
assert.doesNotMatch(
  computerAdapterSource,
  accountDispatchEntrypointPattern,
  'Computer hosted tool adapter 不能引入模型账号调度或上游传输入口'
)

assertNoMatches({
  rule: '应用源码不允许直接使用模型 SDK 或 SDK 风格调用',
  files: appSourceFiles,
  pattern: /(?:from\s+['"]openai['"]|require\s*\(\s*['"]openai['"]\s*\)|\bnew\s+OpenAI\s*\(|from\s+['"]@anthropic-ai\/sdk['"]|require\s*\(\s*['"]@anthropic-ai\/sdk['"]\s*\)|\bnew\s+Anthropic\s*\(|\.chat\.completions\.create\s*\(|\.responses\.create\s*\(|\.messages\.create\s*\()/g
})

assertNoMatches({
  rule: '应用源码不允许直接 fetch 公共模型上游域名',
  files: appSourceFiles,
  pattern: /\bfetch\s*\(\s*['"`][^'"`]*(?:api\.openai\.com|api\.anthropic\.com|generativelanguage\.googleapis\.com)/g
})

const scriptRequestUpstreamCallers = collectSourceFiles([resolve(sourceRoot, 'scripts')])
  .filter((file) => normalizeRepoPath(file) !== normalizeRepoPath(scriptPath))
assertOnlyAllowedCallSites({
  rule: '回归/性能脚本不应绕过公共网关发起模型调用；只有上游传输层专项脚本可直测 requestUpstream',
  files: scriptRequestUpstreamCallers,
  pattern: /\brequestUpstream\s*\(/g,
  allowedFiles: [
    'backend/src/scripts/regression/gateway-upstream-keepalive-regression.ts',
    'backend/src/scripts/regression/upstream-base-url-ssrf-policy-regression.ts'
  ]
})

if (findings.length > 0) {
  const details = findings
    .map((finding) => `${finding.rule}\n  ${finding.file}:${finding.line} ${finding.text}`)
    .join('\n')
  assert.fail(`模型调用统一入口静态检查失败：\n${details}`)
}

console.log('模型调用统一入口静态检查通过：生产模型调用集中在公共网关调度链路')

function assertOnlyAllowedCallSites(input: {
  rule: string
  files: string[]
  pattern: RegExp
  allowedFiles: string[]
}): void {
  const allowed = new Set(input.allowedFiles.map(normalizeAllowedRepoPath))
  for (const file of input.files) {
    const repoPath = normalizeRepoPath(file)
    if (allowed.has(repoPath)) continue
    collectMatches(file, input.pattern).forEach((match) => findings.push({ rule: input.rule, ...match }))
  }
}

function assertNoMatches(input: {
  rule: string
  files: string[]
  pattern: RegExp
  allowedFiles?: string[]
}): void {
  const allowed = new Set((input.allowedFiles ?? []).map(normalizeAllowedRepoPath))
  for (const file of input.files) {
    if (allowed.has(normalizeRepoPath(file))) continue
    collectMatches(file, input.pattern).forEach((match) => findings.push({ rule: input.rule, ...match }))
  }
}

function assertSourceOrder(input: {
  rule: string
  file: string
  source: string
  before: string
  after: string
}): void {
  const beforeIndex = input.source.indexOf(input.before)
  const afterIndex = input.source.indexOf(input.after)
  if (beforeIndex >= 0 && afterIndex >= 0 && beforeIndex < afterIndex) {
    return
  }
  findings.push({
    rule: input.rule,
    file: input.file,
    line: 1,
    text: `${input.before} must appear before ${input.after}`
  })
}

function collectMatches(file: string, pattern: RegExp): Array<Omit<SourceFinding, 'rule'>> {
  const source = readFileSync(file, 'utf8')
  const lines = source.split(/\r?\n/)
  const matches: Array<Omit<SourceFinding, 'rule'>> = []
  for (let index = 0; index < lines.length; index += 1) {
    pattern.lastIndex = 0
    if (pattern.test(lines[index])) {
      matches.push({
        file: normalizeRepoPath(file),
        line: index + 1,
        text: lines[index].trim()
      })
    }
  }
  return matches
}

function collectSourceFiles(paths: string[]): string[] {
  const files: string[] = []
  for (const path of paths) {
    collectSourceFilesInto(path, files)
  }
  return files
}

function collectSourceFilesInto(path: string, files: string[]): void {
  const stats = statSync(path)
  if (stats.isFile()) {
    if (isSourceFile(path)) files.push(path)
    return
  }
  if (!stats.isDirectory()) return
  for (const entry of readdirSync(path, { withFileTypes: true })) {
    if (entry.isDirectory() && shouldSkipDirectory(entry.name)) continue
    collectSourceFilesInto(resolve(path, entry.name), files)
  }
}

function existingPaths(paths: string[]): string[] {
  return paths.filter((path) => existsSync(path))
}

function isSourceFile(path: string): boolean {
  return /\.(?:ts|tsx|js|jsx|vue)$/.test(path)
}

function shouldSkipDirectory(name: string): boolean {
  return new Set(['node_modules', 'dist', 'build', 'coverage', '.git', '.turbo']).has(name)
}

function normalizeRepoPath(path: string): string {
  return relative(repoRoot, resolve(path)).replace(/\\/g, '/')
}

function normalizeAllowedRepoPath(path: string): string {
  if (/^(?:[a-zA-Z]:[\\/]|[\\/])/.test(path)) {
    return normalizeRepoPath(path)
  }
  return path.replace(/\\/g, '/')
}
