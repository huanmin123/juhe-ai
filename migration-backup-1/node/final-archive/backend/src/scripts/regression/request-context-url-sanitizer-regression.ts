import assert from 'node:assert/strict'

import { sanitizeUrlCredentialsForLog, sanitizeUrlForLog } from '../../shared/request-context.js'

const sanitizedRelative = sanitizeUrlForLog('/oauth/callback?access_token=access-leak&refreshToken=refresh-leak&client_secret=secret-leak&safe=ok')
assert(sanitizedRelative.includes('access_token=access-leak'), '请求日志 URL 应保留 access_token 查询参数原文')
assert(sanitizedRelative.includes('refreshToken=refresh-leak'), '请求日志 URL 应保留 refreshToken 查询参数原文')
assert(sanitizedRelative.includes('client_secret=secret-leak'), '请求日志 URL 应保留 client_secret 查询参数原文')
assert(sanitizedRelative.includes('safe=ok'), '请求日志 URL 应保留非敏感查询参数')

const sanitizedAbsolute = sanitizeUrlCredentialsForLog('https://user:pass@example.com/v1/responses?id_token=id-leak&code_verifier=verifier-leak&safe=ok')
assert.equal(sanitizedAbsolute, 'https://user:pass@example.com/v1/responses?id_token=id-leak&code_verifier=verifier-leak&safe=ok')

const sanitizedAuthorize = sanitizeUrlForLog('/oauth/authorize?state=state-leak&code_challenge=challenge-leak&transaction_id=transaction-leak&safe=ok')
assert(!sanitizedAuthorize.includes('state-leak'), 'OAuth authorize 日志不得保留 state')
assert(!sanitizedAuthorize.includes('challenge-leak'), 'OAuth authorize 日志不得保留 PKCE challenge')
assert(!sanitizedAuthorize.includes('transaction-leak'), 'OAuth authorize 日志不得保留事务标识')
assert(sanitizedAuthorize.includes('safe=ok'), 'OAuth authorize 日志应保留非敏感查询参数')

console.log('请求上下文 URL 日志回归通过：普通请求保留原文，OAuth 浏览器事务参数已脱敏')
