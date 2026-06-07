import assert from 'node:assert/strict'

import { sanitizeUrlCredentialsForLog, sanitizeUrlForLog } from '../../shared/request-context.js'

const sanitizedRelative = sanitizeUrlForLog('/oauth/callback?access_token=access-leak&refreshToken=refresh-leak&client_secret=secret-leak&safe=ok')
assert(sanitizedRelative.includes('access_token=access-leak'), '请求日志 URL 应保留 access_token 查询参数原文')
assert(sanitizedRelative.includes('refreshToken=refresh-leak'), '请求日志 URL 应保留 refreshToken 查询参数原文')
assert(sanitizedRelative.includes('client_secret=secret-leak'), '请求日志 URL 应保留 client_secret 查询参数原文')
assert(sanitizedRelative.includes('safe=ok'), '请求日志 URL 应保留非敏感查询参数')

const sanitizedAbsolute = sanitizeUrlCredentialsForLog('https://user:pass@example.com/v1/responses?id_token=id-leak&code_verifier=verifier-leak&safe=ok')
assert.equal(sanitizedAbsolute, 'https://user:pass@example.com/v1/responses?id_token=id-leak&code_verifier=verifier-leak&safe=ok')

console.log('请求上下文 URL 日志回归通过：查询参数和 URL 用户信息按原文进入日志')
