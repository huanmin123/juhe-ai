import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const gatewayLoadSource = readFileSync(new URL('../performance/performance-gateway-load-test.ts', import.meta.url), 'utf8')
assert.match(gatewayLoadSource, /loadDurationMs: round\(input\.stats\.loadDurationMs/, '正式报告必须单列 load phase 时长')
assert.match(gatewayLoadSource, /totalRequests \/ loadDurationSeconds/, '正式 QPS 只能除以 load phase 时长')
assert.match(gatewayLoadSource, /配置场景 .*没有正式 load 样本/, '每个配置场景必须有样本门禁')
assert.match(gatewayLoadSource, /历史请求体档位 .*没有正式 load 样本/, '每个历史请求体 tier 必须有样本门禁')
assert.match(gatewayLoadSource, /SSE TTFB 样本不完整/, 'SSE TTFB 样本必须完整')
assert.match(gatewayLoadSource, /SSE 终态样本不完整/, 'SSE 必须验证终态')
assert.match(gatewayLoadSource, /stream\.length !== 0 \|\| stream\.pendingCount !== 0 \|\| stream\.lagCount !== 0 \|\| stream\.backlogCount !== 0/, 'settle 后五类 Stream 必须精确清零')
assert.match(gatewayLoadSource, /使用记录未精确对账/, 'usage 必须精确对账')
assert.match(gatewayLoadSource, /审计日志未精确对账/, 'audit 必须按稳定 trace 规则精确对账')
assert.match(gatewayLoadSource, /account_last_used_coordination/, 'PG 必须识别账户 last_used_at 的已知短暂协调等待')
assert.match(gatewayLoadSource, /persistentOrSlowKnownCoordinationWaits/, 'PG 必须区分瞬时与持续/超阈值已知协调等待')
assert.match(gatewayLoadSource, /maxOtherWaiters/, 'PG 未知锁等待仍必须保持独立零容忍门禁')

const probeSource = readFileSync(new URL('../performance/performance-control-plane-probe.ts', import.meta.url), 'utf8')
assert.match(probeSource, /sampleCoverage < input\.minSampleCoverage/, 'probe 必须门禁预期样本覆盖率')
assert.match(probeSource, /scheduleDriftMs\.max > input\.maxScheduleDriftMs/, 'probe 必须门禁调度 drift')
assert.match(probeSource, /boundedSampleTimeline/, 'probe 报告必须保留有界原始样本时间线')
assert.match(probeSource, /usage-records\?page=1&pageSize=20&result=all&systemAccountId=sys_admin/, '默认 usage-records 探针必须携带必填系统账户')
assert.match(gatewayLoadSource, /quiesceGatewayFixture/, '清理前必须先停用压测账户，阻止健康检查继续进入 mock upstream')
assert.match(gatewayLoadSource, /压测清理后仍残留/, '正式负载退出前必须验证夹具业务与持久化记录无残留')

const orchestratorSource = readFileSync(new URL('../performance/performance-10m-acceptance.ts', import.meta.url), 'utf8')
assert.match(orchestratorSource, /external-3-gateway-independent-probe/, '正式入口必须要求显式外部三 Gateway 确认')
assert.match(orchestratorSource, /assert\.equal\(gatewayHealthUrls\.length, 3/, '正式入口必须要求三个 Gateway 直连健康地址')
assert.match(orchestratorSource, /envInteger\('JUHE_AI_PERFORMANCE_ACCEPTANCE_DURATION_SECONDS', 600, 600, 3600\)/, '正式入口不得接受不足 10 分钟的 load')
assert.match(orchestratorSource, /singleServerFallbackAllowed: false/, '正式入口必须明确禁止单 server 回退')
assert.match(orchestratorSource, /此入口不会伪装成 sysstat/, '正式入口必须明确外部资源采集边界')

const failure = await runMissingConfirmationPreflight()
assert.notEqual(failure.code, 0)
assert.match(`${failure.stdout}\n${failure.stderr}`, /JUHE_AI_PERFORMANCE_ACCEPTANCE_CONFIRM/)

console.log('performance acceptance gates regression passed')

function runMissingConfirmationPreflight(): Promise<{ code: number | null; stdout: string; stderr: string }> {
  return new Promise((resolveRun, rejectRun) => {
    const env = { ...process.env }
    delete env.JUHE_AI_PERFORMANCE_ACCEPTANCE_CONFIRM
    const child = spawn(process.execPath, ['--import', 'tsx', 'src/scripts/performance/performance-10m-acceptance.ts'], {
      cwd: resolve('.'),
      env,
      stdio: ['ignore', 'pipe', 'pipe'],
      windowsHide: true
    })
    let stdout = ''
    let stderr = ''
    child.stdout.on('data', (chunk: Buffer) => { stdout += chunk.toString('utf8') })
    child.stderr.on('data', (chunk: Buffer) => { stderr += chunk.toString('utf8') })
    child.once('error', rejectRun)
    child.once('exit', (code) => resolveRun({ code, stdout, stderr }))
  })
}
