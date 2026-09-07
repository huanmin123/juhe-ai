import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('../../modules/background/background-ipc.ts', import.meta.url), 'utf8')
assert.equal(source.includes('background_worker_runtime_log_line'), false, '运行日志不得进入 background IPC')
assert.equal(source.includes('sendRuntimeLogLineToWorker'), false, '运行日志不得通过 IPC 投递')

console.log('后台 IPC 队列回归通过：运行日志不进入 IPC，其他记录队列继续由各自队列测试覆盖')
