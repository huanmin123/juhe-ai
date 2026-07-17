import assert from 'node:assert/strict'

import { assertDevelopmentAutoLoginConfig } from '../../config/development.js'

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
assert.throws(() => assertDevelopmentAutoLoginConfig({
  username: 'admin',
  nodeEnv: 'development',
  host: '0.0.0.0'
}), /只允许后端监听回环地址/)

console.log('development auto login regression passed')
