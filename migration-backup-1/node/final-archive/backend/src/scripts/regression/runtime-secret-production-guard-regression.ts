import { strict as assert } from 'node:assert'
import { spawnSync } from 'node:child_process'

const importRuntimeConfigCode = "import('./src/config/runtime.ts').then(() => console.log('runtime-ok'))"
const strongSecret = 'runtime-secret-production-guard-32-chars-minimum'

const defaultSecretResult = spawnRuntimeImport({
  NODE_ENV: 'production',
  JUHE_AI_SECRET: ''
})
assert.notEqual(defaultSecretResult.status, 0, '生产环境缺少 JUHE_AI_SECRET 时不应允许启动')
assert.match(
  `${defaultSecretResult.stderr}\n${defaultSecretResult.stdout}`,
  /JUHE_AI_SECRET.*生产环境.*默认开发密钥/,
  '生产环境默认密钥失败信息应明确指向 JUHE_AI_SECRET'
)

const shortSecretResult = spawnRuntimeImport({
  NODE_ENV: 'production',
  JUHE_AI_SECRET: 'short-secret'
})
assert.notEqual(shortSecretResult.status, 0, '生产环境短 JUHE_AI_SECRET 时不应允许启动')

const strongSecretResult = spawnRuntimeImport({
  NODE_ENV: 'production',
  JUHE_AI_SECRET: strongSecret,
  JUHE_AI_ALLOWED_ORIGINS: 'https://admin.example.com'
})
assert.equal(strongSecretResult.status, 0, '生产环境配置强 JUHE_AI_SECRET 和后台 Origin 白名单时应允许加载运行配置')
assert.match(strongSecretResult.stdout, /runtime-ok/, '强密钥场景应成功导入 runtime 配置')

console.log('运行时生产密钥保护回归通过：生产环境拒绝默认/短 JUHE_AI_SECRET')

function spawnRuntimeImport(env: Record<string, string>) {
  return spawnSync(process.execPath, ['--import', 'tsx', '-e', importRuntimeConfigCode], {
    cwd: process.cwd(),
    env: {
      ...process.env,
      ...env
    },
    encoding: 'utf8'
  })
}
