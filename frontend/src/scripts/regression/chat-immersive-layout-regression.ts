import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const layoutSource = readFileSync('../frontend/src/layouts/AppLayout.vue', 'utf8')
const routerSource = readFileSync('../frontend/src/router/index.ts', 'utf8')
const chatSource = readFileSync('../frontend/src/views/chat/ChatView.vue', 'utf8')
const messageListSource = readFileSync('../frontend/src/views/chat/ChatMessageList.vue', 'utf8')

assert.match(routerSource, /path:\s*'\/my-chat'[\s\S]{0,400}immersive:\s*true/, 'AI 问答路由必须声明沉浸布局')
assert.match(layoutSource, /<AppHeader\s+v-if="!immersiveLayout"/, '沉浸布局必须隐藏全局页面标题和说明')
assert.match(layoutSource, /:class="\{\s*'content-immersive':\s*immersiveLayout\s*\}"/, '内容区必须按路由切换沉浸样式')
assert.match(layoutSource, /\.content-immersive\s*\{[\s\S]{0,180}padding:\s*0/, '沉浸内容区不得保留管理页外边距')
assert.match(layoutSource, /v-if="immersiveLayout\s*&&\s*isMobile"[\s\S]{0,300}aria-label="打开系统菜单"/, '移动端沉浸布局必须保留紧凑系统菜单入口')
assert.match(layoutSource, /key="navigation"[\s\S]{0,100}打开导航/, '移动端沉浸菜单必须能打开全局导航')
assert.match(layoutSource, /handleImmersiveMenuClick[\s\S]{0,500}handleUserMenuClick/, '移动端沉浸菜单必须复用用户操作并支持退出登录')
assert.match(messageListSource, /message-row\[data-index="0"\][^}]*padding-top:\s*calc\(64px \+ env\(safe-area-inset-top\)\)/, '移动端首条消息的实测高度必须包含悬浮系统菜单和顶部安全区')
assert.doesNotMatch(chatSource, /message-virtual-space[^}]*margin-top/s, '不得用虚拟空间外部 margin 制造顶部避让')
assert.match(chatSource, /\.chat-workspace\s*\{[\s\S]{0,180}height:\s*var\(--app-visual-viewport-height,\s*100dvh\)/, '聊天工作区必须跟随 visualViewport，避免软键盘遮挡输入框')
assert.doesNotMatch(chatSource, /\.chat-workspace\s*\{[^}]*min-height:\s*(?:440|520)px/s, '聊天工作区不得用固定最小高度抵消动态视口')
assert.match(chatSource, /\.chat-workspace\s*\{[\s\S]{0,280}border:\s*0/, '聊天工作区不得继续呈现外层卡片边框')

console.log('AI 问答沉浸布局回归通过')
