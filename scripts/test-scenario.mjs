#!/usr/bin/env node

// 场景测试入口（test-strategy 场景轨道）：真实依赖、按 flow 单独执行。
//
// 与功能基线互斥：必须显式授权 JUHE_AI_TEST_EXTERNAL=1；未授权直接退出 2。
//
// flows：
//   pg             真实 PostgreSQL（pgtestkit 隔离库；自动发现含 pgtestkit 的测试包）
//   acceptance     真实组合根/子进程验收（gateway acceptance，JUHE_AI_E2E_FULLCHAIN=1）
//   frontend-smoke 前端打真实 Go 后端的 plan0081 smoke（需 JUHE_REAL_GO_MANAGEMENT_BASE_URL/COOKIE）
//
// 用法：
//   JUHE_AI_TEST_EXTERNAL=1 node scripts/test-scenario.mjs --flow=pg
//   JUHE_AI_TEST_EXTERNAL=1 node scripts/test-scenario.mjs --flow=acceptance
//   node scripts/test-scenario.mjs --list
//
// 证据输出：命令 / 退出码 / 结果 / 耗时 / 前置条件缺失项。

import { spawnSync } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { testProcessEnv } from './lib/test-process-env.mjs'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')

const MODULES = {
  gateway: { dir: 'backend-go/projects/gateway', importPrefix: 'github.com/huanminabc/juhe-ai/backend-go-gateway' },
  jobs: { dir: 'backend-go/projects/jobs', importPrefix: 'github.com/huanminabc/juhe-ai/backend-go-jobs' },
  maintenance: { dir: 'backend-go/projects/maintenance', importPrefix: 'github.com/huanminabc/juhe-ai/backend-go-maintenance' },
  'shared-platform': { dir: 'backend-go/shared/platform', importPrefix: 'github.com/huanminabc/juhe-ai/backend-go-platform' },
  'shared-contracts': { dir: 'backend-go/shared/contracts', importPrefix: 'github.com/huanminabc/juhe-ai/backend-go-contracts' },
}

function parseArgs(argv) {
  const args = { flow: '', list: false }
  for (const arg of argv) {
    if (arg === '--') continue
    if (arg === '--list') args.list = true
    else if (arg.startsWith('--flow=')) args.flow = arg.slice('--flow='.length)
    else if (arg === '--flow') args.flow = ''
    else throw new Error(`未知参数: ${arg}`)
  }
  return args
}

function walkFiles(dir, predicate, out = []) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) {
      if (entry.name === '.git' || entry.name === 'node_modules') continue
      walkFiles(full, predicate, out)
    } else if (predicate(full)) {
      out.push(full)
    }
  }
  return out
}

function discoverPgPackages() {
  const goRoot = path.join(REPO_ROOT, 'backend-go')
  const files = walkFiles(goRoot, (file) => file.endsWith('_test.go'))
  const groups = new Map()
  for (const file of files) {
    const content = fs.readFileSync(file, 'utf8')
    if (!content.includes('pgtestkit')) continue
    for (const [name, module] of Object.entries(MODULES)) {
      const moduleDirAbs = path.join(REPO_ROOT, module.dir)
      const rel = path.relative(moduleDirAbs, path.dirname(file)).replaceAll('\\', '/')
      if (rel.startsWith('..')) continue
      const spec = rel === '' ? './' : `./${rel}`
      if (!groups.has(name)) groups.set(name, new Set())
      groups.get(name).add(spec)
      break
    }
  }
  return groups
}

function run(label, command, args, cwd, env = {}) {
  const started = Date.now()
  console.log(`\n命令：${command} ${args.join(' ')}`)
  console.log(`目录：${cwd}`)
  const result = spawnSync(command, args, {
    cwd,
    stdio: 'inherit',
    env: { ...testProcessEnv(), ...env },
    shell: process.platform === 'win32' && command === 'pnpm',
  })
  const durationMs = Date.now() - started
  const code = result.error ? 1 : (result.status ?? 1)
  console.log(`退出码：${code}`)
  console.log(`结果：${code === 0 ? 'pass' : 'fail'}`)
  console.log(`耗时：${(durationMs / 1000).toFixed(1)}s`)
  if (result.error) console.error(`启动失败：${result.error.message}`)
  return code === 0
}

function main() {
  const args = parseArgs(process.argv.slice(2))
  if (args.list) {
    console.log('可用 flow：')
    console.log('  pg             真实 PostgreSQL（pgtestkit 隔离库）')
    console.log('  acceptance     组合根/子进程验收（mock 上游）')
    console.log('  frontend-smoke 前端打真实 Go 后端 smoke')
    return
  }
  if (args.flow === '') {
    console.error('缺少 --flow（可选 pg|acceptance|frontend-smoke；--list 查看说明）')
    process.exitCode = 2
    return
  }
  if (process.env.JUHE_AI_TEST_EXTERNAL !== '1') {
    console.error('拒绝运行：场景测试必须显式授权。请设置 JUHE_AI_TEST_EXTERNAL=1 后重试。')
    process.exitCode = 2
    return
  }

  let ok = true
  if (args.flow === 'pg') {
    const groups = discoverPgPackages()
    if (groups.size === 0) {
      console.error('未发现包含 pgtestkit 的测试包。')
      process.exitCode = 1
      return
    }
    for (const [name, specs] of groups) {
      const moduleDirAbs = path.join(REPO_ROOT, MODULES[name].dir)
      console.log(`\n[flow=pg] ${name}: ${[...specs].join(' ')}`)
      if (!run(`${name}:pg`, 'go', ['test', '-count=1', '-p', '1', '-timeout', '20m', ...specs], moduleDirAbs)) {
        ok = false
      }
    }
    console.log('\n未运行：acceptance / frontend-smoke / 性能测试。')
  } else if (args.flow === 'acceptance') {
    const cwd = path.join(REPO_ROOT, MODULES.gateway.dir)
    console.log('[flow=acceptance] 需要 mock 上游与子进程 harness；真实上游场景另需 JUHE_AI_E2E_REAL=1 + JUHE_AI_E2E_PROD_SECRET。')
    ok = run('acceptance', 'go', ['test', './cmd/juhe-ai-gateway/acceptance', '-count=1', '-p', '1', '-timeout', '20m'], cwd, {
      JUHE_AI_E2E_FULLCHAIN: '1',
    })
    console.log('\n未运行：pg / frontend-smoke / 性能测试。')
  } else if (args.flow === 'frontend-smoke') {
    const missing = ['JUHE_REAL_GO_MANAGEMENT_BASE_URL', 'JUHE_REAL_GO_MANAGEMENT_COOKIE'].filter(
      (key) => !process.env[key],
    )
    if (missing.length > 0) {
      console.error(`缺少前端 smoke 前置环境：${missing.join(', ')}（脚本级门控，与 JUHE_AI_TEST_EXTERNAL 不互替）`)
      process.exitCode = 2
      return
    }
    ok = run('frontend-smoke', 'pnpm', ['--filter', 'juhe-ai-frontend', 'smoke:plan0081-real-go-management'], REPO_ROOT)
    console.log('\n未运行：pg / acceptance / 性能测试。')
  } else {
    console.error(`未知 flow: ${args.flow}`)
    process.exitCode = 2
    return
  }

  process.exitCode = ok ? 0 : 1
}

main()
