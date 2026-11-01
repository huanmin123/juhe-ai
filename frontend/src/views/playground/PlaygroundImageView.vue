<template>
  <a-card class="page-card responsive-page-card">
    <div class="playground-media-layout">
      <a-form layout="vertical" class="playground-media-form">
        <a-form-item label="账户" required>
          <a-select
            v-model:value="accountId"
            :options="accountOptions"
            :loading="accountsLoading"
            allow-clear
            show-search
            option-filter-prop="label"
            placeholder="选择要试玩的账户"
            @change="handleAccountChange"
          />
        </a-form-item>
        <a-form-item label="模型" required>
          <a-select
            v-model:value="modelId"
            :options="modelOptions"
            :disabled="!accountId || !modelOptions.length"
            show-search
            option-filter-prop="label"
            :placeholder="accountId ? '选择生图模型' : '先选择账户'"
          />
        </a-form-item>
        <a-form-item label="提示词" required>
          <a-textarea
            v-model:value="prompt"
            :rows="4"
            placeholder="描述要生成的图片内容"
          />
        </a-form-item>
        <div class="playground-two-column">
          <a-form-item label="尺寸（可选）">
            <a-select
              v-model:value="size"
              :options="sizeOptions"
              allow-clear
              placeholder="默认（由上游决定）"
            />
          </a-form-item>
        </div>
        <a-form-item label="参考图（可选，走图生图）">
          <div v-if="referencePreview" class="playground-reference-row">
            <img class="playground-reference-thumb" :src="referencePreview" alt="参考图预览" />
            <a-button @click="clearReference">移除参考图</a-button>
          </div>
          <a-upload
            v-else
            accept="image/png,image/jpeg,image/webp"
            :before-upload="handleReferenceUpload"
            :show-upload-list="false"
          >
            <a-button>上传参考图</a-button>
          </a-upload>
        </a-form-item>
        <a-button
          type="primary"
          :loading="generating"
          :disabled="!canGenerate"
          @click="generate"
        >
          生成图片
        </a-button>
        <div v-if="accountsLoadError" class="playground-form-error">{{ accountsLoadError }}</div>
      </a-form>

      <section class="playground-media-result">
        <a-empty
          v-if="!results.length"
          class="playground-empty"
          description="选择账户与生图模型，输入提示词开始生成"
        />
        <template v-else>
          <div class="playground-result-meta">
            <template v-if="resultModel">模型 {{ resultModel }}</template>
          </div>
          <div class="playground-image-grid">
            <div v-for="(image, index) in results" :key="image.id" class="playground-image-cell">
              <!-- a-image 自带点击大图预览（遮罩查看器），单张时网格自适应铺满结果区。 -->
              <a-image
                class="playground-image"
                :src="image.url"
                :alt="`生成结果 ${index + 1}`"
              />
              <a-button block size="small" @click="downloadImage(image, index)">下载</a-button>
            </div>
          </div>
        </template>
      </section>
    </div>
  </a-card>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { PlaygroundImageArtifact } from '@/types/domain/playground'
import { usePlaygroundCatalog } from './usePlaygroundCatalog'

// 图片生成（演练场设计 §4.2）：账户 × image 模型直连演示，产物落服务端暂存
// （§3.4/§3.8，同域 url 直显直下），前端零持久化；刷新后经 artifacts 恢复最近一批。

const maxReferenceBytes = 10 * 1024 * 1024

const {
  accountsLoading,
  accountsLoadError,
  accountId,
  modelId,
  accountOptions,
  modelOptions,
  handleAccountChange,
  loadCatalog
} = usePlaygroundCatalog('image')

const sizeOptions = [
  { value: '1024x1024', label: '1024x1024' },
  { value: '1024x1536', label: '1024x1536' },
  { value: '1536x1024', label: '1536x1024' }
]

const prompt = ref('')
const size = ref<string>()
const referenceName = ref('')
const referenceBase64 = ref('')
const referencePreview = ref('')
const generating = ref(false)
const results = ref<PlaygroundImageArtifact[]>([])
const resultModel = ref('')

const canGenerate = computed(() => Boolean(prompt.value.trim() && accountId.value && modelId.value && !generating.value))

onMounted(() => {
  void restoreOnLoad()
})

/** 加载序：先账户目录（默认选中），再拉暂存清单恢复最近一批图（暂存为展示缓存，恢复失败不阻断生成）。 */
async function restoreOnLoad(): Promise<void> {
  await loadCatalog()
  try {
    const artifacts = await api.playground.artifacts()
    if (artifacts.images.length) {
      results.value = artifacts.images
    }
  } catch (error) {
    console.error(error)
  }
}

/** a-upload beforeUpload：本地读取转 base64，返回 false 阻止自动上传。 */
function handleReferenceUpload(file: File): boolean {
  if (file.size > maxReferenceBytes) {
    message.warning('参考图不能超过 10MB')
    return false
  }
  const reader = new FileReader()
  reader.onload = () => {
    if (typeof reader.result !== 'string') return
    const separator = reader.result.indexOf(',')
    if (separator < 0) {
      message.error('读取参考图失败')
      return
    }
    referencePreview.value = reader.result
    referenceBase64.value = reader.result.slice(separator + 1)
    referenceName.value = file.name
    message.success(`已读取参考图 ${file.name}`)
  }
  reader.onerror = () => {
    message.error('读取参考图失败')
  }
  reader.readAsDataURL(file)
  return false
}

function clearReference(): void {
  referenceName.value = ''
  referenceBase64.value = ''
  referencePreview.value = ''
}

async function generate(): Promise<void> {
  const content = prompt.value.trim()
  if (!content) {
    message.warning('请输入提示词')
    return
  }
  if (!accountId.value || !modelId.value) {
    message.warning('请先选择账户与模型')
    return
  }
  generating.value = true
  try {
    const result = await api.playground.images({
      accountId: accountId.value,
      modelId: modelId.value,
      prompt: content,
      size: size.value || undefined,
      inputImageBase64: referenceBase64.value || undefined
    })
    results.value = result.images
    resultModel.value = result.model
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '图片生成失败，请稍后重试'))
  } finally {
    generating.value = false
  }
}

/** 同域内容地址 + a[download]：浏览器自动带会话 Cookie 拉取；契约未给图片 MIME，文件名固定 png 后缀。 */
function downloadImage(image: PlaygroundImageArtifact, index: number): void {
  const link = document.createElement('a')
  link.href = image.url
  link.download = `playground-image-${index + 1}.png`
  link.click()
}
</script>

<style scoped>
.playground-media-layout {
  display: grid;
  grid-template-columns: 360px minmax(0, 1fr);
  gap: 24px;
}

.playground-media-form {
  min-width: 0;
}

.playground-two-column {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}

.playground-form-error {
  margin-top: 12px;
  color: var(--juhe-danger);
  font-size: 12px;
}

.playground-reference-row {
  display: flex;
  align-items: center;
  gap: 12px;
}

.playground-reference-thumb {
  height: 72px;
  max-width: 160px;
  object-fit: contain;
  border: 1px solid var(--juhe-border);
  border-radius: 6px;
}

.playground-media-result {
  min-width: 0;
}

.playground-empty {
  height: 100%;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  min-height: 240px;
}

.playground-result-meta {
  margin-bottom: 12px;
  color: var(--juhe-muted);
  font-size: 13px;
}

.playground-image-grid {
  display: grid;
  /* 单张铺满结果区、多张自适应分列（张数移除后常态为 1 张）。 */
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  gap: 16px;
}

.playground-image-cell {
  min-width: 0;
  padding: 8px;
  border: 1px solid var(--juhe-border);
  border-radius: 8px;
  background: var(--juhe-surface-soft);
}

/* a-image 外层是包装 div:让图片占满单元格宽度并吃掉可用高度,预览点击落在 img 上。 */
.playground-image :deep(img) {
  display: block;
  width: 100%;
  max-height: min(62vh, 620px);
  object-fit: contain;
  border-radius: 6px;
  cursor: zoom-in;
}

.playground-image :deep(.ant-image-img) {
  margin-bottom: 8px;
}

@media (max-width: 992px) {
  .playground-media-layout {
    grid-template-columns: 1fr;
  }

  .playground-image-grid {
    grid-template-columns: 1fr;
  }
}
</style>
