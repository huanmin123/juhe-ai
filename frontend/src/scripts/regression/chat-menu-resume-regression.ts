import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { reconcileChatSubmission } from '../../views/chat/chatTurnReconciliation'

const acceptedStops: string[] = []
const accepted = await reconcileChatSubmission({
  initialAcceptedTurnId: 'turn_running',
  initialAssistantStatus: 'streaming',
  getSubmissionStatus: async () => { throw { response: { status: 503 } } },
  listMessages: async () => [],
  stop: async (turnId) => { acceptedStops.push(turnId) },
  wait: async () => undefined,
  maxAttempts: 2
})
assert.equal(accepted.accepted, true)
assert.deepEqual(acceptedStops, [], 'accepted + streaming 遇到临时错误必须交给应用级 runtime 重连，绝不自动 stop')

const viewSource = readFileSync(new URL('../../views/chat/ChatView.vue', import.meta.url), 'utf8')
const routerSource = readFileSync(new URL('../../router/index.ts', import.meta.url), 'utf8')
const authSource = readFileSync(new URL('../../composables/useAuth.ts', import.meta.url), 'utf8')
const mainSource = readFileSync(new URL('../../main.ts', import.meta.url), 'utf8')
const layoutSource = readFileSync(new URL('../../layouts/AppLayout.vue', import.meta.url), 'utf8')

assert.match(routerSource, /path:\s*'\/my-chat'[\s\S]{0,300}keepAlive:\s*true/, 'AI 问答路由必须启用 KeepAlive')
assert.match(viewSource, /onActivated/, '返回 AI 问答时必须恢复页面监听与轻量同步')
assert.match(viewSource, /onDeactivated/, '离开 AI 问答时必须只暂停页面监听和定时器')
assert.doesNotMatch(viewSource, /onBeforeUnmount\([\s\S]{0,900}streamController\?\.abort\(\)/, '页面卸载不得中止已接受生成')
assert.match(viewSource, /chatGenerationRuntime\.subscribe/, '页面必须订阅应用级生成运行态')
assert.match(viewSource, /chatGenerationRuntime\.start/, '发送必须交给应用级生成运行态')
assert.match(mainSource, /chatGenerationRuntime[\s\S]{0,500}(?:activateAccount|switchAccount)/, '应用入口必须根据登录账户切换 runtime 身份')
assert.match(authSource, /clearAccount/, '明确退出登录必须清理当前账户聊天缓存')
assert.match(layoutSource, /clearCurrentAccountChatState/, '撤销当前会话必须执行与明确退出相同的聊天清理')

console.log('AI 问答菜单切换续跑、KeepAlive 与身份清理回归通过')
