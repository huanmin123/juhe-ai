import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'

const execFileAsync = promisify(execFile)
const backendRoot = resolve(process.cwd())

// Run the three real HTTP Mock-AI lifecycles in isolated processes. Each script
// owns temporary databases and runtime singletons, so this is an intentional
// process boundary rather than a second test fixture implementation.
const scenarios = [
  'test:gateway-runtime-recovery-probe-mock-ai',
  'test:normal-route-speed-first-mock-ai',
  'test:account-quality-gateway-status'
] as const

for (const script of scenarios) {
  const result = await execFileAsync('pnpm.cmd', ['run', script], {
    cwd: backendRoot,
    env: { ...process.env, CI: '1' },
    shell: process.platform === 'win32',
    maxBuffer: 16 * 1024 * 1024
  })
  const output = `${result.stdout}\n${result.stderr}`
  assert.match(output, /passed|通过/, `${script} 未输出成功标记：${output}`)
}

// Keep the cross-mechanism contract visible in the regression suite. The
// existing HTTP scenarios prove each side end-to-end; these guards ensure the
// routing test continues to exercise the circuit/attribution boundaries.
const speedSource = readFileSync(resolve(backendRoot, 'src/scripts/regression/normal-route-speed-first-mock-ai-regression.ts'), 'utf8')
const circuitSource = readFileSync(resolve(backendRoot, 'src/scripts/regression/gateway-runtime-recovery-probe-mock-ai-regression.ts'), 'utf8')
const qualitySource = readFileSync(resolve(backendRoot, 'src/scripts/regression/account-quality-gateway-status-regression.ts'), 'utf8')

assert.match(speedSource, /assertSpeedFirstDoesNotCutoverToAlreadyDegradedCandidate/, '速度优先必须跳过已降级账户')
assert.match(speedSource, /assertBackgroundProbeRestoresPrimary/, '速度优先必须覆盖恢复探针后的重新调度')
assert.match(speedSource, /assertBulkFastTrafficAfterRecovery/, '恢复后必须覆盖健康账户承接流量')
assert.match(circuitSource, /assert\.equal\(opened\.phase, 'OPEN'/, '失败风暴必须覆盖账户 OPEN')
assert.match(circuitSource, /assert\.equal\(leased\.phase, 'HALF_OPEN'/, '恢复必须覆盖 HALF_OPEN 单飞')
assert.match(circuitSource, /assert\.equal\(state\.phase, 'CLOSED'/, '恢复必须覆盖 CLOSED')
assert.match(qualitySource, /failureAttribution/, '失败/成功归因必须由质量状态回归覆盖')
assert.match(qualitySource, /temporary_unavailable/, '质量失败必须升级临时不可调用')
assert.match(qualitySource, /effectiveAvailability\.status, 'available'/, '冷却恢复后必须重新可调度')

console.log('route speed/circuit lifecycle regression passed')
