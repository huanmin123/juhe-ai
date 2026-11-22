import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import type { InternalAxiosRequestConfig } from 'axios'

import { consumePlaygroundSseResponse, playgroundApi } from '../../api/domains/playground'
import { http } from '../../api/http'
import type { PlaygroundModelOption, PlaygroundStreamEvent } from '../../types/domain/playground'
import { firstModelIdOf, resolveModelsForKind } from '../../views/playground/usePlaygroundCatalog'

// 演练场（docs/functions/演练场设计.md §6.2）回归：
// 路由/菜单注册（v1.1 六页）、2026-11-22 Key 化：API Key→模型两级联动（六 kind，只展示
// 本页能力域）、SSE 事件解析（delta/usage/error + 跨 chunk 分帧，round/tool/done 退役）、
// embeddings/rerank 请求形状（apiKeyId 载荷）与 unwrap、零会话持久化断言（消息与
// 向量/重排结果只存页面内存，无 localStorage / 无会话 API / 无 /my-chat 引用）；
// v1.2 图像工作台：快速入口、占位卡片、3s 轮询 + 熔断、删除倒计时、已删除图像
// 读时派生、终态回填、active 筛选与参考图上传（同步 images 已退役断言）；
// v1.5 卡片操作区（复审二改定案）：状态徽标维持 v1.3 原位叠加缩略图左上、仅下载/
// 删除移出图像区为操作行图标按钮（删除 a-popconfirm 二次确认 + imagesJobRemove DELETE，§3.4.6）。

// —— 1) 路由与菜单注册 ——
const routerSource = readFileSync(new URL('../../router/index.ts', import.meta.url), 'utf8')
const layoutSource = readFileSync(new URL('../../layouts/AppLayout.vue', import.meta.url), 'utf8')

const playgroundRoutes = [
  ['/playground/chat', '对话试玩'],
  ['/playground/images', '图片生成'],
  ['/playground/videos', '视频生成'],
  ['/playground/speech', '语音合成'],
  ['/playground/embeddings', '向量生成'],
  ['/playground/rerank', '重排测试']
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
  ['/playground/speech', 'AudioOutlined'],
  ['/playground/embeddings', 'ClusterOutlined'],
  ['/playground/rerank', 'SortAscendingOutlined']
] as const) {
  assert.match(layoutSource, new RegExp(`'${path}':\\s*${icon}`), `演练场菜单项 ${path} 必须配置图标 ${icon}`)
}
assert.match(layoutSource, /AudioOutlined,/, '演练场语音菜单图标依赖 AudioOutlined 导入')
assert.match(layoutSource, /ClusterOutlined,/, '演练场向量菜单图标依赖 ClusterOutlined 导入')
assert.match(layoutSource, /SortAscendingOutlined,/, '演练场重排菜单图标依赖 SortAscendingOutlined 导入')

// —— 2) 零持久化与零会话 API 断言（六个演练场页一律不落盘、不触会话面）——
const viewFileByPath: Record<(typeof playgroundRoutes)[number][0], string> = {
  '/playground/chat': 'PlaygroundChatView.vue',
  '/playground/images': 'PlaygroundImageView.vue',
  '/playground/videos': 'PlaygroundVideoView.vue',
  '/playground/speech': 'PlaygroundSpeechView.vue',
  '/playground/embeddings': 'PlaygroundEmbeddingsView.vue',
  '/playground/rerank': 'PlaygroundRerankView.vue'
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
assert.match(chatViewSource, /@change="handleApiKeyChange"/, 'Key→模型联动必须经 handleApiKeyChange 重置模型选择')
assert.match(chatViewSource, /streamError/, 'error 帧必须渲染为消息区内错误条（streamError）')
assert.doesNotMatch(chatViewSource, /tool_start|tool_end|type: 'round'|playground-tool-timeline/, '工具循环退役（2026-11-22）：对话页不得残留 tool 时间线与 round 事件处理')

const playgroundApiSource = readFileSync(new URL('../../api/domains/playground.ts', import.meta.url), 'utf8')
assert.doesNotMatch(playgroundApiSource, /my-chat/, 'playground API 域不得引用 /my-chat 路径')
assert.match(playgroundApiSource, /keyModels\s*[:(]/, 'Key 化目录：API 域必须提供 keyModels（GET /playground/key-models）')
assert.doesNotMatch(playgroundApiSource, /\baccounts\s*[:(]/, '账户目录已退役（2026-11-22 Key 化）：API 域不得再有 accounts()')
for (const endpoint of ['imagesReferences', 'imagesCreateJob', 'imagesJobs', 'imagesJob'] as const) {
  assert.match(playgroundApiSource, new RegExp(`${endpoint}\\s*[:(]`), `图像工作台 API 域必须提供 ${endpoint}（v1.2 §3.4）`)
}
assert.doesNotMatch(playgroundApiSource, /\bimages:\s*(async|\()/, 'v1.0 同步 images() 已退役（§8），API 域不得再有该函数')
// v1.5 手动删除（§3.4.6）：DELETE /playground/images/jobs/{id}，jobId 经 encodeURIComponent，
// 成功 204 无响应体（断言锚定整行形状，天然排除 unwrap 包裹）。
assert.match(playgroundApiSource, /imagesJobRemove\s*[:(]/, '图像工作台 API 域必须提供 imagesJobRemove（v1.5 §3.4.6 手动删除）')
assert.match(playgroundApiSource, /imagesJobRemove: \(jobId: string\) => http\.delete\(`\/playground\/images\/jobs\/\$\{encodeURIComponent\(jobId\)\}`\)/, 'imagesJobRemove 必须 DELETE /playground/images/jobs/{id}（jobId 经 encodeURIComponent，成功 204 无响应体不 unwrap，§3.4.6）')

// 视频页轮询熔断（源码形态断言，视图组件无组件测试）：连续失败 ≥5 次停止
// 轮询并置终态错误提示（手动刷新重试）。
const videoViewSource = viewSources['/playground/videos']
assert.match(videoViewSource, /const pollMaxFailures = 5/, '视频页必须定义连续轮询失败熔断阈值 5（pollMaxFailures）')
assert.match(videoViewSource, /pollFailureCount \+= 1[\s\S]{0,400}if \(pollFailureCount >= pollMaxFailures\)[\s\S]{0,400}stopPolling\(\)[\s\S]{0,400}pollBroken\.value = true/, '视频页轮询连续失败达阈值必须停止轮询并置终态错误（pollBroken）')
assert.match(videoViewSource, /手动刷新/, '视频页熔断提示必须包含手动刷新重试指引')

// —— 2.0) 图像工作台页结构断言（v1.3 §4.2）：卡片式快速入口、统一高度卡片（不展示
// tokens/费用）、占位卡片、3s 轮询 + 5 次熔断、每分钟删除倒计时 tick、「已删除图像」
// 读时派生、终态卡片回填弹窗、active 筛选 ——
const imageViewSource = viewSources['/playground/images']
assert.match(imageViewSource, /class="image-quick-entry"[\s\S]{0,80}@click="openWorkbench"/, '快速入口必须是卡片式入口（非真实输入框），整卡点击经 openWorkbench 打开弹窗')
assert.doesNotMatch(imageViewSource, /costTextOf|image-card-usage|tokens · /, '演练场卡片不得展示费用与 tokens（v1.3 移除 image-card-usage 行与 costTextOf）')
assert.match(imageViewSource, /jobs\.value\.unshift\(makePlaceholderJob\(result\)\)/, '创建任务成功必须在历史网格顶部插入占位卡片（makePlaceholderJob）')
assert.match(imageViewSource, /const pollIntervalMs = 3000/, '图像任务轮询间隔必须为 3s（pollIntervalMs）')
assert.match(imageViewSource, /const pollMaxFailures = 5/, '图像任务轮询熔断阈值必须为 5（pollMaxFailures）')
assert.match(imageViewSource, /if \(failures >= pollMaxFailures\)[\s\S]{0,400}stopPollingJob\(jobId\)[\s\S]{0,300}message\.error\('生成任务状态查询连续失败/, '单任务轮询连续失败达阈值必须熔断停轮并提示恢复方式')
assert.match(imageViewSource, /tickTimer = setInterval\(\(\) => \{[\s\S]{0,120}nowTick\.value = Date\.now\(\)[\s\S]{0,60}\}, 60_000\)/, '删除倒计时必须每分钟 tick 刷新（nowTick 60_000 定时器）')
assert.match(imageViewSource, /function isCardDeleted\(job: PlaygroundImageJob\): boolean \{[\s\S]{0,200}job\.imageDeleted[\s\S]{0,200}job\.expiresAt \* 1000 <= nowTick\.value/, '「已删除图像」必须由后端标记 × 本地倒计时到期读时派生（isCardDeleted）')
assert.match(imageViewSource, /isCardDeleted\(job\) \? '已删除图像' : playgroundImageJobStatusLabels\[job\.status\]/, '图像删除后状态角标必须覆盖为「已删除图像」（statusLabelOf）')
assert.match(imageViewSource, /if \(isCardDeleted\(job\)\) return '图像已删除'/, '删除倒计时终点必须显示「图像已删除」（expiryTextOf）')
assert.match(imageViewSource, /后删除图像/, 'completed 未删卡片必须显示「X 天 X 时后删除图像」倒计时文案')
assert.match(imageViewSource, /function handleCardClick\(job: PlaygroundImageJob\): void \{[\s\S]{0,120}if \(isCardClickable\(job\)\) openWorkbenchWithJob\(job\)/, '点击终态卡片必须回填弹窗配置（handleCardClick → openWorkbenchWithJob）')
assert.match(imageViewSource, /job\.status === 'completed' \|\| job\.status === 'failed'/, '回填弹窗仅对终态卡片开放（isCardClickable）')
assert.match(imageViewSource, /\{ label: '生成中', value: 'active' \}/, '「生成中」筛选必须用契约派生态 active（= queued+running 一次覆盖），不得只筛 running')

// —— 2.0.2) v1.5 卡片操作区（§4.2/§3.4.6 用户裁决 + 复审二改定案）：状态徽标维持
// v1.3 原位叠加缩略图左上（定位样式照旧），仅下载/删除移出图像区为信息区末行操作行
// 图标按钮（删除带 danger 并经 a-popconfirm 二次确认），成功后停轮清计数、本地移除
// 卡片并 toast；信息区三块（提示词 → 标签 → 元信息+操作行），不新增独立状态行。
const imageViewThumbBlock = imageViewSource.slice(imageViewSource.indexOf('<div class="image-card-thumb">'), imageViewSource.indexOf('image-card-body'))
assert.match(imageViewThumbBlock, /<div class="image-card-thumb">[\s\S]{0,300}<span class="playground-status"/, '状态徽标必须维持 v1.3 原位叠加缩略图左上（thumb 块首元素，仅容许注释间隔，§4.2 v1.5 复审二改定案）')
assert.match(imageViewSource, /\.image-card-thumb > \.playground-status \{[\s\S]{0,160}position: absolute[\s\S]{0,120}top: 8px;[\s\S]{0,40}left: 8px;/, '缩略图状态角标必须保持左上角叠加定位（position absolute top/left 8px，v1.3 原位）')
assert.doesNotMatch(imageViewThumbBlock, /image-card-download|DownloadOutlined|DeleteOutlined|a-popconfirm/, 'v1.5 缩略图区只承载图像/占位与状态角标叠加，下载/删除按钮一律移出图像区')
assert.doesNotMatch(imageViewSource, /image-card-download/, 'v1.5 旧缩略图悬浮下载按钮及其样式已退役（下载改操作行图标按钮）')
assert.doesNotMatch(imageViewSource, /image-card-status-row|image-card-prompt-row/, '不得新增独立状态行或将徽标并入提示词行（v1.5 复审二改定案：维持 v1.3 原位叠加）')
assert.match(imageViewSource, /import \{ ArrowUpOutlined, DeleteOutlined, DownloadOutlined \} from '@ant-design\/icons-vue'/, '操作行图标依赖 DownloadOutlined 与 DeleteOutlined（v1.5）')
assert.match(imageViewSource, /class="image-card-actions"[\s\S]{0,400}<DownloadOutlined \/>/, '下载必须是信息区操作行 image-card-actions 的 DownloadOutlined 图标按钮（v1.5）')
assert.match(imageViewSource, /class="image-card-actions"[\s\S]{0,1200}<DeleteOutlined \/>/, '删除必须是信息区操作行 image-card-actions 的 DeleteOutlined 图标按钮（v1.5）')
assert.match(imageViewSource, /v-if="showImageThumb\(job\)"[\s\S]{0,300}downloadImage\(job\)[\s\S]{0,150}<DownloadOutlined \/>/, '下载入口仅在 completed 且未删（showImageThumb）时显示并触发 downloadImage（v1.5）')
assert.match(imageViewSource, /v-if="isJobTerminal\(job\)"[\s\S]{0,800}<DeleteOutlined \/>/, '删除入口仅终态（completed/failed，含图像已删卡片）显示（§3.4.6）')
assert.match(imageViewSource, /function isJobTerminal\(job: PlaygroundImageJob\): boolean \{[\s\S]{0,120}job\.status === 'completed' \|\| job\.status === 'failed'/, '终态判定必须覆盖 completed（含图像已删）与 failed（isJobTerminal，§3.4.6）')
assert.match(imageViewSource, /size="small"[\s\S]{0,80}danger[\s\S]{0,120}title="删除任务"/, '删除按钮必须带 danger 语义色（v1.5）')
assert.match(imageViewSource, /a-popconfirm[\s\S]{0,200}删除后任务记录与图像一并删除，不可恢复[\s\S]{0,200}@confirm="deleteJob\(job\)"/, '删除必须经 a-popconfirm 二次确认「删除后任务记录与图像一并删除，不可恢复」（§3.4.6）')
assert.match(imageViewSource, /async function deleteJob\(job: PlaygroundImageJob\): Promise<void> \{[\s\S]{0,200}imagesJobRemove\(job\.jobId\)[\s\S]{0,300}message\.success\('已删除'\)/, '删除确认必须调 imagesJobRemove，成功后清理轮询与本地移除卡片并 toast「已删除」（§3.4.6）')

// —— 2.0.1) Key 化卡片行（v1.4 §3.4.3/§3.4.4）：任务行携带 apiKeyId（历史筛选 + 卡片回填依据）——
const playgroundTypesSource = readFileSync(new URL('../../types/domain/playground.ts', import.meta.url), 'utf8')
assert.match(playgroundTypesSource, /interface PlaygroundImageJob \{[\s\S]{0,600}apiKeyId: string/, '图像任务卡片行必须携带 apiKeyId（v1.4 行投影：归属以创建时 Key 为准）')
assert.doesNotMatch(playgroundTypesSource, /accountId: string/, 'Key 化（v1.4）后演练场类型不得再有任何 accountId 字段（卡片行/请求载荷一律 apiKeyId）')
assert.match(imageViewSource, /apiKeyId: ''/, '图像占位卡片行字段必须同步 Key 化（apiKeyId 空串）')

const workbenchModalSource = readFileSync(new URL('../../views/playground/PlaygroundImageWorkbenchModal.vue', import.meta.url), 'utf8')
assert.match(workbenchModalSource, /const maxReferences = 10/, '参考图上限必须为 10 张（maxReferences）')
assert.match(workbenchModalSource, /const maxReferenceBytes = 10 \* 1024 \* 1024/, '单张参考图上限必须为 10MB（maxReferenceBytes）')
assert.match(workbenchModalSource, /api\.playground\.imagesReferences\(/, '参考图必须经 imagesReferences 逐张上传落服务端暂存（§3.4.1）')
assert.match(workbenchModalSource, /api\.playground\.imagesCreateJob\(/, '生成必须走 imagesCreateJob 异步任务创建（§3.4.2）')
assert.match(workbenchModalSource, /emit\('created', result\)/, '创建成功必须 emit created 由父页面插入占位卡片')
// v1.3 参数四项必选：无「默认」档,固定默认 16:9/1K/标准/PNG,创建恒传;演练场不展示费用。
assert.match(workbenchModalSource, /const aspectRatio = ref<PlaygroundImageAspectRatio>\('16:9'\)/, '画面比例必须固定默认 16:9（无「默认」档）')
assert.match(workbenchModalSource, /const resolution = ref<PlaygroundImageResolution>\('1k'\)/, '分辨率必须固定默认 1K（无「默认」档）')
assert.match(workbenchModalSource, /const quality = ref<PlaygroundImageQuality>\('standard'\)/, '生成质量必须固定默认标准（无「默认」档）')
assert.match(workbenchModalSource, /const outputFormat = ref<PlaygroundImageOutputFormat>\('png'\)/, '输出格式必须固定默认 PNG（无「默认」档）')
assert.doesNotMatch(workbenchModalSource, /label: '默认'/, '参数四项不得再有「默认」选项（v1.3 必选其一）')
assert.doesNotMatch(workbenchModalSource, /workbench-estimate|estimatedCostUsd|imageEstimate/, '生成设置不得展示费用（v1.3 裁决：演练场不展示 tokens/费用，2026-11-22 Key 化后继续有效）')
assert.match(workbenchModalSource, /@change="handleKeyChange"/, '生成设置 Key 下拉必须经 handleKeyChange 清占位态后联动重置')
assert.match(workbenchModalSource, /job\.apiKeyId/, '卡片回填 Key/模型必须以卡片 apiKeyId 为准（§4.2，不再使用当前/默认 Key）')
assert.match(workbenchModalSource, /原 Key 已删除，请重新选择/, '回填 Key 已删除/不在本人列表时必须占位「原 Key 已删除，请重新选择」并要求重选（不阻断其他项回填，§4.2）')
assert.match(workbenchModalSource, /a-upload-dragger/, '参考图必须用拖拽上传区（a-upload-dragger）')
assert.match(workbenchModalSource, /workbench-param-option/, '参数四项必须用块状选择器（workbench-param-option）')
assert.match(workbenchModalSource, /最终以上游返回/, '比例/分辨率近似映射必须注明「最终以上游返回结果为准」（§4.2）')

// —— 2.1) 向量生成页结构断言（§4.3）：embedding 目录联动、本地余弦矩阵、复制向量、前端计时 ——
const embeddingsViewSource = viewSources['/playground/embeddings']
assert.match(embeddingsViewSource, /usePlaygroundCatalog\('embedding'\)/, '向量页必须经 usePlaygroundCatalog(\'embedding\') 联动 embedding 模型')
assert.match(embeddingsViewSource, /@change="handleApiKeyChange"/, '向量页 Key→模型联动必须经 handleApiKeyChange 重置模型选择')
assert.match(embeddingsViewSource, /api\.playground\.embeddings/, '向量页必须走 playground embeddings API')
assert.match(embeddingsViewSource, /function cosineSimilarity/, '相似度矩阵必须由前端本地计算（cosineSimilarity）')
assert.match(embeddingsViewSource, /dot \/ \(Math\.sqrt\(normA\) \* Math\.sqrt\(normB\)\)/, '余弦相似度必须按 dot/(|a||b|) 归一')
assert.match(embeddingsViewSource, /if \(!normA \|\| !normB\) return 0/, '零向量防护必须按 0 处理')
assert.match(embeddingsViewSource, /rowItem\.index === colItem\.index \? 1 : cosineSimilarity/, '矩阵对角线必须恒取 1.000 满色')
assert.match(embeddingsViewSource, /Math\.max\(0, Math\.min\(1, score\)\)/, '热力色阶必须把负分值夹取 0')
assert.match(embeddingsViewSource, /lightness <= 60 \? '#fff'/, '深色单元格必须换白字保证三位小数可读')
assert.match(embeddingsViewSource, /toFixed\(3\)/, '矩阵单元格必须显示三位小数')
assert.match(embeddingsViewSource, /navigator\.clipboard\.writeText\(JSON\.stringify\(item\.vector\)\)/, '结果卡必须提供「复制向量 JSON」按钮')
assert.match(embeddingsViewSource, /performance\.now\(\)/, '结果行必须以 performance.now 计时前端耗时')
assert.doesNotMatch(embeddingsViewSource, /promptTokens|totalTokens/, '向量页结果行不得渲染 tokens（v1.3 裁决全演练场口径：usage 数据流保留，仅 UI 不渲染）')
assert.doesNotMatch(embeddingsViewSource, /artifacts/, '向量页无暂存产物，不得引用 artifacts 恢复逻辑（零持久化）')

// —— 2.2) 重排测试页结构断言（§4.4）：rerank 目录联动、分数条归一、名次徽标、前端计时 ——
const rerankViewSource = viewSources['/playground/rerank']
assert.match(rerankViewSource, /usePlaygroundCatalog\('rerank'\)/, '重排页必须经 usePlaygroundCatalog(\'rerank\') 联动 rerank 模型')
assert.match(rerankViewSource, /@change="handleApiKeyChange"/, '重排页 Key→模型联动必须经 handleApiKeyChange 重置模型选择')
assert.match(rerankViewSource, /api\.playground\.rerank/, '重排页必须走 playground rerank API')
assert.match(rerankViewSource, /maxRelevance/, '分数条必须按本批最高分归一（maxRelevance）')
assert.match(rerankViewSource, /if \(maxRelevance\.value <= 0\) return 0/, '分数条归一必须在 maxRelevance≤0 时返回 0（防除零）')
assert.match(rerankViewSource, /score \/ maxRelevance\.value\) \* 100/, '条形宽度必须按分值占本批最高分比例计算')
assert.match(rerankViewSource, /transition: width 0\.4s ease/, '分数条必须带宽度过渡动画')
assert.match(rerankViewSource, /rankBadgeClass\(rank\)/, '排序结果必须渲染名次徽标')
assert.match(rerankViewSource, /toFixed\(3\)/, '重排分数必须显示三位小数')
assert.match(rerankViewSource, /item\.document\?\.text/, 'returnDocuments=false 时必须只渲染名次 + 行号 + 分数')
assert.match(rerankViewSource, /performance\.now\(\)/, '结果行必须以 performance.now 计时前端耗时')
assert.doesNotMatch(rerankViewSource, /promptTokens|totalTokens/, '重排页结果行不得渲染 tokens（v1.3 裁决全演练场口径：usage 数据流保留，仅 UI 不渲染）')
assert.doesNotMatch(rerankViewSource, /artifacts/, '重排页无暂存产物，不得引用 artifacts 恢复逻辑（零持久化）')

// —— 3) SSE 事件解析行为回放（delta/usage/error + 跨 chunk 分帧；2026-11-22 Key 化后
// round/tool_start/tool_end/done 退役，事件只剩三值）——
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
    'data: {"type":"delta","text":"正在查"}\n\n',
    'data: {"typ',
    'e":"delta","text":"询结果"}\n\n',
    'data: {"type":"usage","promptTokens":10,"completionTokens":5,"totalTokens":15}\n\n'
  ]),
  '/playground/chat/stream',
  (event) => events.push(event)
)
assert.deepEqual(events.map((event) => event.type), ['delta', 'delta', 'usage'])
assert.deepEqual(events[0], { type: 'delta', text: '正在查' })
assert.deepEqual(events[2], { type: 'usage', promptTokens: 10, completionTokens: 5, totalTokens: 15 })

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

// —— 4) Key→模型联动纯函数（六 kind 过滤 + 默认选中第一个；Key 化后模型行为扁平行）——
const fixtureKeyModels: PlaygroundModelOption[] = [
  { modelId: 'gpt-5.4', kind: 'chat', providerCode: 'gpt' },
  { modelId: 'gpt-image-2', kind: 'image', providerCode: 'gpt' },
  { modelId: 'grok-imagine-video-1.5', kind: 'video', providerCode: 'xai' },
  { modelId: 'speech-2.8-hd', kind: 'audio', providerCode: 'minimax' },
  { modelId: 'text-embedding-v4', kind: 'embedding', providerCode: 'qwen' },
  { modelId: 'gte-rerank-v2', kind: 'rerank', providerCode: 'qwen' }
]
assert.deepEqual(resolveModelsForKind(fixtureKeyModels, 'chat').map((model) => model.modelId), ['gpt-5.4'])
assert.deepEqual(resolveModelsForKind(fixtureKeyModels, 'video').map((model) => model.modelId), ['grok-imagine-video-1.5'])
assert.deepEqual(resolveModelsForKind([], 'chat'), [])
assert.equal(firstModelIdOf(resolveModelsForKind(fixtureKeyModels, 'image')), 'gpt-image-2')
assert.equal(firstModelIdOf([]), undefined)

// v1.1 六 kind 目录联动（设计 §4.5）：embedding/rerank 两域与其他四页互不串扰；
// 2026-11-22 Key 化：只展示本页能力域的模型，不展示全部。
assert.deepEqual(resolveModelsForKind(fixtureKeyModels, 'embedding').map((model) => model.modelId), ['text-embedding-v4'])
assert.deepEqual(resolveModelsForKind(fixtureKeyModels, 'rerank').map((model) => model.modelId), ['gte-rerank-v2'])

// —— 5) embeddings / rerank 请求形状与 unwrap 行为（capture adapter 回放，§3.9/§3.10）——
const originalAdapter = http.defaults.adapter
let capturedConfig: InternalAxiosRequestConfig | undefined
let replayedPayload: unknown
http.defaults.adapter = async (config) => {
  capturedConfig = config
  return { data: { data: replayedPayload }, status: 200, statusText: 'OK', headers: {}, config }
}
try {
  const embeddingsPayload = {
    apiKeyId: 'key_1',
    modelId: 'text-embedding-v4',
    input: ['文本一', '文本二'],
    dimensions: 1024
  }
  replayedPayload = {
    model: 'text-embedding-v4',
    usage: { promptTokens: 12, totalTokens: 12 },
    items: [
      { index: 0, dimensions: 1024, vector: [1, 0] },
      { index: 1, dimensions: 1024, vector: [0.8, 0.6] }
    ]
  }
  const embeddingsResult = await playgroundApi.embeddings(embeddingsPayload)
  assert.equal(capturedConfig?.method, 'post', 'embeddings 必须 POST')
  assert.equal(capturedConfig?.url, '/playground/embeddings', 'embeddings 必须命中 /playground/embeddings')
  assert.equal(capturedConfig?.timeout, 0, 'embeddings 同步等待必须禁用 axios 默认超时（timeout: 0）')
  const embeddingsSentBody = JSON.parse(String(capturedConfig?.data)) as Record<string, unknown>
  assert.deepEqual(embeddingsSentBody, embeddingsPayload, 'embeddings 请求体必须透传 apiKeyId/modelId/input/dimensions（2026-11-22 Key 化）')
  assert.ok(!('encodingFormat' in embeddingsSentBody), 'encodingFormat 为后端契约保留的透传字段（API 直调 /v1 面使用），演练场前端请求不得携带该键')
  assert.equal(embeddingsResult.model, 'text-embedding-v4', 'unwrap 后必须直接拿到响应 payload（{data} 信封）')
  assert.equal(embeddingsResult.items.length, 2)
  assert.deepEqual(embeddingsResult.usage, { promptTokens: 12, totalTokens: 12 })

  const rerankPayload = {
    apiKeyId: 'key_1',
    modelId: 'gte-rerank-v2',
    query: '查询语句',
    documents: ['文档一', '文档二', '文档三'],
    topN: 3,
    returnDocuments: false
  }
  replayedPayload = {
    results: [
      { index: 2, relevanceScore: 0.98 },
      { index: 0, relevanceScore: 0.42 },
      { index: 1, relevanceScore: 0.07 }
    ],
    usage: { promptTokens: 100, totalTokens: 100 }
  }
  const rerankResult = await playgroundApi.rerank(rerankPayload)
  assert.equal(capturedConfig?.method, 'post', 'rerank 必须 POST')
  assert.equal(capturedConfig?.url, '/playground/rerank', 'rerank 必须命中 /playground/rerank')
  assert.equal(capturedConfig?.timeout, 0, 'rerank 同步等待必须禁用 axios 默认超时（timeout: 0）')
  assert.deepEqual(JSON.parse(String(capturedConfig?.data)), rerankPayload, 'rerank 请求体必须透传 apiKeyId/modelId/query/documents/topN/returnDocuments（2026-11-22 Key 化）')
  assert.deepEqual(rerankResult.results.map((item) => item.index), [2, 0, 1], 'results 必须按后端降序原样透传（index 为请求 documents 下标）')
  assert.equal(rerankResult.results[0]?.document, undefined, 'returnDocuments=false 时结果无 document 字段')
  assert.deepEqual(rerankResult.usage, { promptTokens: 100, totalTokens: 100 })
} finally {
  http.defaults.adapter = originalAdapter
}

console.log('演练场路由注册（六页）、SSE 事件解析、Key→模型联动（六 kind）、embeddings/rerank 请求形状与零持久化回归通过')
