import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

// 媒体工作室 P0 三工作台回归（规划 F7 T2：源码锚定）：
// 路由三条 + 菜单图标注册、能力载荷缺项不渲染锚点、错误兜底、
// client_operation_id 携带与响应丢失确认、keepAlive 激活暂停恢复、
// 上传取消链路与无游标协议。

function readSource(relativePath: string): string {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8')
}

const routerSource = readSource('../../router/index.ts')
const layoutSource = readSource('../../layouts/AppLayout.vue')
const apiSource = readSource('../../api/domains/studio.ts')
const apiTestSource = readSource('../../api/domains/studio.test.ts')
const typesSource = readSource('../../types/domain/studio.ts')
const pollingSource = readSource('../../views/my-studio/shared/useStudioTaskPolling.ts')
const queueSource = readSource('../../views/my-studio/shared/useStudioTaskQueue.ts')
const uploadSource = readSource('../../views/my-studio/shared/useStudioUpload.ts')
const imagesViewSource = readSource('../../views/my-studio/studioImages/StudioImagesView.vue')
const imageParamsSource = readSource('../../views/my-studio/studioImages/ImageParamsPanel.vue')
const gallerySource = readSource('../../views/my-studio/studioImages/StudioGalleryPanel.vue')
const audioViewSource = readSource('../../views/my-studio/studioAudio/StudioAudioView.vue')
const ttsPanelSource = readSource('../../views/my-studio/studioAudio/TtsPanel.vue')
const transcribePanelSource = readSource('../../views/my-studio/studioAudio/TranscribePanel.vue')
const historySource = readSource('../../views/my-studio/studioAudio/AudioHistoryList.vue')
const videoViewSource = readSource('../../views/my-studio/studioVideo/StudioVideoView.vue')
const videoFormSource = readSource('../../views/my-studio/studioVideo/VideoQuickForm.vue')
const videoListSource = readSource('../../views/my-studio/studioVideo/VideoTaskList.vue')
const capabilitiesSource = readSource('../../views/my-studio/shared/studioCapabilities.ts')

// --- 1. 路由三条 + meta 体系 ---
for (const [path, title] of [
  ['/my-studio/images', '图像生成'],
  ['/my-studio/audio', '音频生成'],
  ['/my-studio/video', '视频生成']
] as const) {
  const entryIndex = routerSource.indexOf(`path: '${path}'`)
  assert.ok(entryIndex >= 0, `menuRoutes 必须登记 ${path}`)
  const entry = routerSource.slice(entryIndex, routerSource.indexOf('},', entryIndex))
  assert.match(entry, /menuGroup: 'studio'/, `${path} 必须归入 studio 分组`)
  assert.match(entry, /menuGroupTitle: '媒体工作室'/, `${path} 必须声明分组标题`)
  assert.match(entry, /viewScope: 'self'/, `${path} 必须是登录用户可用（无角色限制）`)
  assert.match(entry, /keepAlive: true/, `${path} 必须开启 keepAlive`)
  assert.match(entry, /heavy: true/, `${path} 必须声明 heavy`)
  assert.ok(entry.includes(`title: '${title}'`), `${path} 必须使用中文标题 ${title}`)
  assert.match(routerSource, new RegExp(`import\\('@/views/my-studio/`), '路由组件必须指向 my-studio 视图目录')
}

// --- 2. 菜单图标三处接线 ---
assert.match(layoutSource, /SoundOutlined,/, 'AppLayout 必须导入 SoundOutlined')
assert.match(layoutSource, /'\/my-studio\/images': PictureOutlined/, '图像工作台菜单图标必须登记')
assert.match(layoutSource, /'\/my-studio\/audio': SoundOutlined/, '音频工作台菜单图标必须登记')
assert.match(layoutSource, /'\/my-studio\/video': VideoCameraOutlined/, '视频工作台菜单图标必须登记')
assert.match(layoutSource, /studio:\s*AppstoreOutlined/, 'studio 分组图标必须登记')

// --- 3. 能力载荷驱动表单（缺项不渲染，不硬编码参数项） ---
assert.match(capabilitiesSource, /export function studioCapabilityList/, '能力读取必须提供安全列表读取')
assert.match(capabilitiesSource, /export function studioCapabilityFlag/, '能力读取必须提供布尔开关读取')
assert.match(imageParamsSource, /v-if="supportsNegativePrompt"/, '负面提示词必须按能力声明条件渲染')
assert.match(imageParamsSource, /v-if="sizes\.length > 0"/, '尺寸档位必须在能力声明后才渲染')
assert.match(imageParamsSource, /v-if="supportsReferenceImage"/, '参考图上传必须按能力声明条件渲染')
assert.match(videoFormSource, /v-if="supportsFirstFrame"/, '首帧上传必须按能力声明条件渲染')
assert.match(videoFormSource, /v-if="secondsOptions\.length > 0"/, '时长档位必须在能力声明后才渲染')
assert.match(videoFormSource, /v-if="sizeOptions\.length > 0"/, '画幅档位必须在能力声明后才渲染')
assert.match(ttsPanelSource, /v-if="voices\.length > 0"/, '音色必须在能力声明后才渲染')
assert.match(ttsPanelSource, /v-if="speedRange"/, '语速必须在能力声明后才渲染')
assert.match(ttsPanelSource, /v-if="emotionOptions\.length > 0"/, '情感必须在能力声明后才渲染')
assert.match(ttsPanelSource, /v-if="responseFormats\.length > 0"/, '输出格式必须在能力声明后才渲染')
assert.doesNotMatch(imageParamsSource, /sizes:\s*\[', /, '图像表单不得硬编码尺寸词表')
assert.doesNotMatch(videoFormSource, /secondsOptions:\s*\[/, '视频表单不得硬编码时长档位')

// --- 4. 错误兜底与空态中文 ---
const viewSources: Array<[string, string]> = [
  ['图像工作台', imagesViewSource],
  ['音频工作台', audioViewSource],
  ['视频工作台', videoViewSource],
  ['音频历史', historySource]
]
for (const [label, source] of viewSources) {
  assert.match(source, /extractApiErrorMessage\(error, '[^']{4,}'\)/, `${label} 必须以 extractApiErrorMessage + 中文兜底提示错误`)
}
assert.match(gallerySource, /还没有作品，从左侧配置参数生成第一张/, '画廊空态必须是中文引导文案')
assert.match(videoListSource, /还没有视频任务，从上方表单发起第一单镜生成/, '视频任务空态必须是中文引导文案')
assert.match(historySource, /还没有音频，从上方合成第一段/, '音频历史空态必须是中文引导文案')

// --- 5. client_operation_id 幂等与响应丢失确认 ---
assert.match(apiSource, /export function newStudioClientOperationId[\s\S]*crypto\.randomUUID/, 'API 层必须提供 crypto.randomUUID 幂等键')
assert.match(queueSource, /newStudioClientOperationId\(\)/, '异步任务队列提交必须携带新生成的幂等键')
assert.match(queueSource, /operation_id: operationId/, '响应丢失后必须先按 operation_id 查询确认受理结果')
assert.match(queueSource, /isResponseLost/, '必须显式识别响应丢失（无 response）场景')
assert.match(ttsPanelSource, /clientOperationId: crypto\.randomUUID\(\)/, 'TTS 同步请求必须携带幂等键（分段逐段独立）')
assert.match(transcribePanelSource, /clientOperationId: crypto\.randomUUID\(\)/, 'STT 同步请求必须携带幂等键')
for (const key of ['generateImage', 'generateVideo', 'generateSpeech']) {
  assert.match(apiSource, new RegExp(`${key}: \\(payload: ${key === 'generateSpeech' ? 'StudioSpeechGenerationRequest' : key === 'generateImage' ? 'StudioImageGenerationRequest' : 'StudioVideoGenerationRequest'}\\)`), `${key} 必须要求携带 clientOperationId 的请求 DTO`)
}
assert.match(apiTestSource, /not\.toHaveProperty\('n'\)/, '契约测试必须断言无数量参数（决策 #9④）')
assert.match(apiTestSource, /not\.toHaveProperty\('enableWebSearch'\)/, '契约测试必须断言无问答域类比透传字段')

// --- 6. keepAlive 激活暂停恢复 ---
for (const [label, source] of [['图像工作台', imagesViewSource], ['视频工作台', videoViewSource]] as const) {
  assert.match(source, /onActivated\(/, `${label} 必须实现 onActivated 恢复`)
  assert.match(source, /onDeactivated\(/, `${label} 必须实现 onDeactivated 暂停`)
  assert.match(source, /taskQueue\.pause\(\)/, `${label} 停用时必须暂停轮询与计时`)
  assert.match(source, /refreshActiveTasks\(\)\.then\(\(\) => taskQueue\.start\(\)\)/, `${label} 激活必须先补一轮状态同步再恢复轮询`)
}
assert.match(audioViewSource, /onActivated\(/, '音频工作台激活必须补一轮历史同步')
assert.match(pollingSource, /baseIntervalMs = 5_000/, '轮询基础间隔 5s')
assert.match(pollingSource, /maxIntervalMs = 30_000/, '轮询退避封顶 30s')
assert.match(pollingSource, /visibilityState !== 'visible'/, '页面不可见必须跳过轮询')
assert.match(pollingSource, /visibilitychange/, '回到可见必须立即补一轮（visibilitychange 监听）')
assert.match(pollingSource, /deps\.scopeId\(\) === scopeId/, '迟到响应必须按页面作用域校验丢弃')
assert.match(pollingSource, /isStudioTaskFinalSnapshot\(snapshot\)/, '残缺终态快照必须经落定校验（不落定继续轮询）')

// --- 7. 上传取消链路 ---
assert.match(uploadSource, /new AbortController\(\)/, '上传必须提供 AbortController 取消')
assert.match(uploadSource, /axios\.isCancel\(error\)/, '上传取消不得按失败提示（静默回收占位）')
assert.match(apiSource, /timeout: 0/, '上传请求必须 timeout 0（chat uploadAsset 先例）')
assert.match(apiSource, /signal: options\?\.signal/, '上传请求必须透传 AbortSignal')

// --- 8. settled 前不显示成品 / 产物缺失渲染 ---
assert.match(queueSource, /isStudioTaskFinalSnapshot\(task\)/, '任务落定必须经终态校验（onSettled 仅在落定后触发）')
assert.match(videoListSource, /产物缺失/, '产物悬空必须按"产物缺失"文案渲染，不报系统错误')
assert.match(videoListSource, /studioTaskArtifactState\(task\)/, '成品渲染必须以 settled 判定驱动')

// --- 9. 无游标协议（决策 #9⑥） ---
for (const [label, source] of [
  ['studio api', apiSource],
  ['studio types', typesSource],
  ['图像视图', imagesViewSource],
  ['音频视图', audioViewSource],
  ['视频视图', videoViewSource],
  ['音频历史', historySource]
] as const) {
  assert.ok(!source.includes('cursor'), `${label} 不得出现游标分页协议（limit/offset 唯一口径）`)
}

console.log('studio workbench regression passed')
