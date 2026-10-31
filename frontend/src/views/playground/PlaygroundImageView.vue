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
          <a-form-item label="张数">
            <a-input-number v-model:value="count" :min="1" :max="4" style="width: 100%" />
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
          <div class="playground-result-meta">模型 {{ resultModel }} · {{ results.length }} 张</div>
          <div class="playground-image-grid">
            <div v-for="(image, index) in results" :key="index" class="playground-image-cell">
              <img class="playground-image" :src="image.dataUrl" :alt="`生成结果 ${index + 1}`" />
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
import type { PlaygroundImageData } from '@/types/domain/playground'
import { usePlaygroundCatalog } from './usePlaygroundCatalog'

// 图片生成（演练场设计 §4.2）：账户 × image 模型直连演示，产物内联 base64 直显，
// 零存储——刷新即空，不做任何本地持久化。

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
const count = ref(1)
const referenceName = ref('')
const referenceBase64 = ref('')
const referencePreview = ref('')
const generating = ref(false)
const results = ref<PlaygroundImageData[]>([])
const resultModel = ref('')

const canGenerate = computed(() => Boolean(prompt.value.trim() && accountId.value && modelId.value && !generating.value))

onMounted(() => {
  void loadCatalog()
})

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
      n: count.value,
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

function downloadImage(image: PlaygroundImageData, index: number): void {
  const link = document.createElement('a')
  link.href = image.dataUrl
  link.download = `playground-image-${index + 1}.${dataUrlExtension(image.dataUrl)}`
  link.click()
}

function dataUrlExtension(dataUrl: string): string {
  const mime = /^data:image\/([a-z0-9.+-]+);/i.exec(dataUrl)?.[1]?.toLowerCase() ?? 'png'
  return mime === 'jpeg' ? 'jpg' : mime
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
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
}

.playground-image-cell {
  min-width: 0;
  padding: 8px;
  border: 1px solid var(--juhe-border);
  border-radius: 8px;
  background: var(--juhe-surface-soft);
}

.playground-image {
  display: block;
  width: 100%;
  max-height: 320px;
  margin-bottom: 8px;
  object-fit: contain;
  border-radius: 6px;
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
