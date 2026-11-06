#!/usr/bin/env node

// 功能基线入口（test-strategy 功能基线轨道）。串行执行各 Go 模块的 -short
// 单测与前端 vitest 单测，全程进程内 mock、免授权，不连接任何外部依赖。
//
// 分层（2026-11-06 重建）：
//   --tier=fast（默认）日常全量功能基线：internal 快包 + 前端 unit + 契约比对；
//   --tier=slow        慢基线：单包 >10s 的重包（gatewaydispatch/schema 等），
//                      纯 mock、不含 cmd/acceptance；
//   --tier=all         fast + slow；仍不包含真实 PG/上游（属场景测试）。
// 按改动范围的最小基线用 `pnpm test:affected`（scripts/test-affected.mjs）。
//
// 运行时先 `go list` 全量发现包，再按 include/exclude 后缀过滤：新增包默认
// 进入 fast 链路，只有显式列入 exclude 才被裁到 slow；go list 失败即该阶段
// 失败，不静默降级。
// 规则源：docs/develop/后端测试分层规则.md——同一时刻只允许一个 go test
// 进程：本脚本各阶段顺序 await 串行执行，go test 统一带 -p 1 与显式
// -timeout；与授权链路（acceptance / 真实 PG smoke / docker 隔离回归）互斥：
// 检测到 JUHE_AI_TEST_EXTERNAL=1 时拒绝运行。
// 用法：node scripts/test-daily-regression.mjs [--tier=fast|slow|all]

import { spawn } from 'node:child_process'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { testProcessEnv } from './lib/test-process-env.mjs'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')

// 总墙钟预算（毫秒）：超出即硬失败（退出码 1）。单阶段另有各自 -timeout 保护。
// 2026-11-06 复核：原 300s 预算在冷缓存机器上会在 gateway 阶段被截断（实测
// 300.6s 被杀，后续阶段未执行）；首次 600s 预算下 gateway 冷编译 382.8s，
// 全量 warm 实测 544.8s，余量不足以覆盖冷缓存重编译，故 fast 提到 15 分钟。
// 日常改动范围基线仍由 test:affected 控制在 5 分钟内。
const TIER_BUDGETS_MS = {
  fast: 900000,
  slow: 900000,
  all: 1800000,
}

// 各 go test 阶段的 -timeout 值，集中放在这里便于按实测调参。行尾为
// 2026-10-27 验收轮实测（warm）；2026-11-06 冷缓存复核后上调 fast 的
// gateway 阶段到 480s，避免 74 包全量在 240s 被误杀。
const GO_STAGE_TIMEOUTS = {
  gateway: '480s',           // 裁剪后 warm 148.0s；冷缓存可达 300s+
  jobs: '120s',              // 裁剪后 23.0s
  maintenance: '120s',       // 裁剪后 8.9s
  'shared-contracts': '120s', // 全量 1.0s
  'shared-platform': '120s',  // 全量 27.0s
  'gateway-slow': '900s',
  'jobs-slow': '300s',
  'maintenance-slow': '300s',
}

// 日常回归范围（裁剪版）。裁剪规则：单包 warm -short 实测 >10s 的包列入
// exclude（每项标注实测秒数），去向为「扩展 mock 回归」（./internal/... 全量、
// 纯 mock、免授权、按需执行，见文件头注释与 docs/develop/后端测试分层规则.md
// §3.6）。exclude 为导入路径后缀，endsWith 精确匹配（含 '/internal' 前缀段，
// 不会误伤其他模块或同名前缀包（如 '/internal/chat' 不会匹配 '/internal/chat/chatassets'）。
// 不在清单的重量级 / 授权项及原因：gateway|jobs cmd 组合根全量与 acceptance
// 冒烟属装配验收（gateway cmd 包全量基线约 691s，会独占预算）；真实 PG smoke
// 需 JUHE_AI_TEST_PG_ADMIN_URL 授权；docker 隔离回归属环境级验收——均属授权
// 链路，不入日常（见 docs/develop/后端测试分层规则.md）。
const FAST_STAGES = [
  {
    name: 'gateway:internal',
    cwd: 'backend-go/projects/gateway',
    goTimeout: GO_STAGE_TIMEOUTS.gateway,
    listPattern: './internal/...',
    // gateway internal 是网关业务逻辑单测主体；下列 9 包单包 >10s，移入扩展 mock 回归。
    exclude: [
      '/internal/gatewaydispatch',            // 56.4s → 扩展 mock 回归
      '/internal/business/key_model_runtime', // 28.2s → 扩展 mock 回归
      '/internal/auditlog',                   // 20.5s → 扩展 mock 回归
      '/internal/operationlog',               // 14.8s → 扩展 mock 回归
      '/internal/gatewayaccounteffects',      // 13.9s → 扩展 mock 回归
      '/internal/gatewaysession',             // 12.9s → 扩展 mock 回归
      '/internal/gatewaycircuit',             // 12.8s → 扩展 mock 回归
      '/internal/gatewayquota',               // 12.6s → 扩展 mock 回归
      '/internal/gatewayobs',                 // 11.7s → 扩展 mock 回归
    ],
  },
  {
    name: 'jobs:internal',
    cwd: 'backend-go/projects/jobs',
    goTimeout: GO_STAGE_TIMEOUTS.jobs,
    listPattern: './internal/...',
    // jobs internal 是后台任务单测主体；下列 2 包单包 >10s，移入扩展 mock 回归。
    exclude: [
      '/internal/speedfirstrepo',             // 40.1s → 扩展 mock 回归
      '/internal/usagewriter',                // 12.3s → 扩展 mock 回归
    ],
  },
  {
    name: 'maintenance:internal',
    cwd: 'backend-go/projects/maintenance',
    goTimeout: GO_STAGE_TIMEOUTS.maintenance,
    listPattern: './internal/...',
    // maintenance internal 是 schema/seed 单测；真实 PG 授权门未开时由 Go 侧跳过。
    exclude: [
      '/internal/schema',                     // 40.2s → 扩展 mock 回归
    ],
  },
  {
    name: 'shared-contracts:all',
    cwd: 'backend-go/shared/contracts',
    command: 'go',
    args: ['test', '-short', '-count=1', '-p', '1', '-timeout', GO_STAGE_TIMEOUTS['shared-contracts'], './...'],
    // shared/contracts 是独立 go.mod 模块，不在上面三个项目的 ./internal/... 内，
    // 单独跑不构成重复执行；根包为连接器注册表等纯数据契约单测（验收轮 1.0s）。
  },
  {
    name: 'shared-platform:all',
    cwd: 'backend-go/shared/platform',
    command: 'go',
    args: ['test', '-short', '-count=1', '-p', '1', '-timeout', GO_STAGE_TIMEOUTS['shared-platform'], './...'],
    // shared/platform 同为独立模块（平台工具族，验收轮 27.0s）；真实 PG 冒烟在
    // 未设授权变量时由 Go 侧 t.Skip 跳过，不会触碰 PG。
  },
  {
    name: 'frontend:unit',
    cwd: '.',
    command: 'pnpm',
    args: ['--filter', 'juhe-ai-frontend', 'test:unit'],
    // 前端 vitest 单测全部进程内 mock（2026-10-27 验收轮实测 94 个文件 / 1114 用例 / 8.9s）。
  },
  {
    name: 'frontend:api-contract',
    cwd: '.',
    command: 'pnpm',
    args: ['--filter', 'juhe-ai-frontend', 'test:api-contract'],
    // 契约测试属功能基线（后端分层规则 §2）：只读静态比对前端调用面与 Go 注册面。
  },
]

// 慢基线（--tier=slow）：单包 warm -short >10s 的纯 mock 包。与 fast 互斥
// 执行；不包含 cmd / acceptance（组合根/子进程属场景测试）。
const SLOW_STAGES = [
  {
    name: 'gateway:internal-slow',
    cwd: 'backend-go/projects/gateway',
    goTimeout: GO_STAGE_TIMEOUTS['gateway-slow'],
    listPattern: './internal/...',
    include: [
      '/internal/gatewaydispatch',
      '/internal/business/key_model_runtime',
      '/internal/auditlog',
      '/internal/operationlog',
      '/internal/gatewayaccounteffects',
      '/internal/gatewaysession',
      '/internal/gatewaycircuit',
      '/internal/gatewayquota',
      '/internal/gatewayobs',
    ],
  },
  {
    name: 'jobs:internal-slow',
    cwd: 'backend-go/projects/jobs',
    goTimeout: GO_STAGE_TIMEOUTS['jobs-slow'],
    listPattern: './internal/...',
    include: ['/internal/speedfirstrepo', '/internal/usagewriter'],
  },
  {
    name: 'maintenance:internal-slow',
    cwd: 'backend-go/projects/maintenance',
    goTimeout: GO_STAGE_TIMEOUTS['maintenance-slow'],
    listPattern: './internal/...',
    include: ['/internal/schema'],
  },
]

function log(line) {
  process.stdout.write(`${line}\n`)
}

function formatMs(ms) {
  return `${(ms / 1000).toFixed(1)}s`
}

// killTree 终止子进程树。Windows 下 shell:true 会派生 cmd -> go/pnpm 两层进程，
// 必须按树杀（taskkill /T），否则只杀掉 cmd 外壳，go test 仍在后台运行，违反
// 「同一时刻只允许一个 go test 进程」纪律。
function killTree(child) {
  if (child.pid === undefined || child.exitCode !== null || child.signalCode !== null) {
    return
  }
  if (process.platform === 'win32') {
    spawn('taskkill', ['/pid', String(child.pid), '/T', '/F'], { stdio: 'ignore' })
  } else {
    // 非 Windows 未做进程树处理，仅 SIGTERM 外壳，孙进程可能残留；脚本当前
    // 只在 Windows 上使用与验证。
    child.kill('SIGTERM')
  }
}

// spawnChild 运行单个子进程：输出直接继承本进程 stdout/stderr（不吞错误原文；
// captureStdout 时仅捕获 stdout、stderr 仍继承）。remainingMs 内未结束则终止
// 子进程树并以 killedByBudget 标记返回。
function spawnChild({ label, command, args, cwdAbs, remainingMs, captureStdout = false }) {
  return new Promise((resolve) => {
    const child = spawn(command, args, {
      cwd: cwdAbs,
      stdio: captureStdout ? ['ignore', 'pipe', 'inherit'] : 'inherit',
      // Windows 下 go / pnpm 实际是 .exe / .cmd，shell:true 交由 shell 解析。
      shell: process.platform === 'win32',
      env: testProcessEnv(),
    })
    let stdout = ''
    if (captureStdout && child.stdout) {
      child.stdout.setEncoding('utf8')
      child.stdout.on('data', chunk => { stdout += chunk })
    }
    let settled = false
    let killedByBudget = false
    const budgetTimer = setTimeout(() => {
      killedByBudget = true
      log(`[预算超时] ${label} 已耗尽总墙钟预算剩余额度，终止子进程树`)
      killTree(child)
    }, Math.max(remainingMs, 0))
    const finish = (patch) => {
      if (settled) return
      settled = true
      clearTimeout(budgetTimer)
      resolve({ killedByBudget, code: 1, stdout, ...patch })
    }
    child.on('error', error => finish({ error }))
    child.on('close', code => finish({ code: code ?? 1 }))
  })
}

// runStage 执行单个阶段并返回子进程结果。带 listPattern 的阶段先 go list 全量
// 发现包：fast 阶段用 exclude（单包 >10s 的慢包移出），slow 阶段用 include
// （只跑显式登记的慢包）；go list 非零退出、预算超时或无法启动时该阶段直接
// 失败，不静默降级。
async function runStage(stage, deadline) {
  const cwdAbs = path.join(REPO_ROOT, stage.cwd)

  let testArgs = stage.args
  if (stage.listPattern !== undefined) {
    const list = await spawnChild({
      label: `${stage.name}（go list）`,
      command: 'go',
      args: ['list', stage.listPattern],
      cwdAbs,
      remainingMs: deadline - Date.now(),
      captureStdout: true,
    })
    if (list.error !== undefined || list.killedByBudget || list.code !== 0) {
      const reason = list.killedByBudget
        ? '总墙钟预算超时'
        : (list.error !== undefined ? `无法启动（${list.error.message}）` : `退出码 ${list.code}`)
      log(`[包发现失败] ${stage.name}: go list ${stage.listPattern} ${reason}，不静默降级`)
      return list
    }
    const allPackages = list.stdout.split(/\r?\n/).map(line => line.trim()).filter(Boolean)
    let keptPackages
    if (stage.include !== undefined) {
      keptPackages = allPackages.filter(pkg => stage.include.some(suffix => pkg.endsWith(suffix)))
    } else {
      keptPackages = allPackages.filter(pkg => !stage.exclude.some(suffix => pkg.endsWith(suffix)))
    }
    const mode = stage.include !== undefined ? `include 选中 ${keptPackages.length}` : `exclude 裁剪 ${allPackages.length - keptPackages.length}`
    log(`[包发现] ${stage.name}: ${stage.listPattern} 共 ${allPackages.length} 包，${mode} 包，执行 ${keptPackages.length} 包`)
    testArgs = ['test', '-short', '-count=1', '-p', '1', '-timeout', stage.goTimeout, ...keptPackages]
    if (deadline - Date.now() <= 0) {
      log(`[预算超时] ${stage.name} 包发现后剩余预算已耗尽，跳过执行`)
      return { killedByBudget: true, code: 1, stdout: '' }
    }
  }

  return spawnChild({
    label: stage.name,
    command: stage.command ?? 'go',
    args: testArgs,
    cwdAbs,
    remainingMs: deadline - Date.now(),
  })
}

function printTimingTable(records, totalMs, budgetMs) {
  log('')
  log('各阶段耗时：')
  for (const record of records) {
    const mark = record.killedByBudget
      ? '（预算超时终止）'
      : (record.ok ? '' : `（失败，退出码 ${record.code}）`)
    log(`  ${record.stage.name.padEnd(24)}${formatMs(record.durationMs).padStart(10)}${mark}`)
  }
  log(`  ${'总计'.padEnd(24)}${formatMs(totalMs).padStart(10)}（预算 ${formatMs(budgetMs)}）`)
}

async function main() {
  // 链路守卫：功能基线必须是纯 mock 链路，与场景（外部依赖）链路互斥。
  if (process.env.JUHE_AI_TEST_EXTERNAL === '1') {
    log('拒绝运行：检测到 JUHE_AI_TEST_EXTERNAL=1。功能基线必须是纯 mock 链路，请取消该授权变量或改用 pnpm test:scenario。')
    process.exitCode = 2
    return
  }

  const tierArg = process.argv.find(arg => arg.startsWith('--tier='))
  const tier = tierArg ? tierArg.slice('--tier='.length) : 'fast'
  const budgetMs = TIER_BUDGETS_MS[tier]
  if (budgetMs === undefined) {
    log(`未知 tier: ${tier}（可选 fast|slow|all）`)
    process.exitCode = 2
    return
  }
  const stages = tier === 'slow'
    ? SLOW_STAGES
    : (tier === 'all' ? [...FAST_STAGES, ...SLOW_STAGES] : FAST_STAGES)
  log(`功能基线 tier=${tier}（预算 ${formatMs(budgetMs)}，${stages.length} 个阶段）`)

  const chainStartedAt = Date.now()
  const records = []

  for (const stage of stages) {
    const remainingMs = budgetMs - (Date.now() - chainStartedAt)
    if (remainingMs <= 0) {
      log('')
      log('功能基线超预算：剩余预算已耗尽，跳过全部后续阶段。')
      printTimingTable(records, Date.now() - chainStartedAt, budgetMs)
      process.exitCode = 1
      return
    }

    log(`[阶段开始] ${stage.name}`)
    const stageStartedAt = Date.now()
    const result = await runStage(stage, stageStartedAt + remainingMs)
    const durationMs = Date.now() - stageStartedAt
    const ok = !result.killedByBudget && result.code === 0 && result.error === undefined
    records.push({ stage, durationMs, ...result, ok })
    log(`[阶段结束] ${stage.name} 耗时 ${formatMs(durationMs)}`)

    if (!ok) {
      const reason = result.killedByBudget
        ? '总墙钟预算超时'
        : (result.error !== undefined ? `无法启动（${result.error.message}）` : `退出码 ${result.code}`)
      log('')
      log(`功能基线失败：阶段 ${stage.name} 未通过（${reason}），后续阶段不再执行。`)
      printTimingTable(records, Date.now() - chainStartedAt, budgetMs)
      process.exitCode = result.killedByBudget ? 1 : result.code
      return
    }
  }

  const totalMs = Date.now() - chainStartedAt
  printTimingTable(records, totalMs, budgetMs)
  if (totalMs > budgetMs) {
    log('')
    log(`功能基线超预算：总墙钟 ${formatMs(totalMs)} 超过预算 ${formatMs(budgetMs)}。`)
    process.exitCode = 1
    return
  }
  log('')
  log(`功能基线通过：tier=${tier}，总耗时 ${formatMs(totalMs)}，预算内（≤ ${formatMs(budgetMs)}），退出码 0。`)
}

const isDirectRun = process.argv[1]
  && path.resolve(process.argv[1]) === path.resolve(fileURLToPath(import.meta.url))

if (isDirectRun) {
  main().catch(error => {
    process.stderr.write(`${error instanceof Error ? (error.stack ?? error.message) : String(error)}\n`)
    process.exitCode = 1
  })
}
