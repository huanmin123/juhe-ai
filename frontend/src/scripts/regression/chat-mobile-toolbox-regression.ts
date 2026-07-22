import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const layoutSource = readFileSync(new URL('../../layouts/AppLayout.vue', import.meta.url), 'utf8')
const viewSource = readFileSync(new URL('../../views/chat/ChatView.vue', import.meta.url), 'utf8')
const composerSource = readFileSync(new URL('../../views/chat/composer/AIComposer.vue', import.meta.url), 'utf8')

assert.match(layoutSource, /id="immersive-mobile-tools" class="immersive-mobile-tool-stack"/, '沉浸移动布局必须提供稳定的工具 Teleport 目标')
assert.match(layoutSource, /<div v-if="immersiveLayout" id="immersive-mobile-tools"/, 'Teleport 目标必须在沉浸布局首次渲染时稳定存在，不能等待移动端 mounted 后再创建')
assert.doesNotMatch(layoutSource, /<div v-if="immersiveLayout\s*&&\s*isMobile" id="immersive-mobile-tools"/, 'Teleport 目标不得与 ChatView 同时按移动态延迟创建，否则会丢失会话入口')
assert.match(layoutSource, /id="immersive-mobile-tools"[\s\S]{0,1000}class="immersive-mobile-tool immersive-mobile-menu-trigger"[\s\S]{0,120}打开系统菜单/, '系统菜单必须是统一移动工具组中的第一个按钮')
assert.match(layoutSource, /\.immersive-mobile-tool-stack\s*\{[\s\S]{0,260}position:\s*fixed[\s\S]{0,260}left:/, '移动工具组必须固定在顶部安全区并与系统菜单并列')
assert.match(viewSource, /<Teleport v-if="mobile" to="#immersive-mobile-tools">[\s\S]{0,260}打开对话记录/, '移动端对话记录必须从输入框迁移到应用壳浮动工具组')
assert.doesNotMatch(viewSource, /show-conversation-button|open-conversations/, 'ChatView 不得继续通过输入框承载会话入口')
assert.match(viewSource, /:mobile="mobile"/, 'ChatView 必须向编辑器传递移动端工具箱状态')
assert.match(composerSource, /<a-dropdown :trigger="\['click'\]"[\s\S]{0,650}key="image"[\s\S]{0,240}添加图片/, '桌面与移动端共用的工具箱必须提供添加图片动作')
assert.match(composerSource, /imageToolDisabledReason[\s\S]{0,240}当前模型不支持图片输入/, '不支持图片时必须保留工具入口并提供中文原因')
assert.doesNotMatch(composerSource, /filter\(\(item\) => props\.imageInputSupported \|\| item\.key !== 'image'\)/, '/image 不得因模型不支持而从命令菜单消失')
assert.match(composerSource, /class="ai-composer-toolbox-trigger"[^>]*aria-label="打开工具箱"/, '工具箱触发器必须是可访问的图标按钮')
assert.match(composerSource, /function openImagePicker\(\)[\s\S]{0,280}fileInput\.value\?\.click\(\)/, '添加图片必须复用既有隐藏文件输入和上传链路')
assert.doesNotMatch(composerSource, /showConversationButton|open-conversations/, 'AIComposer 不得保留旧会话入口属性或事件')
assert.match(composerSource, /@media \(pointer:\s*coarse\)[\s\S]{0,650}\.ai-composer-footer :deep\(\.ant-btn\)[^}]*min-width:\s*44px/, '移动工具箱按钮必须满足粗指针 44px 命中区')

console.log('AI 问答移动端浮动会话入口与图片工具箱回归通过')
