#!/usr/bin/env node

// 测试单进程纪律守卫（规则源：docs/develop/后端测试分层规则.md §5「测试进程
// 纪律（硬性）」）。一次 go test 运行 = 恰好一个测试进程：本脚本扫描 backend-go
// 全部 *_test.go，命中 exec.Command / exec.CommandContext / os.Args[0] /
// os.Executable 子进程模式即失败；历史例外清单内的文件不允许新增命中行（防止
// 例外被滥用扩大，命中数只允许减少）。
// 用法：node scripts/test-process-isolation-regression.mjs

import { readdir, readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const BACKEND_GO_ROOT = path.join(REPO_ROOT, 'backend-go')

// 子进程孵化信号：出现任一模式即视为违例（含注释命中——测试代码里不应保留
// 可复制的该模式写法）。四个信号共同覆盖事故形态（重执行测试二进制、go
// build / go run、启动被测服务进程）与自复制入口（os.Args[0] / os.Executable）。
const SUBPROCESS_PATTERNS = [
  /exec\.Command\(/,
  /exec\.CommandContext\(/,
  /os\.Args\[0\]/,
  /os\.Executable\(/,
]

// 历史遗留例外（2026-09 前已存在；清单与 docs/develop/后端测试分层规则.md §5
// 第 6 条一致；数值 = 允许的最大命中行数，只减不增）。禁止新增模仿。
const LEGACY_EXCEPTION_BASELINE = {
  // gateway cmd boot / acceptance 系列
  'backend-go/projects/gateway/cmd/juhe-ai-gateway/w1_boot_arms_test.go': 1,
  'backend-go/projects/gateway/cmd/juhe-ai-gateway/w1_boot_chain_test.go': 1,
  'backend-go/projects/gateway/cmd/juhe-ai-gateway/w1_boot_cover_test.go': 3,
  'backend-go/projects/gateway/cmd/juhe-ai-gateway/w1_boot_owner_test.go': 1,
  'backend-go/projects/gateway/cmd/juhe-ai-gateway/w1_main_boot_test.go': 11,
  'backend-go/projects/gateway/cmd/juhe-ai-gateway/w1_main_tail_test.go': 3,
  'backend-go/projects/gateway/cmd/juhe-ai-gateway/w1_pg_wave_test.go': 1,
  'backend-go/projects/gateway/cmd/juhe-ai-gateway/w2_assembly_tail_test.go': 1,
  'backend-go/projects/gateway/cmd/juhe-ai-gateway/acceptance/fullchain_clients_test.go': 2,
  'backend-go/projects/gateway/cmd/juhe-ai-gateway/acceptance/harness_test.go': 2,
  'backend-go/projects/gateway/cmd/juhe-ai-gateway/acceptance/main_test.go': 1,
  // jobs cmd main arms / e2e 系列
  'backend-go/projects/jobs/cmd/juhe-ai-jobs/w13g8_cmd_main_arms_test.go': 2,
  'backend-go/projects/jobs/cmd/juhe-ai-jobs/w16d_cmd_main_arms_test.go': 2,
  'backend-go/projects/jobs/cmd/juhe-ai-jobs/wg_main_e2e_test.go': 2,
  // jobs internal 历史遗留（spawn 外部 node 进程读文件创建时间）
  'backend-go/projects/jobs/internal/runtimelog/runtimelog_test.go': 1,
}

export class TestProcessIsolationError extends Error {
  constructor(message) {
    super(message)
    this.name = 'TestProcessIsolationError'
  }
}

function fail(reason) {
  throw new TestProcessIsolationError(`test process isolation regression: ${reason}`)
}

function toPosix(relativePath) {
  return relativePath.split(path.sep).join('/')
}

async function listTestFiles(dir) {
  const entries = await readdir(dir, { withFileTypes: true })
  const files = []
  for (const entry of entries) {
    const fullPath = path.join(dir, entry.name)
    if (entry.isDirectory()) {
      files.push(...(await listTestFiles(fullPath)))
    } else if (entry.name.endsWith('_test.go')) {
      files.push(fullPath)
    }
  }
  return files.sort()
}

// findPatternLines 返回文件中命中任一子进程模式的行号与原文（1 起）。
export function findPatternLines(content) {
  const hits = []
  for (const [index, line] of content.split(/\r?\n/).entries()) {
    if (SUBPROCESS_PATTERNS.some(pattern => pattern.test(line))) {
      hits.push({ line: index + 1, text: line.trim() })
    }
  }
  return hits
}

// checkProcessIsolation 扫描测试文件并与例外基线比对，返回扫描摘要；
// 存在违例（非例外文件命中，或例外文件命中数超过基线）时抛出
// TestProcessIsolationError。rootDir 仅供测试注入，生产入口固定 backend-go。
export async function checkProcessIsolation(rootDir = BACKEND_GO_ROOT) {
  const testFiles = await listTestFiles(rootDir)
  if (testFiles.length === 0) {
    fail(`no *_test.go files found under ${rootDir}`)
  }

  const violations = []
  const expandedExceptions = []
  let exceptionFilesScanned = 0
  let exceptionHitLines = 0

  for (const file of testFiles) {
    const relativePath = toPosix(path.relative(REPO_ROOT, file))
    const content = await readFile(file, 'utf8')
    const hits = findPatternLines(content)
    if (hits.length === 0) continue

    const baseline = LEGACY_EXCEPTION_BASELINE[relativePath]
    if (baseline === undefined) {
      violations.push({ file: relativePath, hits })
      continue
    }
    exceptionFilesScanned += 1
    exceptionHitLines += hits.length
    if (hits.length > baseline) {
      expandedExceptions.push({ file: relativePath, baseline, actual: hits.length })
    }
  }

  if (violations.length > 0) {
    const details = violations
      .map(entry => `  ${entry.file}\n${entry.hits.map(hit => `    line ${hit.line}: ${hit.text}`).join('\n')}`)
      .join('\n')
    fail(`subprocess patterns are forbidden in go tests (see docs/develop/后端测试分层规则.md §5):\n${details}`)
  }
  if (expandedExceptions.length > 0) {
    const details = expandedExceptions
      .map(entry => `  ${entry.file}: baseline ${entry.baseline}, actual ${entry.actual}`)
      .join('\n')
    fail(`legacy exception files must not gain new subprocess call sites (only decreases allowed):\n${details}`)
  }

  const unknownExceptionFiles = Object.keys(LEGACY_EXCEPTION_BASELINE)
    .filter(key => !testFiles.some(file => toPosix(path.relative(REPO_ROOT, file)) === key))
  return {
    scannedFiles: testFiles.length,
    exceptionFilesScanned,
    missingExceptionFiles: unknownExceptionFiles,
    exceptionHitLines,
  }
}

async function main() {
  const summary = await checkProcessIsolation()
  const missingNote = summary.missingExceptionFiles.length > 0
    ? `, ${summary.missingExceptionFiles.length} exception file(s) absent from disk`
    : ''
  process.stdout.write(
    `test-process-isolation: OK — scanned ${summary.scannedFiles} *_test.go files under backend-go; `
    + `${summary.exceptionFilesScanned} legacy exception file(s) within baseline (${summary.exceptionHitLines} hit line(s))${missingNote}\n`
    + 'rule: docs/develop/后端测试分层规则.md §5 测试进程纪律（硬性）\n',
  )
}

const isDirectRun = process.argv[1]
  && path.resolve(process.argv[1]) === path.resolve(fileURLToPath(import.meta.url))

if (isDirectRun) {
  main().catch(error => {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`)
    process.exitCode = 1
  })
}
