import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const source = readFileSync(resolve('src/modules/model-checks/model-checks.routes.ts'), 'utf8')

assert.match(source, /attachDownstreamResponseErrorBoundary/, '流式模型检测必须在首个 SSE 写入前安装下游响应错误边界')
assert.match(source, /event: 'model_check_stream_downstream_response_error'/, '流式模型检测响应错误必须记录独立事件')
assert.match(source, /epipeSource: errorCode === 'EPIPE' \? 'model_check_sse' : undefined/, '流式模型检测 EPIPE 必须记录固定来源')
assert.ok(
  source.indexOf('const detachDownstreamResponseErrorBoundary = attachDownstreamResponseErrorBoundary') < source.indexOf("res.write(': connected\\n\\n')"),
  '响应边界必须在首个连接事件写入前安装'
)

console.log('model check SSE error boundary regression passed')
