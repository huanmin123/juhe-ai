#!/usr/bin/env node

// scripts/mockdata.mjs 的 node --test 单测：参数解析、env 不兼容报错、
// SQLite 适用任务名集合生成规则，以及依赖注入 mock spawn 的编排顺序断言。
// 不真跑任何 Go 子进程；管道测试的数据根/日志根全部指向系统临时目录。

import assert from 'node:assert/strict'
import { mkdtemp, rm } from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'
import {
  MockdataUsageError,
  parseArguments,
  resolveMockdataRuntimeEnv,
  runMockdataPipeline,
  sqliteApplicableWiredJobNames,
} from './mockdata.mjs'

async function createTempDirectory() {
  return mkdtemp(path.join(os.tmpdir(), 'juhe-mockdata-script-test-'))
}

test('parseArguments 缺省值与全量参数', () => {
  const defaults = parseArguments([])
  assert.deepEqual(defaults, {
    days: 31,
    dailyRequests: 120,
    dataDir: '',
    logDir: '',
    skipRebuild: false,
    skipVerify: false,
    force: false,
    help: false,
  })

  const full = parseArguments([
    '--days', '7',
    '--daily-requests', '20',
    '--data-dir', 'C:/tmp/mockdata-e2e',
    '--log-dir', 'C:/tmp/mockdata-e2e/logs',
    '--skip-rebuild',
    '--skip-verify',
    '--force',
  ])
  assert.equal(full.days, 7)
  assert.equal(full.dailyRequests, 20)
  assert.equal(full.dataDir, 'C:/tmp/mockdata-e2e')
  assert.equal(full.logDir, 'C:/tmp/mockdata-e2e/logs')
  assert.equal(full.skipRebuild, true)
  assert.equal(full.skipVerify, true)
  assert.equal(full.force, true)
})

test('parseArguments 用法错误', () => {
  assert.throws(() => parseArguments(['--days']), MockdataUsageError)
  assert.throws(() => parseArguments(['--data-dir', '']), MockdataUsageError)
  assert.throws(() => parseArguments(['--unknown']), /未知参数/)
  assert.throws(() => parseArguments(['--days', 'abc']), /必须是正整数/)
  assert.throws(() => parseArguments(['--days', '0']), /1 到 90/)
  assert.throws(() => parseArguments(['--days', '91']), /1 到 90/)
  assert.throws(() => parseArguments(['--daily-requests', '0']), /1 到 500/)
  assert.throws(() => parseArguments(['--daily-requests', '501']), /1 到 500/)
})

test('resolveMockdataRuntimeEnv：postgres driver 报错、缺省 SQLite、路径钉住', () => {
  const postgres = resolveMockdataRuntimeEnv({
    dataDir: '',
    logDir: '',
    processEnv: { JUHE_AI_DATABASE_DRIVER: 'postgres' },
    backendEnv: {},
  })
  assert.match(postgres.error, /SQLite 造数不兼容/)

  const resolved = resolveMockdataRuntimeEnv({
    dataDir: 'C:/tmp/mockdata-e2e',
    logDir: '',
    processEnv: {},
    backendEnv: {},
  })
  assert.equal(resolved.driver, 'sqlite')
  assert.equal(resolved.dataDir, 'C:\\tmp\\mockdata-e2e')
  assert.match(resolved.logDir, /\.local[/\\]dev[/\\]logs$/)
  assert.equal(resolved.env.JUHE_AI_DATA_DIR, resolved.dataDir)
  assert.equal(resolved.env.JUHE_AI_LOG_DIR, resolved.logDir)
})

// registryFixture 是与 jobregistry 快照同形状的最小表，锁定生成规则的四个
// 判定：只取 go-wired、剔除 postgres-only、剔除 SQLite 不注册、保留
// sqlite-only。
const registryFixture = {
  entries: [
    { name: 'a-equivalent', status: 'go-equivalent' },
    { name: 'b-wired', status: 'go-wired' },
    { name: 'c-pg-only', status: 'go-wired' },
    { name: 'd-sqlite-only', status: 'go-wired' },
    { name: 'e-not-registered-sqlite', status: 'go-wired' },
  ],
  constraints: { 'c-pg-only': 'postgres-only', 'd-sqlite-only': 'sqlite-only' },
  notRegisteredOnSqlite: ['e-not-registered-sqlite'],
}

test('sqliteApplicableWiredJobNames 生成规则（合成注册表）', () => {
  assert.deepEqual(
    sqliteApplicableWiredJobNames(registryFixture.entries, registryFixture.constraints, registryFixture.notRegisteredOnSqlite),
    ['b-wired', 'd-sqlite-only'],
  )
})

test('sqliteApplicableWiredJobNames 快照：SQLite 适用集合', () => {
  const names = sqliteApplicableWiredJobNames()
  // SQLiteOnly 保留。
  assert.ok(names.includes('usage-scope-range-windows-refresh'))
  // PostgresOnly 剔除（SQLite 分支该 stage 并入 usage-rank-snapshots-refresh）。
  assert.ok(!names.includes('ai-performance-summary-windows-refresh'))
  // SQLite 分支不注册的 PG-only 物化器剔除。
  assert.ok(!names.includes('account-list-availability-projection-maintenance'))
  // go-equivalent（其他组件/进程接管）剔除。
  for (const name of ['system-metrics-sample', 'account-balance-refresh', 'key-model-memory-recovery']) {
    assert.ok(!names.includes(name), name)
  }
  // 缺 Redis 时由组合根登记 disabled、--run-jobs-once 按 skipped 计入成功
  // 路径，因此三族仍保留在集合里。
  for (const name of [
    'normal-route-speed-first-recovery-probe',
    'account-circuit-control-plane-maintenance',
    'account-circuit-recovery',
  ]) {
    assert.ok(names.includes(name), name)
  }
  assert.equal(names.length, 28)
})

// recordingSpawnStep 记录每次注入的调用；codeFor 按调用返回退出码。调用分
// 两类：go build（command='go' 且 args[0]==='build'，binary 取 args[3] 包路
// 径 basename）与直接运行的二进制（binary 取 command 去 .exe 后缀）。
function recordingSpawnStep(codeFor = () => 0) {
  const calls = []
  const spawnStep = (command, args, options) => {
    const isBuild = command === 'go' && args[0] === 'build'
    const binary = isBuild
      ? path.basename(args[3] ?? '')
      : path.basename(command ?? '').replace(/\.exe$/i, '')
    const call = { command, args, options, binary, kind: isBuild ? 'build' : 'run' }
    calls.push(call)
    return { status: codeFor(call) ?? 0, error: undefined }
  }
  return { calls, spawnStep }
}

const runCalls = (recorder) => recorder.calls.filter((call) => call.kind === 'run')

// hermeticPipelineOptions 把数据根/日志根钉进临时目录，单元测试不触碰仓库
// 内 .local 路径；detectRunning 注入为无常驻进程。
async function hermeticPipelineOptions(argv, spawnStep, detectRunning) {
  const tempDir = await createTempDirectory()
  return {
    tempDir,
    options: {
      argv: [...argv, '--data-dir', path.join(tempDir, 'data'), '--log-dir', path.join(tempDir, 'logs')],
      spawnStep,
      detectRunning,
      processEnv: {},
      backendEnv: {},
    },
  }
}

test('runMockdataPipeline：先编译二进制再顺序执行四步并透传数据根 env（mock spawn）', async () => {
  const recorder = recordingSpawnStep()
  const { tempDir, options } = await hermeticPipelineOptions(['--days', '7', '--daily-requests', '20'], recorder.spawnStep, async () => [])
  try {
    const code = await runMockdataPipeline(options)
    assert.equal(code, 0)
    // 两次 go build（maintenance + jobs）+ 四步运行。
    assert.deepEqual(recorder.calls.map((call) => `${call.kind}:${call.binary}`), [
      'build:juhe-ai-maintenance',
      'run:juhe-ai-maintenance',
      'run:juhe-ai-maintenance',
      'build:juhe-ai-jobs',
      'run:juhe-ai-jobs',
      'run:juhe-ai-maintenance',
    ])

    const [ensure, mockdata, rebuild, verify] = runCalls(recorder)
    assert.ok(ensure.args.includes('--ensure-schema'))
    assert.ok(ensure.args.includes('--seed'))
    const pathsValue = ensure.args[ensure.args.indexOf('--paths') + 1]
    assert.match(pathsValue, /business=.+[/\\]business\.sqlite3/)
    assert.match(pathsValue, /codex-context-shard-count=16$/)

    assert.ok(mockdata.args.includes('--mockdata'))
    assert.equal(mockdata.args[mockdata.args.indexOf('--mockdata-days') + 1], '7')
    assert.equal(mockdata.args[mockdata.args.indexOf('--mockdata-daily-requests') + 1], '20')

    assert.match(path.basename(rebuild.command), /^juhe-ai-jobs(\.exe)?$/)
    assert.match(rebuild.args[0], /^-run-jobs-once=system-metrics-trend-windows-refresh,/)
    assert.ok(rebuild.args[0].includes('usage-scope-range-windows-refresh'))
    assert.ok(!rebuild.args[0].includes('ai-performance-summary-windows-refresh'))
    assert.equal(rebuild.options.env.JUHE_AI_DATA_DIR, path.join(tempDir, 'data'))
    assert.equal(rebuild.options.env.JUHE_AI_LOG_DIR, path.join(tempDir, 'logs'))

    assert.ok(verify.args.includes('--verify-mockdata-coverage'))
  } finally {
    await rm(tempDir, { recursive: true, force: true })
  }
})

test('runMockdataPipeline：--skip-rebuild/--skip-verify 跳步、失败即停', async () => {
  const skipper = recordingSpawnStep()
  const skipOptions = await hermeticPipelineOptions(['--skip-rebuild', '--skip-verify'], skipper.spawnStep, async () => [])
  const { tempDir: skipTemp } = skipOptions
  try {
    const skippedCode = await runMockdataPipeline(skipOptions.options)
    assert.equal(skippedCode, 0)
    // 只编译 maintenance（jobs 不需要），运行仅 ensure+mockdata 两步。
    assert.deepEqual(skipper.calls.map((call) => `${call.kind}:${call.binary}`), [
      'build:juhe-ai-maintenance',
      'run:juhe-ai-maintenance',
      'run:juhe-ai-maintenance',
    ])
  } finally {
    await rm(skipTemp, { recursive: true, force: true })
  }

  const failing = recordingSpawnStep((call) => (call.kind === 'run' && call.binary === 'juhe-ai-maintenance' ? 1 : 0))
  const failOptions = await hermeticPipelineOptions([], failing.spawnStep, async () => [])
  const { tempDir: failTemp } = failOptions
  try {
    const failedCode = await runMockdataPipeline(failOptions.options)
    assert.equal(failedCode, 1)
    assert.equal(runCalls(failing).length, 1, '第一步失败后必须停止')
  } finally {
    await rm(failTemp, { recursive: true, force: true })
  }

  // 覆盖校验 exit 3（未 Ready）：直接运行二进制的退出码逐值透传，脚本以 3
  // 收场且前面的步骤全部走完。
  const notReady = recordingSpawnStep((call) => (
    call.kind === 'run' && call.binary === 'juhe-ai-maintenance' && call.args.includes('--verify-mockdata-coverage') ? 3 : 0
  ))
  const notReadyOptions = await hermeticPipelineOptions([], notReady.spawnStep, async () => [])
  const { tempDir: notReadyTemp } = notReadyOptions
  try {
    const notReadyCode = await runMockdataPipeline(notReadyOptions.options)
    assert.equal(notReadyCode, 3)
    assert.equal(runCalls(notReady).length, 4)
  } finally {
    await rm(notReadyTemp, { recursive: true, force: true })
  }
})

test('runMockdataPipeline：检测到常驻进程默认中止，--force 跳过预检', async () => {
  const recorder = recordingSpawnStep()
  let detectCalls = 0
  const running = [{ project: 'gateway', address: '127.0.0.1:3306' }]
  const abortOptions = await hermeticPipelineOptions([], recorder.spawnStep, async () => { detectCalls += 1; return running })
  const { tempDir: abortTemp } = abortOptions
  try {
    const aborted = await runMockdataPipeline(abortOptions.options)
    assert.equal(aborted, 1)
    assert.equal(detectCalls, 1)
    assert.equal(recorder.calls.length, 0, '中止后不得编译或启动任何子进程')
  } finally {
    await rm(abortTemp, { recursive: true, force: true })
  }

  const forcedOptions = await hermeticPipelineOptions(['--force', '--skip-rebuild', '--skip-verify'], recorder.spawnStep, async () => running)
  const { tempDir: forcedTemp } = forcedOptions
  try {
    const forced = await runMockdataPipeline(forcedOptions.options)
    assert.equal(forced, 0)
    assert.equal(detectCalls, 1, '--force 下不再探测')
    assert.deepEqual(recorder.calls.map((call) => `${call.kind}:${call.binary}`), [
      'build:juhe-ai-maintenance',
      'run:juhe-ai-maintenance',
      'run:juhe-ai-maintenance',
    ])
  } finally {
    await rm(forcedTemp, { recursive: true, force: true })
  }
})

test('runMockdataPipeline：--help 不启动任何子进程', async () => {
  const recorder = recordingSpawnStep()
  const { tempDir, options } = await hermeticPipelineOptions(['--help'], recorder.spawnStep, async () => {
    throw new Error('不应探测常驻进程')
  })
  try {
    const code = await runMockdataPipeline(options)
    assert.equal(code, 0)
    assert.equal(recorder.calls.length, 0)
  } finally {
    await rm(tempDir, { recursive: true, force: true })
  }
})
