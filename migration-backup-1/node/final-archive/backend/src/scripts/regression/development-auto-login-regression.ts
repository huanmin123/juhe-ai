import assert from 'node:assert/strict'

import { assertDevelopmentAutoLoginConfig } from '../../config/development.js'
import {
  resolveDevelopmentAutoLoginUsername,
  resolveDevelopmentBackendTarget
} from '../../../../scripts/dev-config.mjs'

assert.equal(resolveDevelopmentAutoLoginUsername(undefined), undefined)
assert.equal(resolveDevelopmentAutoLoginUsername(''), '')
assert.equal(resolveDevelopmentAutoLoginUsername('developer'), 'developer')

assert.equal(resolveDevelopmentBackendTarget({}, {}, {}), 'http://127.0.0.1:3000')
assert.equal(
  resolveDevelopmentBackendTarget({ JUHE_AI_PORT: '3010' }, {}, {}),
  'http://127.0.0.1:3010'
)
assert.equal(
  resolveDevelopmentBackendTarget({}, {}, { JUHE_AI_HOST: '0.0.0.0', JUHE_AI_PORT: '3011' }),
  'http://127.0.0.1:3011'
)
assert.equal(
  resolveDevelopmentBackendTarget({ JUHE_AI_HOST: '::1' }, {}, { JUHE_AI_PORT: '3012' }),
  'http://[::1]:3012'
)
assert.equal(
  resolveDevelopmentBackendTarget(
    { VITE_JUHE_AI_BACKEND_TARGET: ' http://127.0.0.1:4010 ' },
    { VITE_JUHE_AI_BACKEND_TARGET: 'http://127.0.0.1:4011' },
    { JUHE_AI_PORT: '3013' }
  ),
  'http://127.0.0.1:4010'
)

assert.doesNotThrow(() => assertDevelopmentAutoLoginConfig({
  username: undefined,
  nodeEnv: 'production',
  host: '0.0.0.0'
}))
assert.doesNotThrow(() => assertDevelopmentAutoLoginConfig({
  username: 'admin',
  nodeEnv: 'development',
  host: '127.0.0.1'
}))
assert.doesNotThrow(() => assertDevelopmentAutoLoginConfig({
  username: 'admin',
  nodeEnv: '',
  host: '::1'
}))
assert.throws(() => assertDevelopmentAutoLoginConfig({
  username: 'admin',
  nodeEnv: 'production',
  host: '127.0.0.1'
}), /不能在 NODE_ENV=production/)
assert.doesNotThrow(() => assertDevelopmentAutoLoginConfig({
  username: 'admin',
  nodeEnv: 'development',
  host: '0.0.0.0'
}))
assert.doesNotThrow(() => assertDevelopmentAutoLoginConfig({
  username: 'admin',
  nodeEnv: 'development',
  host: '127.0.0.2'
}))

console.log('development auto login regression passed')
