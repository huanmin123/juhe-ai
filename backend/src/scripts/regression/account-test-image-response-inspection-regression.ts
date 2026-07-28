import assert from 'node:assert/strict'

import {
  accountTestImageEnvelopeScanMaxChars,
  inspectAccountTestImageResponseEnvelope
} from '../../modules/accounts/account-test-image-response-inspection.js'

assert.equal(inspectAccountTestImageResponseEnvelope('{"created":1,"data":[{"b64_json":"aGVsbG8="}]}', false).successEvidence, true, 'Images JSON 必须识别 data[].b64_json')
assert.equal(inspectAccountTestImageResponseEnvelope('{"data":[{"url":"https://images.example/result.png"}]}', false).successEvidence, true, 'Images JSON 必须识别 data[].url')

for (const invalidBody of [
  '',
  '<html>ok</html>',
  '{invalid json',
  '{"data":[]}',
  '{"data":[{"b64_json":"!!!!"}]}',
  '{"data":[{"b64_json":"aGVsbG8="}],}'
]) {
  assert.equal(inspectAccountTestImageResponseEnvelope(invalidBody, false).successEvidence, false, `无效 Images 响应不得判定成功：${invalidBody.slice(0, 40)}`)
}

const imageErrorInspection = inspectAccountTestImageResponseEnvelope(JSON.stringify({
  error: { code: 'invalid_request_error', message: 'image request failed' },
  data: [{ b64_json: 'aGVsbG8=' }]
}), false)
assert.equal(imageErrorInspection.successEvidence, false, '顶层 error 即使同时带 data 也不得判定 Images 探针成功')
assert.equal(imageErrorInspection.errorCode, 'invalid_request_error')
assert.equal(imageErrorInspection.errorMessage, 'image request failed')

const truncatedImagePrefix = '{"created":1,"data":[{"b64_json":"'
const truncatedImageBody = `${truncatedImagePrefix}${'A'.repeat(accountTestImageEnvelopeScanMaxChars - truncatedImagePrefix.length)}\n[truncated]`
const originalJsonParse = JSON.parse
let imageInspectionJsonParseCount = 0
JSON.parse = ((text: string, reviver?: (this: unknown, key: string, value: unknown) => unknown) => {
  imageInspectionJsonParseCount += 1
  return originalJsonParse(text, reviver)
}) as typeof JSON.parse
let largeImageInspection: ReturnType<typeof inspectAccountTestImageResponseEnvelope>
try {
  largeImageInspection = inspectAccountTestImageResponseEnvelope(truncatedImageBody, true)
} finally {
  JSON.parse = originalJsonParse
}
assert.equal(largeImageInspection.successEvidence, true, '已看到有效 b64_json 前缀的 256 KiB 截断 Images 响应应构成成功证据')
assert.equal(imageInspectionJsonParseCount, 0, 'Images envelope 扫描不得调用 JSON.parse 物化巨大 base64')
assert.equal(largeImageInspection.scannedCharacters, accountTestImageEnvelopeScanMaxChars, 'Images envelope 扫描必须受 256 KiB 上限约束')
assert(largeImageInspection.imagePayloadCharactersScanned > 250 * 1024, '大图片回归必须真实扫描足量 base64 字符')
assert(JSON.stringify(largeImageInspection).length < 256, 'Images 扫描结果不得复制或返回 base64 正文')

console.log('账户图片诊断响应回归通过：Images envelope 语义正确且大 base64 仅做有界零物化扫描')
