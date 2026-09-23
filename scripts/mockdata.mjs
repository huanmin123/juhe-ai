#!/usr/bin/env node

// 造数派生缓存一次性重建编排：本地隔离数据根上顺序执行
//   1) juhe-ai-maintenance --ensure-schema --seed（幂等建库/种子）
//   2) juhe-ai-maintenance --mockdata（造数）
//   3) juhe-ai-jobs --run-jobs-once=<SQLite 适用任务集合>（派生聚合一次性重建）
//   4) juhe-ai-maintenance --verify-mockdata-coverage（只读覆盖校验）
// 任一步非零退出即停，日志经 stdio inherit 透传子进程。
//
// env 解析与「检测 gateway/jobs 是否仍在运行」复用 scripts/dev.mjs 的既有做
// 法（仓库根 .env + JUHE_AI_ENV_FILE overlay、JUHE_AI_DATA_DIR/JUHE_AI_LOG_DIR
// 缺省 .local/dev/{data,logs}、health 端口探测）；子进程用 go run 直跑项目
// 入口（dev.mjs startGoProject 同款，Windows 下 go.exe 经 PATH 解析无需
// shell）。本脚本只用 Node 内置模块。

import { spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { dirname, isAbsolute, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { tmpdir } from 'node:os'
import { createRequire } from 'node:module'

const scriptRoot = dirname(fileURLToPath(import.meta.url))
const projectRoot = resolve(scriptRoot, '..')
const backendGoRoot = resolve(projectRoot, 'backend-go')
const devDataRoot = resolve(projectRoot, '.local', 'dev', 'data')
const devLogRoot = resolve(projectRoot, '.local', 'dev', 'logs')

// MockdataUsageError：参数/环境类用法错误，进程以 exit 2 收敛（对齐 Go
// maintenance 的「usage error 2」约定）。
export class MockdataUsageError extends Error {}

// scheduledEntriesSnapshot 镜像 backend-go/projects/jobs/internal/jobregistry/
// registry.go ScheduledEntries() 的 (JobName, GoStatus) 快照（注册表是唯一权
// 威；Go 侧注册表变化时需同步本表）。go-equivalent 的任务由其他组件/进程接
// 管，不在 worker 组合根 wiredTasks 清单里。
const scheduledEntriesSnapshot = [
  { name: 'system-metrics-sample', status: 'go-equivalent' },
  { name: 'system-metrics-trend-windows-refresh', status: 'go-wired' },
  { name: 'usage-stats-aggregation', status: 'go-wired' },
  { name: 'usage-hot-window-refresh', status: 'go-wired' },
  { name: 'client-ip-stats-aggregation', status: 'go-wired' },
  { name: 'group-account-stats-refresh', status: 'go-wired' },
  { name: 'usage-rank-snapshots-refresh', status: 'go-wired' },
  { name: 'ai-performance-summary-windows-refresh', status: 'go-wired' },
  { name: 'usage-overview-windows-refresh', status: 'go-wired' },
  { name: 'usage-scope-range-windows-refresh', status: 'go-wired' },
  { name: 'authorization-usage-range-windows-refresh', status: 'go-wired' },
  { name: 'usage-quota-hourly-windows-refresh', status: 'go-wired' },
  { name: 'usage-stats-consistency-check', status: 'go-wired' },
  { name: 'background-task-run-reconcile', status: 'go-wired' },
  { name: 'api-key-record-cleanup-retry', status: 'go-wired' },
  { name: 'account-record-cleanup-retry', status: 'go-wired' },
  { name: 'api-key-availability-schedule-status-sync', status: 'go-wired' },
  { name: 'account-availability-schedule-status-sync', status: 'go-wired' },
  { name: 'resource-authorization-expiry-sweep', status: 'go-wired' },
  { name: 'account-quality-refresh', status: 'go-wired' },
  { name: 'account-balance-refresh', status: 'go-equivalent' },
  { name: 'account-balance-auto-detect-recovery', status: 'go-wired' },
  { name: 'openai-oauth-access-token-refresh', status: 'go-wired' },
  { name: 'oauth-keepalive-token-refresh', status: 'go-wired' },
  { name: 'account-api-key-cooldown-retest', status: 'go-wired' },
  { name: 'normal-route-speed-first-recovery-probe', status: 'go-wired' },
  { name: 'account-circuit-control-plane-maintenance', status: 'go-wired' },
  { name: 'account-list-availability-projection-maintenance', status: 'go-wired' },
  { name: 'account-circuit-recovery', status: 'go-wired' },
  { name: 'key-model-memory-recovery', status: 'go-equivalent' },
  { name: 'data-retention-cleanup', status: 'go-wired' },
  { name: 'chat-retention-cleanup', status: 'go-wired' },
  { name: 'expired-deleted-account-cleanup', status: 'go-wired' },
]

// modeConstraintsSnapshot 镜像 jobregistry/schedule.go modeConstraints()：
//   - ai-performance-summary-windows-refresh：PostgresOnly（SQLite 分支该
//     stage 并入 usage-rank-snapshots-refresh，不独立注册）；
//   - usage-scope-range-windows-refresh：SQLiteOnly（保留，SQLite 分支独有）。
const modeConstraintsSnapshot = {
  'ai-performance-summary-windows-refresh': 'postgres-only',
  'usage-scope-range-windows-refresh': 'sqlite-only',
}

// sqliteNotRegisteredJobs：GoWired 但 SQLite 分支不注册的任务（组合根
// scheduleWiredJob → ResolveScheduleForDriver 反向分支拒绝注册）。
// account-list-availability-projection-maintenance 是 Node PostgreSQL-only
// 物化器，databaseDriver != postgres 时返回空结果不注册。
const sqliteNotRegisteredJobs = ['account-list-availability-projection-maintenance']

// sqliteApplicableWiredJobNames 从注册表快照推导「SQLite 模式下 jobs 组合根
// 会接线」的任务名集合（保持 Node 注册顺序）：go-wired 且非 PostgresOnly 且
// 非 SQLite 分支不注册。缺 Redis 运行态的三族（speed-first/circuit-control/
// circuit-recovery）依赖未齐时由组合根登记 disabled，--run-jobs-once 按
// skipped 计入成功路径（warning 带原因），因此仍保留在集合里。
export function sqliteApplicableWiredJobNames(
  entries = scheduledEntriesSnapshot,
  constraints = modeConstraintsSnapshot,
  notRegisteredOnSqlite = sqliteNotRegisteredJobs,
) {
  return entries
    .filter((entry) => entry.status === 'go-wired')
    .filter((entry) => constraints[entry.name] !== 'postgres-only')
    .filter((entry) => !notRegisteredOnSqlite.includes(entry.name))
    .map((entry) => entry.name)
}

// parseArguments 解析 CLI 参数：--days（默认 31，1..90）、--daily-requests
// （默认 120，1..500）、--data-dir、--log-dir、--skip-rebuild、--skip-verify、
// --force、--help。用法错误抛 MockdataUsageError。
export function parseArguments(argv) {
  const parsed = {
    days: 31,
    dailyRequests: 120,
    dataDir: '',
    logDir: '',
    skipRebuild: false,
    skipVerify: false,
    force: false,
    help: false,
  }
  const withValue = new Set(['--days', '--daily-requests', '--data-dir', '--log-dir'])
  for (let index = 0; index < argv.length; index += 1) {
    const option = argv[index]
    if (option === '--help' || option === '-h') {
      parsed.help = true
      break
    }
    if (option === '--skip-rebuild') { parsed.skipRebuild = true; continue }
    if (option === '--skip-verify') { parsed.skipVerify = true; continue }
    if (option === '--force') { parsed.force = true; continue }
    if (!withValue.has(option)) {
      throw new MockdataUsageError(`未知参数：${option}\n${usage()}`)
    }
    const value = argv[index + 1]
    if (value === undefined || value === '') {
      throw new MockdataUsageError(`${option} 需要一个值\n${usage()}`)
    }
    if (option === '--days' || option === '--daily-requests') {
      if (!/^\d+$/.test(String(value).trim())) {
        throw new MockdataUsageError(`${option} 必须是正整数：${value}`)
      }
      const number = Number(String(value).trim())
      const bounds = option === '--days' ? [1, 90] : [1, 500]
      if (number < bounds[0] || number > bounds[1]) {
        throw new MockdataUsageError(`${option} 必须在 ${bounds[0]} 到 ${bounds[1]} 之间：${value}`)
      }
      if (option === '--days') parsed.days = number
      else parsed.dailyRequests = number
    } else if (option === '--data-dir') {
      parsed.dataDir = String(value).trim()
    } else {
      parsed.logDir = String(value).trim()
    }
    index += 1
  }
  return parsed
}

export function usage() {
  return [
    '用法：node scripts/mockdata.mjs [选项]',
    '  --days <1..90>            造数历史跨度天数（默认 31）',
    '  --daily-requests <1..500> 每日造数请求数（默认 120）',
    '  --data-dir <path>         数据根目录（缺省 JUHE_AI_DATA_DIR 或 .local/dev/data）',
    '  --log-dir <path>          日志目录（缺省 JUHE_AI_LOG_DIR 或 .local/dev/logs）',
    '  --skip-rebuild            跳过 juhe-ai-jobs -run-jobs-once 派生重建',
    '  --skip-verify             跳过 --verify-mockdata-coverage 覆盖校验',
    '  --force                   检测到常驻 gateway/jobs 时仍继续（不建议）',
    '  --help                    打印本说明',
  ].join('\n')
}

// firstConfiguredValue 与 dev.mjs 同语义：取第一个非空配置值。
function firstConfiguredValue(...values) {
  for (const value of values) {
    const trimmed = value?.trim()
    if (trimmed) return trimmed
  }
  return undefined
}

// parseEnvFile/parseEnvValue/loadBackendEnv 复用 dev.mjs 的解析语义（仓库根
// .env + JUHE_AI_ENV_FILE overlay 相对仓库根解析）。
function parseEnvValue(value) {
  const trimmed = value.trim()
  if (trimmed.length >= 2) {
    const quote = trimmed[0]
    if ((quote === '"' || quote === "'") && trimmed[trimmed.length - 1] === quote) {
      return trimmed.slice(1, -1)
    }
  }
  const commentIndex = trimmed.indexOf(' #')
  return commentIndex >= 0 ? trimmed.slice(0, commentIndex).trim() : trimmed
}

function parseEnvFile(content) {
  const env = {}
  for (const rawLine of content.split(/\r?\n/)) {
    const line = rawLine.trim()
    if (!line || line.startsWith('#')) continue
    const match = /^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$/.exec(line)
    if (!match) continue
    env[match[1]] = parseEnvValue(match[2])
  }
  return env
}

function loadEnvFile(filePath) {
  return existsSync(filePath) ? parseEnvFile(readFileSync(filePath, 'utf8')) : {}
}

export function loadBackendEnv() {
  const baseEnv = loadEnvFile(resolve(projectRoot, '.env'))
  const overlayValue = process.env.JUHE_AI_ENV_FILE?.trim() || baseEnv.JUHE_AI_ENV_FILE?.trim()
  if (!overlayValue) return baseEnv
  const overlayPath = isAbsolute(overlayValue) ? overlayValue : resolve(projectRoot, overlayValue)
  return { ...baseEnv, ...loadEnvFile(overlayPath) }
}

// resolveMockdataRuntimeEnv 把 CLI 参数与 env 合成为子进程环境：数据根/日志
// 根按「CLI 参数 > 环境变量 > dev 缺省」钉住（dev.mjs resolveGoProjectEnv 同
// 款）。造数与派生重建只支持 SQLite：显式 JUHE_AI_DATABASE_DRIVER=postgres
// 属环境不兼容，直接报错（缺省时 Go 侧零配置缺省即 sqlite）。
export function resolveMockdataRuntimeEnv({ dataDir, logDir, processEnv, backendEnv }) {
  const merged = { ...backendEnv, ...processEnv }
  const driver = String(merged.JUHE_AI_DATABASE_DRIVER ?? '').trim().toLowerCase()
  if (driver === 'postgres') {
    return {
      error: 'JUHE_AI_DATABASE_DRIVER=postgres 与本地 SQLite 造数不兼容：mockdata 只支持 SQLite 数据根，请改用 SQLite 环境（或临时清空该变量）。',
    }
  }
  const resolvedDataDir = resolve(projectRoot, firstConfiguredValue(dataDir, merged.JUHE_AI_DATA_DIR, devDataRoot))
  const resolvedLogDir = resolve(projectRoot, firstConfiguredValue(logDir, merged.JUHE_AI_LOG_DIR, devLogRoot))
  merged.JUHE_AI_DATA_DIR = resolvedDataDir
  merged.JUHE_AI_LOG_DIR = resolvedLogDir
  return { env: merged, driver: driver || 'sqlite', dataDir: resolvedDataDir, logDir: resolvedLogDir }
}

// buildBootstrapPathsArg 构造 --ensure-schema 的 --paths：六个固定文件名与
// jobs 零配置派生（datadir.Path 固定名表）严格一致，保证 maintenance 建的库
// 就是 jobs 随后打开的同一批文件；shard 数量取 env 或 16（组合根缺省一致）。
export function buildBootstrapPathsArg({ dataDir, env }) {
  const shardCount = String(env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT ?? '').trim() || '16'
  return [
    `business=${resolve(dataDir, 'business.sqlite3')}`,
    `chat=${resolve(dataDir, 'chat.sqlite3')}`,
    `dataset=${resolve(dataDir, 'dataset.sqlite3')}`,
    `usage-catalog=${resolve(dataDir, 'usage-catalog.sqlite3')}`,
    `stats=${resolve(dataDir, 'stats.sqlite3')}`,
    `codex-context-shard-root=${resolve(dataDir, 'codex-context', 'state-shards')}`,
    `codex-context-shard-count=${shardCount}`,
  ].join(',')
}

function defaultSpawnStep(command, args, options) {
  const result = spawnSync(command, args, { shell: false, stdio: 'inherit', ...options })
  return { status: result.status, error: result.error }
}

// binaryFileName：Windows 直接运行需要 .exe 后缀（go build -o 不自动补）。
function binaryFileName(binary) {
  return process.platform === 'win32' ? `${binary}.exe` : binary
}

// buildGoBinary 把 Go 项目入口编译为临时目录内的独立二进制。不用 `go run`：
// 实测 go run 在子进程非零退出时只向 stderr 打印 "exit status N"、自身恒退
// 1，--verify-mockdata-coverage 的 exit 3 契约会被吞掉；直接运行二进制退出
// 码逐值保真（与 scripts/start-go-project.mjs 的二进制运行方式一致）。
function buildGoBinary(spawnStep, { binary, buildDir, env }) {
  const binPath = join(buildDir, binaryFileName(binary))
  const result = spawnStep('go', ['build', '-o', binPath, `./cmd/${binary}`], {
    cwd: resolve(backendGoRoot, 'projects', projectOf(binary)),
    env,
  })
  if (result.error) {
    console.error(`[mockdata] go build ${binary} 无法启动：${result.error.message}`)
    return null
  }
  if (result.status !== 0) {
    console.error(`[mockdata] go build ${binary} 失败（exit ${result.status}），编译错误见上方输出。`)
    return null
  }
  return binPath
}

function projectOf(binary) {
  return binary === 'juhe-ai-jobs' ? 'jobs' : 'maintenance'
}

function runBinaryStep(spawnStep, { label, binPath, args, env }) {
  console.log(`[mockdata] ==> ${label}`)
  const result = spawnStep(binPath, args, { cwd: projectRoot, env })
  if (result.error) {
    console.error(`[mockdata] ${label} 无法启动：${result.error.message}`)
    return 1
  }
  return typeof result.status === 'number' ? result.status : 1
}

// detectLocalGoProjectsRunning 探测常驻 gateway/jobs（dev.mjs
// warnIfGatewayStillRunning 同款 health 端口判定）：有任何 HTTP 应答即视为
// 在运行；连接拒绝/超时是冷路径，保持安静。
export async function detectLocalGoProjectsRunning(env) {
  const targets = [
    { project: 'gateway', address: String(env.JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS ?? '').trim() || '127.0.0.1:3306' },
    { project: 'jobs', address: String(env.JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS ?? '').trim() || '127.0.0.1:3305' },
  ]
  const running = []
  for (const target of targets) {
    try {
      await fetch(`http://${target.address}/health`, { signal: AbortSignal.timeout(2000) })
      running.push(target)
    } catch {
      // 连接拒绝/超时 = 没有可应答的常驻进程，不打扰。
    }
  }
  return running
}

// runMockdataPipeline 是脚本主流程；spawnStep/detectRunning 可注入（测试用
// mock，不真跑子进程）。返回进程退出码。
export async function runMockdataPipeline(options = {}) {
  const argv = options.argv ?? process.argv.slice(2)
  const spawnStep = options.spawnStep ?? defaultSpawnStep
  const detectRunning = options.detectRunning ?? detectLocalGoProjectsRunning
  const processEnv = options.processEnv ?? process.env
  const backendEnv = options.backendEnv ?? loadBackendEnv()

  const parsed = parseArguments(argv)
  if (parsed.help) {
    console.log(usage())
    return 0
  }
  const runtime = resolveMockdataRuntimeEnv({
    dataDir: parsed.dataDir,
    logDir: parsed.logDir,
    processEnv,
    backendEnv,
  })
  if (runtime.error) {
    console.error(`[mockdata] ${runtime.error}`)
    return 2
  }
  mkdirSync(runtime.dataDir, { recursive: true })
  mkdirSync(runtime.logDir, { recursive: true })

  if (!parsed.force) {
    const running = await detectRunning(runtime.env)
    if (running.length > 0) {
      console.error(`[mockdata] 检测到本机常驻 Go 进程仍在运行：${running.map((target) => `${target.project}(${target.address})`).join('、')}。`)
      console.error('[mockdata] 造数与派生重建不能与常驻写者并发（会争抢 SQLite 写锁并污染运行态）。')
      console.error('[mockdata] 请先停止 dev 会话后重试；确认目标数据根完全隔离时可加 --force 跳过本预检。')
      return 1
    }
  }

  const exitCodes = { ensureSeed: null, mockdata: null, rebuild: null, verify: null }
  // 二进制编译进一次性临时目录（finally 清理）；只编译本次流程需要的入口。
  const buildDir = mkdtempSync(join(tmpdir(), 'juhe-mockdata-bin-'))
  try {
    const maintenanceBin = buildGoBinary(spawnStep, { binary: 'juhe-ai-maintenance', buildDir, env: runtime.env })
    if (!maintenanceBin) return 1

    exitCodes.ensureSeed = runBinaryStep(spawnStep, {
      label: `juhe-ai-maintenance --ensure-schema --seed（数据根 ${runtime.dataDir}）`,
      binPath: maintenanceBin,
      args: ['--ensure-schema', '--seed', '--driver', 'sqlite', '--paths', buildBootstrapPathsArg(runtime)],
      env: runtime.env,
    })
    if (exitCodes.ensureSeed !== 0) return exitCodes.ensureSeed

    exitCodes.mockdata = runBinaryStep(spawnStep, {
      label: `juhe-ai-maintenance --mockdata（days=${parsed.days} dailyRequests=${parsed.dailyRequests}）`,
      binPath: maintenanceBin,
      args: [
        '--mockdata',
        '--driver', 'sqlite',
        '--mockdata-data-dir', runtime.dataDir,
        '--mockdata-log-dir', runtime.logDir,
        '--mockdata-days', String(parsed.days),
        '--mockdata-daily-requests', String(parsed.dailyRequests),
      ],
      env: runtime.env,
    })
    if (exitCodes.mockdata !== 0) return exitCodes.mockdata

    if (!parsed.skipRebuild) {
      const jobsBin = buildGoBinary(spawnStep, { binary: 'juhe-ai-jobs', buildDir, env: runtime.env })
      if (!jobsBin) return 1
      // 游标型聚合任务（usage-stats-aggregation / client-ip-stats-aggregation）
      // 一次调用只消费一个批次（statsagg batchLimit 默认 2000 行）：几万行造数
      // 明细必须循环调用到游标追平，否则后续窗口 / 质量 / 额度任务只能看到最早
      // 的一批明细。排空判定直接读 stats_job_state 游标并按聚合器同款谓词统计
      // stats 库 usage_records 的剩余行数。
      const drainJobNames = ['usage-stats-aggregation', 'client-ip-stats-aggregation']
      const restJobNames = sqliteApplicableWiredJobNames().filter((name) => !drainJobNames.includes(name))
      const statsDbPath = resolve(runtime.dataDir, 'stats.sqlite3')
      for (let pass = 1; ; pass += 1) {
        exitCodes.rebuild = runBinaryStep(spawnStep, {
          label: `juhe-ai-jobs -run-jobs-once（排空 ${drainJobNames.join(',')} 第 ${pass} 轮）`,
          binPath: jobsBin,
          args: [`-run-jobs-once=${drainJobNames.join(',')}`],
          env: runtime.env,
        })
        if (exitCodes.rebuild !== 0) return exitCodes.rebuild
        const remaining = statsAggregationRemainingRows(statsDbPath)
        if (remaining === null) {
          console.log('[mockdata] 无法读取聚合游标（stats 库未建或 node:sqlite 不可用），跳过排空循环')
          break
        }
        if (remaining === 0) {
          console.log(`[mockdata] 派生聚合已排空（第 ${pass} 轮）`)
          break
        }
        if (pass >= 200) {
          console.error(`[mockdata] 派生聚合排空超过 200 轮仍有 ${remaining} 行未消费，中止`)
          return 1
        }
        console.log(`[mockdata] 派生聚合排空第 ${pass} 轮完成，剩余 ${remaining} 行未消费`)
      }
      if (restJobNames.length > 0) {
        exitCodes.rebuild = runBinaryStep(spawnStep, {
          label: `juhe-ai-jobs -run-jobs-once（${restJobNames.join(',')}）`,
          binPath: jobsBin,
          args: [`-run-jobs-once=${restJobNames.join(',')}`],
          env: runtime.env,
        })
        if (exitCodes.rebuild !== 0) return exitCodes.rebuild
      }
    }

    if (!parsed.skipVerify) {
      exitCodes.verify = runBinaryStep(spawnStep, {
        label: `juhe-ai-maintenance --verify-mockdata-coverage（数据根 ${runtime.dataDir}）`,
        binPath: maintenanceBin,
        args: ['--verify-mockdata-coverage', '--mockdata-data-dir', runtime.dataDir],
        env: runtime.env,
      })
      if (exitCodes.verify === 3) {
        // exit 3 的两类主因要分开说：专库缺文件（audit/operation/runtime-log/table-monitor/task-runs/account-health
        // 由 gateway/jobs 首次启动建库，--ensure-schema 不建）与派生表未重建（--skip-rebuild 或常驻 jobs 未跑完）。
        console.error('[mockdata] 覆盖校验未 Ready（exit 3）：剩余空表与缺库清单见上方 JSON。')
        console.error('[mockdata] 若清单是「存储文件不存在」类 errors：先 pnpm dev 启动一次（随后停止）再重跑造数；若是派生聚合空表：启动常驻 jobs 后自动补齐。')
        printSummary(parsed, runtime, exitCodes)
        return 3
      }
      if (exitCodes.verify !== 0) return exitCodes.verify
    }

    printSummary(parsed, runtime, exitCodes)
    return 0
  } finally {
    rmSync(buildDir, { recursive: true, force: true })
  }
}

// statsAggregationRemainingRows 按聚合器同款消费谓词统计两个游标型任务
// （usage_stats_aggregation / client_ip_stats_aggregation，二者共用 stats 库
// usage_records 镜像、各有自己的 stats_job_state 游标行）剩余未消费的行数，
// 取二者较小值：任一任务未排空都要继续循环。stats 库 / 表不存在或 node:sqlite
// 不可用时返回 null（调用方据此跳过排空循环，不视为错误）。
function statsAggregationRemainingRows(statsDbPath) {
  let db
  try {
    // .mjs 下没有 require：经 createRequire 加载 node:sqlite（内置模块，
    // Node 22 起可用）；老 Node 或实验开关缺失时优雅退化为「跳过排空循环」。
    const require = createRequire(import.meta.url)
    const { DatabaseSync } = require('node:sqlite')
    db = new DatabaseSync(statsDbPath, { readOnly: true })
  } catch {
    return null
  }
  try {
    const hasTable = db.prepare("SELECT COUNT(*) c FROM sqlite_master WHERE type='table' AND name='usage_records'").get().c > 0
    if (!hasTable) return 0
    let min = Number.POSITIVE_INFINITY
    for (const jobName of ['usage_stats_aggregation', 'client_ip_stats_aggregation']) {
      const cursor = db
        .prepare("SELECT cursor_created_at c, cursor_id i FROM stats_job_state WHERE scope_type='global' AND scope_id='' AND job_name = ?")
        .get(jobName)
      const createdAt = cursor?.c ?? ''
      const id = cursor?.i ?? ''
      const remaining = db
        .prepare('SELECT COUNT(*) c FROM usage_records WHERE created_at > ? OR (created_at = ? AND id > ?)')
        .get(createdAt, createdAt, id).c
      min = Math.min(min, remaining)
    }
    return Number.isFinite(min) ? min : null
  } catch {
    return null
  } finally {
    try { db?.close() } catch { /* 只读句柄关闭失败无需处理 */ }
  }
}

function printSummary(parsed, runtime, exitCodes) {
  const format = (code) => (code === null ? 'skipped' : String(code))
  console.log('[mockdata] ===== 摘要 =====')
  console.log(`[mockdata] 数据目录：${runtime.dataDir}`)
  console.log(`[mockdata] 日志目录：${runtime.logDir}`)
  console.log(`[mockdata] 造数参数：days=${parsed.days} dailyRequests=${parsed.dailyRequests}`)
  console.log(`[mockdata] 退出码：juhe-ai-maintenance --ensure-schema/--seed=${format(exitCodes.ensureSeed)}，juhe-ai-maintenance --mockdata=${format(exitCodes.mockdata)}，juhe-ai-jobs -run-jobs-once=${format(exitCodes.rebuild)}，juhe-ai-maintenance --verify-mockdata-coverage=${format(exitCodes.verify)}`)
  console.log(`[mockdata] 造数 summary 文件：${resolve(runtime.dataDir, 'mockdata-summary.json')}`)
}

const isDirectRun = process.argv[1]
  && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))

if (isDirectRun) {
  runMockdataPipeline()
    .then((code) => { process.exitCode = code })
    .catch((error) => {
      console.error(`[mockdata] ${error instanceof Error ? error.message : String(error)}`)
      process.exitCode = error instanceof MockdataUsageError ? 2 : 1
    })
}
