import { strict as assert } from 'node:assert'

import { errorLogFields, redactSensitiveLogTextForTest } from '../../shared/logger.js'

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

console.log('日志原文回归通过：结构化字段、Error message/stack 和字符串异常保留常见凭据原文')

function assertMarkersPresent(value: string, markers: string[], label: string): void {
  for (const marker of markers) {
    assert(value.includes(marker), `${label}应包含原始值：${marker}`)
  }
}
