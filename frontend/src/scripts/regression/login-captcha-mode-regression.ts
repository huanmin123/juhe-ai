import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../../..')

const loginViewSource = readSource('src/views/login/LoginView.vue')
const authApiSource = readSource('src/api/domains/auth.ts')
const authComposableSource = readSource('src/composables/useAuth.ts')
const identitySource = readSource('src/types/domain/identity.ts')

assert.match(identitySource, /required:\s*boolean/, '验证码能力响应必须明确返回 required 字段')
assert.match(loginViewSource, /v-if="captchaRequired"/, '验证码关闭时登录页必须隐藏验证码表单项')
assert.match(loginViewSource, /if \(captchaRequired\.value/, '登录前端校验必须只在 required=true 时要求验证码')
assert.match(authApiSource, /captchaId\?:\s*string/, '登录 API 入参必须允许验证码字段缺省')
assert.match(authComposableSource, /captchaId\?:\s*string/, '登录 composable 必须允许验证码字段缺省')

console.log('登录验证码模式前端回归通过：页面按后端能力隐藏验证码，登录入参允许显式关闭模式省略验证码字段')

function readSource(relativePath: string): string {
  return readFileSync(resolve(frontendRoot, relativePath), 'utf8')
}
