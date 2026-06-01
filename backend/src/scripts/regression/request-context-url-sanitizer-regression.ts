import assert from 'node:assert/strict'

import { sanitizeUrlCredentialsForLog, sanitizeUrlForLog } from '../../shared/request-context.js'

const sanitizedRelative = sanitizeUrlForLog('/oauth/callback?access_token=access-leak&refreshToken=refresh-leak&client_secret=secret-leak&safe=ok')
assert(sanitizedRelative.includes('access_token=%5Bredacted%5D'), '请求日志 URL 应脱敏 access_token 查询参数')
assert(sanitizedRelative.includes('refreshToken=%5Bredacted%5D'), '请求日志 URL 应脱敏 refreshToken 查询参数')
assert(sanitizedRelative.includes('client_secret=%5Bredacted%5D'), '请求日志 URL 应脱敏 client_secret 查询参数')
assert(sanitizedRelative.includes('safe=ok'), '请求日志 URL 应保留非敏感查询参数')
assert(!sanitizedRelative.includes('access-leak'), '请求日志 URL 不应包含 access_token 原文')
assert(!sanitizedRelative.includes('refresh-leak'), '请求日志 URL 不应包含 refreshToken 原文')
assert(!sanitizedRelative.includes('secret-leak'), '请求日志 URL 不应包含 client_secret 原文')

const sanitizedAbsolute = sanitizeUrlCredentialsForLog('https://user:pass@example.com/v1/responses?id_token=id-leak&code_verifier=verifier-leak&safe=ok')
assert.equal(sanitizedAbsolute, 'https://[redacted]@example.com/v1/responses?id_token=%5Bredacted%5D&code_verifier=%5Bredacted%5D&safe=ok')

console.log('请求上下文 URL 脱敏回归通过：常见 OAuth/API 密钥查询参数和 URL 用户信息不会进入日志')
