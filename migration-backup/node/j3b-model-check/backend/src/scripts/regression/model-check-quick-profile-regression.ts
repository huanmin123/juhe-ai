import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

const serviceSource = readFileSync(new URL('../../modules/model-checks/model-checks.service.ts', import.meta.url), 'utf8')
const tokenSource = readFileSync(new URL('../../modules/model-checks/model-checks-token-probes.ts', import.meta.url), 'utf8')
const profilesSource = readFileSync(new URL('../../modules/model-checks/model-checks.profiles.ts', import.meta.url), 'utf8')

assert.match(serviceSource, /profile === 'quick' \? quickProbeSetVersion : probeSetVersion/, 'run 创建必须按 profile 写入对应 probe-set 版本')
assert.match(serviceSource, /if \(profileMode === 'quick'\) return \{ items, basic, behavior: undefined \}/, 'quick 能力套件完成后不得进入行为、长上下文或稳定性探针')
assert.doesNotMatch(serviceSource, /quickBehaviorProbeDefinitions/, 'quick 不得引用行为题定义')
assert.match(serviceSource, /profileMode: 'quick',[\s\S]{0,120}prefix: 'target',[\s\S]{0,120}observationEnabled: false/, 'quick 目标 Token 必须显式禁止写 observation')
assert.match(serviceSource, /profileMode: 'quick',[\s\S]{0,120}prefix: 'trusted_comparison',[\s\S]{0,120}observationEnabled: false/, 'quick 可信对比 Token 必须显式禁止写 observation')
assert.match(tokenSource, /const roundCount = profileMode === 'quick' \? 1 : 3/, 'quick Token 必须固定为单轮')
assert.match(tokenSource, /const observationEnabled = input\.observationEnabled \?\? profileMode === 'full'/, '只有 full 默认写入 Token observation')
assert.match(profilesSource, /quickProbeSetVersion = 'multi-provider-model-check-quick-v2-light-suite'/, 'quick 必须使用新的轻量 probe-set 版本')

// 引入隔离 mock 回归以执行单账户 quick 请求路径。
await import('./model-check-full-profile-regression.js')

console.log('模型检测 quick profile 回归通过：轻量探针集、Token 诊断隔离和单账户请求路径符合预期')
