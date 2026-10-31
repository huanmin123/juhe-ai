import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { consumePlaygroundSseResponse } from '../../api/domains/playground'
import type { PlaygroundAccount, PlaygroundStreamEvent } from '../../types/domain/playground'
import { firstModelIdOf, resolveModelsForKind } from '../../views/playground/usePlaygroundCatalog'

// 演练场（docs/functions/演练场设计.md §6.2）回归：
// 路由/菜单注册、账户→模型联动、SSE 事件解析（done/error/tool + 跨 chunk 分帧）、
// 零会话持久化断言（消息只存页面内存，无 localStorage / 无会话 API / 无 /my-chat 引用）。

// —— 1) 路由与菜单注册 ——
const routerSource = readFileSync(new URL('../../router/index.ts', import.meta.url), 'utf8')
const layoutSource = readFileSync(new URL('../../layouts/AppLayout.vue', import.meta.url), 'utf8')

const playgroundRoutes = [
  ['/playground/chat', '对话试玩'],
  ['/playground/images', '图片生成'],
  ['/playground/videos', '视频生成'],
  ['/playground/speech', '语音合成']
] as const

for (const [path, title] of playgroundRoutes) {
  const routePattern = new RegExp(`path: '${path}'[\\s\\S]{0,500}menuGroup: 'playground'`)
  assert.match(routerSource, routePattern, `演练场路由 ${path} 必须注册并归入 menuGroup playground`)
  assert.match(routerSource, new RegExp(`path: '${path}'[\\s\\S]{0,500}menuGroupTitle: '演练场'`), `演练场路由 ${path} 必须挂「演练场」组标题`)
  assert.match(routerSource, new RegExp(`path: '${path}'[\\s\\S]{0,500}viewScope: 'self'`), `演练场路由 ${path} 必须是用户面（viewScope self）`)
  assert.match(routerSource, new RegExp(`path: '${path}'[\\s\\S]{0,500}keepAlive: true`), `演练场路由 ${path} 必须启用 KeepAlive 保留页内内存态`)
  assert.match(routerSource, new RegExp(`'${title.replace(/'/g, "\\'")}'`), `演练场路由 ${path} 必须使用标题「${title}」`)
}

for (const [path, icon] of [
  ['/playground/chat', 'ExperimentOutlined'],
  ['/playground/images', 'PictureOutlined'],
  ['/playground/videos', 'VideoCameraOutlined'],
  ['/playground/speech', 'AudioOutlined']
] as const) {
  assert.match(layoutSource, new RegExp(`'${path}':\\s*${icon}`), `演练场菜单项 ${path} 必须配置图标 ${icon}`)
}
assert.match(layoutSource, /AudioOutlined,/, '演练场语音菜单图标依赖 AudioOutlined 导入')

// —— 2) 零持久化与零会话 API 断言（四个演练场页一律不落盘、不触会话面）——
const viewFileByPath: Record<(typeof playgroundRoutes)[number][0], string> = {
  '/playground/chat': 'PlaygroundChatView.vue',
  '/playground/images': 'PlaygroundImageView.vue',
  '/playground/videos': 'PlaygroundVideoView.vue',
  '/playground/speech': 'PlaygroundSpeechView.vue'
}
const viewSources = Object.fromEntries(playgroundRoutes.map(([path]) => [
  path,
  readFileSync(new URL(`../../views/playground/${viewFileByPath[path]}`, import.meta.url), 'utf8')
]))

for (const [path, source] of Object.entries(viewSources)) {
  assert.doesNotMatch(source, /localStorage|sessionStorage|indexedDB/i, `演练场页 ${path} 不得出现任何本地持久化引用`)
  assert.doesNotMatch(source, /\/my-chat/, `演练场页 ${path} 不得引用即将退役的 /my-chat 面`)
}

const chatViewSource = viewSources['/playground/chat']
assert.doesNotMatch(chatViewSource, /api\.chat\b/, '对话试玩不得引用即将退役的 chat API 域（api.playground.chatStream 才是合法入口）')
assert.match(chatViewSource, /api\.playground\.chatStream/, '对话试玩必须走 playground chatStream')
assert.match(chatViewSource, /@change="handleAccountChange"/, '账户→模型联动必须经 handleAccountChange 重置模型选择')
assert.match(chatViewSource, /streamError/, 'error 帧必须渲染为消息区内错误条（streamError）')

const playgroundApiSource = readFileSync(new URL('../../api/domains/playground.ts', import.meta.url), 'utf8')
assert.doesNotMatch(playgroundApiSource, /my-chat/, 'playground API 域不得引用 /my-chat 路径')

// 视频页轮询熔断（源码形态断言，视图组件无组件测试）：连续失败 ≥5 次停止
// 轮询并置终态错误提示（手动刷新重试）。
const videoViewSource = viewSources['/playground/videos']
assert.match(videoViewSource, /const pollMaxFailures = 5/, '视频页必须定义连续轮询失败熔断阈值 5（pollMaxFailures）')
assert.match(videoViewSource, /pollFailureCount \+= 1[\s\S]{0,400}if \(pollFailureCount >= pollMaxFailures\)[\s\S]{0,400}stopPolling\(\)[\s\S]{0,400}pollBroken\.value = true/, '视频页轮询连续失败达阈值必须停止轮询并置终态错误（pollBroken）')
assert.match(videoViewSource, /手动刷新/, '视频页熔断提示必须包含手动刷新重试指引')

// —— 3) SSE 事件解析行为回放（tool / done / error + 跨 chunk 分帧）——
function sseResponseFromChunks(chunks: string[]): Response {
  const encoder = new TextEncoder()
  let index = 0
  const reader = {
    read: async () => (index < chunks.length
      ? { done: false, value: encoder.encode(chunks[index++]) }
      : { done: true, value: undefined }),
    cancel: async () => undefined,
    releaseLock: () => undefined
  }
  return {
    ok: true,
    status: 200,
    text: async () => '',
    clone: () => {
      throw new Error('回归回放不需要 clone')
    },
    body: {
      getReader: () => reader
    }
  } as unknown as Response
}

const events: PlaygroundStreamEvent[] = []
await consumePlaygroundSseResponse(
  sseResponseFromChunks([
    'data: {"type":"round","round":1}\n\ndata: {"type":"tool_start","callId":"c1","name":"fetch_url","args":"{\\"url\\":\\"https://example.com\\"}"}\n\n',
    'data: {"typ',
    'e":"tool_end","callId":"c1","name":"fetch_url","ok":true,"summary":"Example Domain"}\n\n',
    'data: {"type":"delta","text":"正文"}\n\ndata: {"type":"done","rounds":2}\n\n'
  ]),
  '/playground/chat/stream',
  (event) => events.push(event)
)
assert.deepEqual(events.map((event) => event.type), ['round', 'tool_start', 'tool_end', 'delta', 'done'])
assert.deepEqual(events[1], { type: 'tool_start', callId: 'c1', name: 'fetch_url', args: '{"url":"https://example.com"}' })
assert.deepEqual(events[2], { type: 'tool_end', callId: 'c1', name: 'fetch_url', ok: true, summary: 'Example Domain' })
assert.deepEqual(events[4], { type: 'done', rounds: 2 })

const errorEvents: PlaygroundStreamEvent[] = []
await consumePlaygroundSseResponse(
  sseResponseFromChunks([
    'data: {"type":"delta","text":"部分输出"}\n\n',
    'data: {"type":"error","code":"playground_dispatch_failed","message":"上游失败"}\n\n'
  ]),
  '/playground/chat/stream',
  (event) => errorEvents.push(event)
)
assert.equal(errorEvents.length, 2)
const errorEvent: PlaygroundStreamEvent = errorEvents[1]
if (errorEvent.type !== 'error') {
  throw new Error('error 帧必须以事件形式透出，实际收到：' + errorEvent.type)
}
assert.equal(errorEvent.code, 'playground_dispatch_failed')
assert.equal(errorEvent.message, '上游失败')

// —— 4) 账户→模型联动纯函数（按 kind 过滤 + 默认选中第一个）——
const fixtureAccount: PlaygroundAccount = {
  accountId: 'acc_1',
  accountName: '演示账户',
  providerCode: 'gpt',
  models: [
    { modelId: 'gpt-5.4', kind: 'chat', functionCalling: true },
    { modelId: 'gpt-image-2', kind: 'image' },
    { modelId: 'grok-imagine-video-1.5', kind: 'video' },
    { modelId: 'speech-2.8-hd', kind: 'audio' }
  ]
}
assert.deepEqual(resolveModelsForKind(fixtureAccount, 'chat').map((model) => model.modelId), ['gpt-5.4'])
assert.deepEqual(resolveModelsForKind(fixtureAccount, 'video').map((model) => model.modelId), ['grok-imagine-video-1.5'])
assert.deepEqual(resolveModelsForKind(undefined, 'chat'), [])
assert.equal(firstModelIdOf(resolveModelsForKind(fixtureAccount, 'image')), 'gpt-image-2')
assert.equal(firstModelIdOf([]), undefined)

console.log('演练场路由注册、SSE 事件解析、账户模型联动与零持久化回归通过')
