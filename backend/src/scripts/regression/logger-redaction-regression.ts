import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { Writable } from 'node:stream'

import pino from 'pino'

import { errorLogFields, redactSensitiveLogTextForTest, serializeLogError } from '../../shared/logger.js'

const openAiKey = 'sk-test_logger_redaction_secret_1234567890'
const bearerToken = 'Bearer eyJloggerRedaction.eyJsecretPayload.loggerSignature'
const jwtToken = 'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJsb2dnZXIiLCJzZWNyZXQiOiIxMjMifQ.signatureValue'
const refreshToken = 'rt_logger_redaction_refresh_secret'
const accessToken = 'at_logger_redaction_access_secret'
const apiKey = 'plain_logger_redaction_api_key'

const rawText = [
  `Authorization: ${bearerToken}`,
  `x-api-key: ${apiKey}`,
  `refresh_token=${refreshToken}`,
  `access_token=${accessToken}`,
  `api_key=${apiKey}`,
  openAiKey,
  jwtToken
].join(' ')

const redactedText = redactSensitiveLogTextForTest(rawText)
assertMarkersPresent(redactedText, [openAiKey, bearerToken, jwtToken, refreshToken, accessToken, apiKey], '日志字符串原文')

const rawJsonLine = JSON.stringify({
  api_key: apiKey,
  access_token: accessToken,
  refresh_token: refreshToken,
  headers: {
    'x-api-key': apiKey,
    authorization: bearerToken,
    'proxy-authorization': bearerToken
  },
  nested: {
    refreshToken
  }
})
const redactedJsonLine = redactSensitiveLogTextForTest(rawJsonLine)
assertMarkersPresent(redactedJsonLine, [bearerToken, refreshToken, accessToken, apiKey], 'JSON 日志字段原文')

const error = new Error(`upstream failed with Authorization: ${bearerToken}; refresh_token=${refreshToken}; key=${openAiKey}`)
error.stack = `Error: ${error.message}\n    at logger_redaction (${jwtToken})`
const fields = errorLogFields(error)
const serializedFields = JSON.stringify(fields)
assertMarkersPresent(serializedFields, [openAiKey, bearerToken, jwtToken, refreshToken], 'Error message/stack 原文')

const nonErrorFields = errorLogFields(`proxy-authorization: ${bearerToken}; api_key=${apiKey}`)
assertMarkersPresent(JSON.stringify(nonErrorFields), [bearerToken, apiKey], '非 Error 异常文本原文')

const oversizedErrorText = '😀中文\u0000"'.repeat(23_000)
const oversizedError = new Error(oversizedErrorText)
oversizedError.stack = `Error: ${oversizedErrorText}\n    at logger_capacity_regression`
const boundedFields = errorLogFields(oversizedError, { event: 'logger_capacity_regression' })
const boundedError = boundedFields.err as Record<string, unknown>
assert.equal(boundedError.errorTruncated, true, '超大 Error 必须明确标记为已截断')
assert(Buffer.byteLength(String(boundedError.message), 'utf8') <= 8 * 1024, '超大 Error message 必须限制在 8 KiB')
assert(Buffer.byteLength(String(boundedError.stack), 'utf8') <= 8 * 1024, '超大 Error stack 必须限制在 8 KiB')
assert(Buffer.byteLength(JSON.stringify(boundedFields), 'utf8') < 32 * 1024, '普通日志错误字段必须远低于 Loki 单行拒绝阈值')

const pinoLines: string[] = []
const pinoLogger = pino({ serializers: { err: serializeLogError } }, new Writable({
  write(chunk, _encoding, callback) {
    pinoLines.push(String(chunk))
    callback()
  }
}))
pinoLogger.error({ event: 'logger_capacity_regression', err: oversizedError }, '验证 Pino 实际序列化路径')
assert.equal(pinoLines.length, 1, 'Pino 必须输出一条日志')
assert(Buffer.byteLength(pinoLines[0], 'utf8') < 32 * 1024, 'Pino 实际日志必须远低于 Loki 单行拒绝阈值')
assert.equal(pinoLines[0].includes(oversizedErrorText), false, 'Pino 实际日志不得写入完整超大错误文本')
pinoLogger.error(errorLogFields(oversizedError, { event: 'logger_capacity_regression_prebuilt' }), '验证预构建错误字段路径')
assert.equal(pinoLines.length, 2, 'Pino 必须输出预构建错误字段日志')
const prebuiltError = JSON.parse(pinoLines[1]).err as Record<string, unknown>
assert.equal(prebuiltError.type, 'Error', '预构建错误字段进入 Pino 后必须保留错误类型')
assert(Buffer.byteLength(String(prebuiltError.message), 'utf8') <= 8 * 1024, '预构建错误字段进入 Pino 后必须保持有界')

const loggerSource = readFileSync(new URL('../../shared/logger.ts', import.meta.url), 'utf8')
assert.match(loggerSource, /serializers:\s*\{\s*err:\s*serializeLogError\s*\}/u, '生产 Pino 必须使用统一有界错误序列化器')

const streamSource = readFileSync(new URL('../../modules/gateway/response/stream.ts', import.meta.url), 'utf8')
assert.doesNotMatch(streamSource, /streamLogger\.warn\(\{\s*error\s*\}/u, '流式失败状态记录不得直接序列化未受限 error 对象')
assert.match(streamSource, /streamLogger\.warn\(errorLogFields\(error\)/u, '流式失败状态记录必须走统一有界错误字段')

const finalizationSource = readFileSync(new URL('../../modules/gateway/response/finalization.ts', import.meta.url), 'utf8')
assert.doesNotMatch(finalizationSource, /errorMessage:\s*error instanceof Error \? error\.message : String\(error\)/u, 'SSE 取消失败日志不得直接写入未受限错误文本')
assert.match(finalizationSource, /logger\.debug\(errorLogFields\(error, \{/u, 'SSE 取消失败日志必须走统一有界错误字段')

console.log('日志回归通过：凭据原文保持不变，普通 Error 和 Pino 实际输出均只保留有界摘要')

function assertMarkersPresent(value: string, markers: string[], label: string): void {
  for (const marker of markers) {
    assert(value.includes(marker), `${label}应包含原始值：${marker}`)
  }
}
