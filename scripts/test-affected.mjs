#!/usr/bin/env node

// 按改动范围运行功能基线（test-strategy 功能基线轨道）。
//
// 规则：
//   - 默认只跑本次 git 改动命中的 Go 包（-short、-p 1、无外部依赖）；
//   - 前端改动跑 vitest 单测，api/ 改动额外跑前后端契约比对；
//   - cmd 重包不在默认 affected 内，打印慢基线入口；
//   - shared 包变更额外跑一跳反向依赖（直接 import 该包的项目包）；
//   - 文档/脚本变更不触发测试；
//   - 检测到 JUHE_AI_TEST_EXTERNAL=1 时拒绝运行（授权链路必须走 test:scenario）。
//
// 用法：
//   node scripts/test-affected.mjs [--base <git-ref>] [--dry-run]
//   --base 默认 HEAD（比较工作区 + 未跟踪文件）
//   --dry-run 只打印选择结果，不执行
//
// 证据输出：命令 / 退出码 / 结果 / 耗时 / 未运行项。

import { spawnSync } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { testProcessEnv } from './lib/test-process-env.mjs'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')

const MODULES = {
  gateway: {
    dir: 'backend-go/projects/gateway',
    importPrefix: 'github.com/huanminabc/juhe-ai/backend-go-gateway',
  },
  jobs: {
    dir: 'backend-go/projects/jobs',
    importPrefix: 'github.com/huanminabc/juhe-ai/backend-go-jobs',
  },
  maintenance: {
    dir: 'backend-go/projects/maintenance',
    importPrefix: 'github.com/huanminabc/juhe-ai/backend-go-maintenance',
  },
  'shared-platform': {
    dir: 'backend-go/shared/platform',
    importPrefix: 'github.com/huanminabc/juhe-ai/backend-go-platform',
  },
  'shared-contracts': {
    dir: 'backend-go/shared/contracts',
    importPrefix: 'github.com/huanminabc/juhe-ai/backend-go-contracts',
  },
}

function parseArgs(argv) {
  const args = { base: 'HEAD', dryRun: false, includeSlow: false }
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i]
    if (arg === '--') continue
    if (arg === '--dry-run') args.dryRun = true
    else if (arg === '--include-slow') args.includeSlow = true
    else if (arg === '--base') args.base = argv[++i] ?? 'HEAD'
    else if (arg.startsWith('--base=')) args.base = arg.slice('--base='.length)
    else throw new Error(`未知参数: ${arg}`)
  }
  return args
}

function gitOutput(args) {
  const result = spawnSync('git', args, { cwd: REPO_ROOT, encoding: 'utf8', shell: false })
  if (result.error) throw result.error
  if (result.status !== 0) throw new Error(`git ${args.join(' ')} 失败: ${(result.stderr ?? '').trim()}`)
  return result.stdout
}

function changedFiles(base) {
  const tracked = gitOutput(['diff', '--name-only', '--diff-filter=ACMRT', base])
  const untracked = gitOutput(['ls-files', '--others', '--exclude-standard'])
  return [...new Set(`${tracked}\n${untracked}`.split(/\r?\n/).map((line) => line.trim()).filter(Boolean))]
}

function moduleFor(file) {
  const normalized = file.replaceAll('\\', '/')
  for (const [name, module] of Object.entries(MODULES)) {
    if (normalized.startsWith(`${module.dir}/`) || normalized === module.dir) return { name, ...module }
  }
  return null
}

function packageSpec(moduleDirAbs, file) {
  // Go 包以目录为单位；测试文件与生产文件同包。
  const dir = path.dirname(file)
  const rel = path.relative(moduleDirAbs, path.resolve(REPO_ROOT, dir)).replaceAll('\\', '/')
  if (rel === '' || rel === '.') return './'
  return `./${rel}`
}

function isCmdPackage(spec) {
  return spec.startsWith('./cmd/')
}

function listPackagesWithDeps(moduleDirAbs) {
  const result = spawnSync('go', ['list', '-f', '{{.ImportPath}}|{{join .Deps ","}}', './...'], {
    cwd: moduleDirAbs,
    encoding: 'utf8',
    shell: false,
  })
  if (result.error) throw result.error
  if (result.status !== 0) throw new Error(`go list 失败（${moduleDirAbs}）: ${(result.stderr ?? '').trim()}`)
  return result.stdout
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line) => {
      const [importPath, deps] = line.split('|')
      return { importPath, deps: deps ? deps.split(',') : [] }
    })
}

function collectSharedImporters(changedImports) {
  const selected = new Map()
  for (const [name, module] of Object.entries(MODULES)) {
    if (name.startsWith('shared-')) continue
    const moduleDirAbs = path.join(REPO_ROOT, module.dir)
    const packages = listPackagesWithDeps(moduleDirAbs)
    for (const pkg of packages) {
      if (!pkg.importPath.startsWith(`${module.importPrefix}/`)) continue
      if (changedImports.some((changed) => pkg.deps.includes(changed))) {
        const rel = pkg.importPath.slice(module.importPrefix.length)
        selected.set(`${name}:${rel}`, { module: name, spec: `.${rel}` })
      }
    }
  }
  return selected
}

function runCommand(label, command, args, cwd, env = {}) {
  const started = Date.now()
  process.stdout.write(`\n命令：${command} ${args.join(' ')}\n目录：${cwd}\n`)
  const result = spawnSync(command, args, {
    cwd,
    stdio: 'inherit',
    env: { ...testProcessEnv(), ...env },
    shell: process.platform === 'win32' && command === 'pnpm',
  })
  const durationMs = Date.now() - started
  const code = result.error ? 1 : (result.status ?? 1)
  const ok = !result.error && code === 0
  process.stdout.write(`退出码：${code}\n结果：${ok ? 'pass' : 'fail'}\n耗时：${(durationMs / 1000).toFixed(1)}s\n`)
  if (result.error) process.stderr.write(`启动失败：${result.error.message}\n`)
  return { ok, code, durationMs }
}

function main() {
  if (process.env.JUHE_AI_TEST_EXTERNAL === '1') {
    process.stderr.write('拒绝运行：功能基线必须是纯 mock 链路，请取消 JUHE_AI_TEST_EXTERNAL 或改用 pnpm test:scenario。\n')
    process.exitCode = 2
    return
  }

  const args = parseArgs(process.argv.slice(2))
  const files = changedFiles(args.base)
  if (files.length === 0) {
    process.stdout.write('未检测到改动（工作区与未跟踪文件均为空），未运行测试。\n')
    return
  }

  const goRuns = new Map() // module -> Set(spec)
  const slowPackages = new Set()
  const sharedChangedImports = new Set()
  let frontendChanged = false
  let frontendApiChanged = false
  const ignored = []

  for (const file of files) {
    const normalized = file.replaceAll('\\', '/')
    const module = moduleFor(normalized)
    if (module) {
      if (!normalized.endsWith('.go')) {
        // go.mod/go.sum/go.work 变更无法精确到包：提示跑全量基线。
        if (/go\.(mod|sum|work)$/.test(normalized)) slowPackages.add(`${module.name}:全部（依赖文件变更）`)
        continue
      }
      const moduleDirAbs = path.join(REPO_ROOT, module.dir)
      const spec = packageSpec(moduleDirAbs, normalized)
      if (spec.startsWith('..')) continue
      if (module.name.startsWith('shared-')) {
        const rel = path.relative(path.join(REPO_ROOT, module.dir), path.resolve(REPO_ROOT, path.dirname(normalized))).replaceAll('\\', '/')
        const importPath = rel === '' ? module.importPrefix : `${module.importPrefix}/${rel}`
        sharedChangedImports.add(importPath)
        if (!goRuns.has(module.name)) goRuns.set(module.name, new Set())
        goRuns.get(module.name).add(spec)
      } else if (isCmdPackage(spec)) {
        slowPackages.add(`${module.name}:${spec}`)
      } else {
        if (!goRuns.has(module.name)) goRuns.set(module.name, new Set())
        goRuns.get(module.name).add(spec)
      }
      continue
    }
    if (normalized.startsWith('frontend/')) {
      frontendChanged = true
      if (normalized.startsWith('frontend/src/api/')) frontendApiChanged = true
      continue
    }
    ignored.push(normalized)
  }

  // shared 变更：追加一跳反向依赖（直接 import 的项目包）。
  if (sharedChangedImports.size > 0) {
    try {
      const importers = collectSharedImporters([...sharedChangedImports])
      for (const { module, spec } of importers.values()) {
        // cmd 重包不进默认 affected（与直接源码改动同一规则），只登记慢基线。
        if (isCmdPackage(spec)) {
          slowPackages.add(`${module}:${spec}`)
          continue
        }
        if (!goRuns.has(module)) goRuns.set(module, new Set())
        goRuns.get(module).add(spec)
      }
    } catch (error) {
      process.stderr.write(`反向依赖扫描失败，按模块全量基线处理：${error.message}\n`)
      for (const module of ['gateway', 'jobs', 'maintenance']) {
        if (!goRuns.has(module)) goRuns.set(module, new Set(['./internal/...']))
      }
    }
  }

  process.stdout.write('改动文件：\n')
  for (const file of files) process.stdout.write(`  ${file}\n`)

  if (goRuns.size === 0 && !frontendChanged && slowPackages.size === 0) {
    process.stdout.write('\n未命中需要运行的测试（仅文档/脚本变更）。\n')
    return
  }

  if (args.dryRun) {
    process.stdout.write('\n[dry-run] 计划运行：\n')
    for (const [module, specs] of goRuns) {
      process.stdout.write(`  ${module}: go test -short -count=1 -p 1 -> ${[...specs].join(' ')}\n`)
    }
    if (frontendChanged) process.stdout.write('  frontend: pnpm --filter juhe-ai-frontend test:unit\n')
    if (frontendApiChanged) process.stdout.write('  frontend: pnpm --filter juhe-ai-frontend test:api-contract\n')
    for (const slow of slowPackages) process.stdout.write(`  [慢基线，未运行] ${slow}\n`)
    return
  }

  let allOk = true
  for (const [module, specs] of goRuns) {
    const moduleConfig = MODULES[module]
    const moduleDirAbs = path.join(REPO_ROOT, moduleConfig.dir)
    const result = runCommand(
      module,
      'go',
      ['test', '-short', '-count=1', '-p', '1', '-timeout', '10m', ...specs],
      moduleDirAbs,
    )
    if (!result.ok) allOk = false
  }

  if (frontendChanged) {
    const unit = runCommand('frontend:unit', 'pnpm', ['--filter', 'juhe-ai-frontend', 'test:unit'], REPO_ROOT)
    if (!unit.ok) allOk = false
    if (frontendApiChanged) {
      const contract = runCommand('frontend:api-contract', 'pnpm', ['--filter', 'juhe-ai-frontend', 'test:api-contract'], REPO_ROOT)
      if (!contract.ok) allOk = false
    }
  }

  if (slowPackages.size > 0) {
    process.stdout.write('\n未运行（慢基线）：\n')
    for (const slow of slowPackages) process.stdout.write(`  ${slow}\n`)
    process.stdout.write('  运行入口：pnpm test:baseline:slow\n')
  }
  if (ignored.length > 0) {
    process.stdout.write(`\n未触发测试的非代码改动：${ignored.length} 个文件（文档/配置/脚本）。\n`)
  }
  process.stdout.write('\n未运行：全量功能基线（pnpm test:baseline）、慢基线（pnpm test:baseline:slow）、场景测试（pnpm test:scenario）、性能测试。\n')
  process.exitCode = allOk ? 0 : 1
}

main()
