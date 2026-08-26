import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { clampChatFloatingMenuPosition, resolveChatViewportHeight, resolveChatVisualViewportBounds } from '../../views/chat/chatViewport'
import { resolveChatViewportResizeTransition, shouldDetachChatFollowOnScroll, shouldFollowChatViewportResize } from '../../views/chat/chatScrollPolicy'
import { planChatCreateDialogFromConversationPane } from '../../views/chat/chatConversationDrawer'

const layoutSource = readFileSync(new URL('../../layouts/AppLayout.vue', import.meta.url), 'utf8')
const globalSource = readFileSync(new URL('../../styles/global.css', import.meta.url), 'utf8')
const viewSource = readFileSync(new URL('../../views/chat/ChatView.vue', import.meta.url), 'utf8')
const messageListSource = readFileSync(new URL('../../views/chat/ChatMessageList.vue', import.meta.url), 'utf8')
const composerSource = readFileSync(new URL('../../views/chat/composer/AIComposer.vue', import.meta.url), 'utf8')
const imageSource = readFileSync(new URL('../../views/chat/composer/ChatImageAttachmentView.vue', import.meta.url), 'utf8')

assert.equal(resolveChatViewportHeight({ visualViewportHeight: 420, innerHeight: 844 }), 420)
assert.equal(resolveChatViewportHeight({ visualViewportHeight: 0, innerHeight: 800 }), 800)
assert.equal(resolveChatViewportHeight({ visualViewportHeight: Number.NaN, innerHeight: 0 }), undefined)
const visualBounds = resolveChatVisualViewportBounds({ offsetLeft: 20, offsetTop: 100, width: 360, height: 420, innerWidth: 800, innerHeight: 900 })
assert.deepEqual(visualBounds, { left: 20, top: 100, right: 380, bottom: 520, width: 360, height: 420 })
assert.deepEqual(clampChatFloatingMenuPosition({ preferredX: 370, preferredY: 510, menuWidth: 136, menuHeight: 188, viewport: visualBounds, padding: 8 }), { x: 236, y: 324 }, '菜单必须限制在 visualViewport 内')
assert.deepEqual(planChatCreateDialogFromConversationPane({ mobile: true, drawerOpen: true }), { closeDrawer: true, openDialogNow: false }, '手机抽屉新建必须先关闭抽屉')
assert.deepEqual(planChatCreateDialogFromConversationPane({ mobile: false, drawerOpen: false }), { closeDrawer: false, openDialogNow: true }, '桌面对话面板新建必须立即打开')

assert.equal(shouldFollowChatViewportResize({ wasFollowing: true, userDetached: false }), true, '贴底时键盘或 composer 变高后必须继续贴底')
assert.equal(shouldFollowChatViewportResize({ wasFollowing: true, userDetached: true }), false, '用户主动离底后尺寸变化不得抢滚')
assert.equal(shouldFollowChatViewportResize({ wasFollowing: false, userDetached: false }), false)
const followingResize = resolveChatViewportResizeTransition({ followLatest: true, userDetached: false })
assert.deepEqual(followingResize, { followLatest: true, userDetached: false, shouldScroll: true }, '跟随中 viewport 增高仍必须继续跟随')
assert.equal(shouldDetachChatFollowOnScroll({ previousOffset: 600, currentOffset: 520, now: 1_100, programmaticScrollUntil: 1_250 }), false, 'viewport 增高造成的程序向上滚不能误判用户离底')
const detachedResize = resolveChatViewportResizeTransition({ followLatest: false, userDetached: true })
assert.deepEqual(detachedResize, { followLatest: false, userDetached: true, shouldScroll: false }, '用户离底后 resize 不能恢复跟随')

assert.match(layoutSource, /visualViewport[\s\S]{0,700}--app-visual-viewport-height/, 'AppLayout 必须把 visualViewport 高度同步为 CSS 变量')
assert.match(layoutSource, /immersive-layout-active/, '沉浸聊天必须锁定 body 外层滚动')
assert.match(globalSource, /body\.immersive-layout-active[\s\S]{0,260}overflow:\s*hidden/, '沉浸聊天 body 不得外滚')
assert.match(viewSource, /height:\s*var\(--app-visual-viewport-height,\s*100dvh\)/, '聊天高度必须使用动态视口变量')
assert.doesNotMatch(viewSource, /\.chat-workspace\s*\{[^}]*min-height:\s*(?:440|520)px/s, '固定最小高度会抵消软键盘动态视口')
assert.match(viewSource, /env\(safe-area-inset-bottom/, '输入区必须避让底部安全区')
assert.match(viewSource, /env\(safe-area-inset-left/, '输入区必须避让横屏左侧安全区')
assert.match(viewSource, /env\(safe-area-inset-right/, '输入区必须避让横屏右侧安全区')

assert.match(viewSource, /conversation-more-button/, '手机会话行必须有可见更多入口')
assert.match(viewSource, /openConversationMenuFromButton/, '更多入口必须能打开重命名、置顶、详情、删除菜单')
assert.match(viewSource, /createConversation[\s\S]{0,500}conversationDrawerOpen\.value\s*=\s*false/, '手机新建成功后必须关闭会话抽屉')
assert.match(viewSource, /selectConversation\(item\.id\)[\s\S]{0,120}emit\('selected'\)/, '手机选择会话成功后必须关闭抽屉')
assert.match(viewSource, /<a-drawer[^>]*@after-open-change="handleConversationDrawerAfterOpenChange"/, '必须等待 Drawer 关闭动画结束')
assert.match(viewSource, /conversation-new-button[\s\S]{0,220}onClick:\s*createConversationFromPane/, '会话面板新建必须走抽屉协调入口')
assert.match(viewSource, /function createConversationFromPane[\s\S]{0,420}pendingCreateAfterDrawerClose\.value\s*=\s*true[\s\S]{0,180}conversationDrawerOpen\.value\s*=\s*false[\s\S]{0,80}return/, '手机入口必须先关闭 Drawer，再创建会话')
assert.match(viewSource, /function handleConversationDrawerAfterOpenChange\(open:\s*boolean\)[\s\S]{0,300}if \(open \|\| !pendingCreateAfterDrawerClose\.value\) return[\s\S]{0,180}createConversation\(\)/, 'Drawer 确认关闭后才能创建会话')
assert.doesNotMatch(viewSource, /function createConversationFromPane[\s\S]{0,420}conversationDrawerOpen\.value\s*=\s*false[\s\S]{0,160}(?:nextTick|setTimeout|requestAnimationFrame)/, '不得用猜测延迟代替 Drawer afterClose')
assert.match(viewSource, /class="conversation-context-menu"[^>]*role="menu"[^>]*@keydown="handleConversationMenuKeyDown"/, '会话菜单必须声明 menu 语义并处理键盘')
assert.equal((viewSource.match(/<button[^>]*role="menuitem"/g) ?? []).length, 4, '四个会话动作都必须声明 menuitem')
assert.match(viewSource, /'aria-haspopup':\s*'menu'/, '更多按钮必须声明弹出菜单')
assert.match(viewSource, /'aria-expanded':\s*conversationMenu/, '更多按钮必须暴露展开状态')
assert.match(viewSource, /focusConversationMenu[\s\S]{0,260}querySelector<HTMLElement>\('\[role="menuitem"\]'\)[\s\S]{0,180}focus\(\)/, '菜单打开后必须聚焦首个菜单项')
assert.match(viewSource, /event\.key === 'Escape'[\s\S]{0,180}closeConversationMenu\(true\)/, 'Escape 必须关闭菜单并归还焦点')
assert.match(viewSource, /querySelector<HTMLElement>\('\.conversation-item-select'\)/, '桌面右键菜单也必须保存可聚焦的会话按钮用于 Escape 回焦')
assert.match(viewSource, /resolveChatVisualViewportBounds/, '菜单定位必须使用 visualViewport 边界')

assert.match(messageListSource, /resizeObserver\.observe\(scrollElement\.value\)/, '消息列表必须观察滚动容器可视高度')
assert.match(messageListSource, /resolveChatViewportResizeTransition/, '滚动容器变高或变矮必须遵守用户离底状态')
assert.match(messageListSource, /function followStream[\s\S]{0,220}programmaticScrollUntil\s*=/, '所有自动跟随都必须先标记程序滚动窗口')
assert.match(messageListSource, /shouldDetachChatFollowOnScroll/, '滚动事件必须统一识别程序滚动')
assert.doesNotMatch(viewSource, /message-virtual-space[^}]*margin-top/s, '虚拟空间外部 margin 会破坏 distance 与 virtualizer 尺寸口径')
assert.match(messageListSource, /message-row\[data-index="0"\][^}]*padding-top:/s, '顶部菜单避让必须进入索引 0 消息的测量高度')

for (const [source, selector] of [
  [layoutSource, 'immersive-mobile-menu-trigger'],
  [viewSource, 'conversation-new-button'],
  [viewSource, 'conversation-item-select'],
  [viewSource, 'conversation-load-more'],
  [viewSource, 'conversation-more-button'],
  [composerSource, 'ai-composer-context'],
  [composerSource, 'ai-composer-footer'],
  [messageListSource, 'message-action-button'],
] as const) {
  assert.match(source, new RegExp(`@media[^}]+pointer:\\s*coarse[\\s\\S]+?\\.${selector}[\\s\\S]{0,500}(?:44px|inset:\\s*-[1-9])`), `${selector} 粗指针命中区必须至少 44px`)
}
assert.match(imageSource, /@media[^}]+pointer:\s*coarse[\s\S]+?(?:chat-image-node-status button|chat-image-node-remove)[\s\S]{0,500}(?:44px|inset:\s*-[1-9])/, '图片重试和删除必须扩大粗指针命中区')
assert.match(composerSource, /@media \(pointer:\s*coarse\)[\s\S]*?\.ai-composer-model-controls :deep\(\.ant-select-selector\)[^}]*min-height:\s*44px/s, '三个小尺寸 Select selector 必须达到 44px')
assert.match(composerSource, /@media \(pointer:\s*coarse\)[\s\S]*?\.ai-composer-model-controls :deep\(\.ant-select-selection-search-input\)[^}]*height:\s*44px/s, 'Select 搜索输入必须达到 44px')
for (const bar of ['turn-editing-bar', 'turn-limit-bar', 'submission-confirmation-bar']) {
  assert.match(viewSource, new RegExp(`@media \\(pointer:\\s*coarse\\)[\\s\\S]*?\\.${bar} :deep\\(\\.ant-btn\\)[^}]*min-height:\\s*44px`), `${bar} 小按钮必须达到 44px`)
}

console.log('chat mobile interaction regression passed')
