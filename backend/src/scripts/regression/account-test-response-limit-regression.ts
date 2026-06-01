import { strict as assert } from 'node:assert'

import { MemoryGatewayResponse, accountTestResponsePreviewBytes } from '../../modules/accounts/account-test.service.js'

const omittedTailMarker = 'account_test_response_limit_tail'

const response = new MemoryGatewayResponse(Date.now())
response.write('data: {"type":"response.created","response":{"id":"resp_limit","status":"in_progress"}}\n\n')
response.write('data: {"type":"response.output_text.delta","delta":"O')
response.write('K"}\n\n')
response.write('x'.repeat(accountTestResponsePreviewBytes + 64 * 1024))
response.write(omittedTailMarker)
response.end()

const responseText = response.bodyText()
assert.equal(response.bodyTruncated(), true, '账户测试响应体超过 256 KiB 后应标记截断')
assert(responseText.includes('响应体过大，已截断'), '截断响应文本应包含中文提示')
assert(!responseText.includes(omittedTailMarker), '截断响应文本不应保留超过上限后的尾部内容')
assert(Buffer.byteLength(responseText, 'utf8') < accountTestResponsePreviewBytes + 1024, '截断响应文本应控制在 256 KiB 附近')
assert.equal(typeof response.firstTokenMs(), 'number', '截断前的 SSE 输出仍应被流式检查器识别首 token')

console.log('账户测试响应体上限回归通过：异常大响应只保留有界预览，且不影响首 token 识别')
