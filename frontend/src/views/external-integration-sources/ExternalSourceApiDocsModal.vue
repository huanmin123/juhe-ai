<template>
  <a-modal
    v-model:open="open"
    title="公开接口接入文档"
    width="calc(100vw - 60px)"
    wrap-class-name="api-doc-modal-wrap"
    :footer="null"
  >
    <a-spin :spinning="loading">
      <div v-if="apiCatalog" class="api-doc-layout">
        <aside class="api-doc-sidebar">
          <a-input-search
            v-model:value="keyword"
            allow-clear
            placeholder="搜索接口名称"
          />
          <div class="api-doc-list">
            <button
              v-for="item in filteredApiDocs"
              :key="item.id"
              class="api-doc-list-item"
              :class="{ active: selectedApiDoc?.id === item.id }"
              type="button"
              @click="selectApiDoc(item.id)"
            >
              <span class="api-doc-list-title">{{ item.name }}</span>
              <a-tooltip :title="`${item.method} ${item.path}`" placement="right">
                <span class="api-doc-list-path">{{ item.method }} {{ item.path }}</span>
              </a-tooltip>
            </button>
            <a-empty v-if="!filteredApiDocs.length" image="simple" description="没有匹配的接口。" />
          </div>
        </aside>
        <section v-if="selectedApiDoc" class="api-doc-detail">
          <div class="api-doc-detail-head">
            <div>
              <div class="api-doc-title-line">
                <a-tag color="blue">{{ selectedApiDoc.method }}</a-tag>
                <h3>{{ selectedApiDoc.name }}</h3>
                <a-tag :color="apiStatusColor(selectedApiDoc.status)">{{ apiStatusText(selectedApiDoc.status) }}</a-tag>
              </div>
              <p>{{ selectedApiDoc.summary }}</p>
            </div>
            <div class="api-doc-actions">
              <a-button @click="exportApiMarkdown(selectedApiDoc)">
                <template #icon><download-outlined /></template>
                导出 Markdown
              </a-button>
              <a-button type="primary" @click="copyCurl(selectedApiDoc)">
                <template #icon><copy-outlined /></template>
                复制 curl
              </a-button>
            </div>
          </div>

          <a-descriptions bordered size="small" :column="1">
            <a-descriptions-item label="调用地址">
              <code>{{ apiDocUrl(selectedApiDoc) }}</code>
            </a-descriptions-item>
            <a-descriptions-item label="认证方式">
              <code>Authorization: Bearer &lt;source_token&gt;</code>
            </a-descriptions-item>
            <a-descriptions-item label="接口资源授权">
              <code>{{ selectedApiDoc.scope || '-' }}</code>
            </a-descriptions-item>
          </a-descriptions>

          <div class="api-doc-section">
            <h4>请求头</h4>
            <div class="api-doc-field-table">
              <div class="api-doc-field-row head">
                <span>名称</span>
                <span>类型</span>
                <span>必填</span>
                <span>说明</span>
                <span>示例</span>
              </div>
              <div v-for="header in selectedApiDoc.headers" :key="header.name" class="api-doc-field-row">
                <code>{{ header.name }}</code>
                <span>HTTP Header</span>
                <span>{{ header.required ? '是' : '否' }}</span>
                <span>{{ header.description }}</span>
                <code>{{ header.example }}</code>
              </div>
            </div>
          </div>

          <div class="api-doc-section">
            <h4>请求参数</h4>
            <div v-if="selectedApiDoc.query.length" class="api-doc-field-table">
              <div class="api-doc-field-row head">
                <span>名称</span>
                <span>类型</span>
                <span>必填</span>
                <span>说明</span>
                <span>示例</span>
              </div>
              <div v-for="field in selectedApiDoc.query" :key="field.name" class="api-doc-field-row">
                <code>{{ field.name }}</code>
                <span>{{ field.type }}</span>
                <span>{{ field.required ? '是' : '否' }}</span>
                <span>{{ field.description }}</span>
                <code>{{ formatFieldExample(field.example) }}</code>
              </div>
            </div>
            <span v-else class="muted-cell">无</span>
          </div>

          <div class="api-doc-section">
            <h4>请求体</h4>
            <template v-if="selectedApiDoc.requestBody">
              <div class="api-doc-content-type">Content-Type：<code>{{ selectedApiDoc.requestBody.contentType }}</code></div>
              <div v-if="selectedApiDoc.requestBody.fields.length" class="api-doc-field-table">
                <div class="api-doc-field-row head">
                  <span>名称</span>
                  <span>类型</span>
                  <span>必填</span>
                  <span>说明</span>
                  <span>示例</span>
                </div>
                <div v-for="field in selectedApiDoc.requestBody.fields" :key="field.name" class="api-doc-field-row">
                  <code>{{ field.name }}</code>
                  <span>{{ field.type }}</span>
                  <span>{{ field.required ? '是' : '否' }}</span>
                  <span>{{ field.description }}</span>
                  <code>{{ formatFieldExample(field.example) }}</code>
                </div>
              </div>
              <h5>请求体示例</h5>
              <pre class="api-doc-code">{{ formatJson(selectedApiDoc.requestBody.example) }}</pre>
            </template>
            <span v-else class="muted-cell">无</span>
          </div>

          <div class="api-doc-section">
            <h4>响应字段</h4>
            <div v-if="selectedApiDoc.responseFields.length" class="api-doc-field-table">
              <div class="api-doc-field-row head">
                <span>名称</span>
                <span>类型</span>
                <span>必填</span>
                <span>说明</span>
                <span>示例</span>
              </div>
              <div v-for="field in selectedApiDoc.responseFields" :key="field.name" class="api-doc-field-row">
                <code>{{ field.name }}</code>
                <span>{{ field.type }}</span>
                <span>{{ field.required ? '是' : '否' }}</span>
                <span>{{ field.description }}</span>
                <code>{{ formatFieldExample(field.example) }}</code>
              </div>
            </div>
            <span v-else class="muted-cell">无</span>
          </div>

          <div class="api-doc-section">
            <h4>响应示例</h4>
            <pre class="api-doc-code">{{ formatResponseExample(selectedApiDoc) }}</pre>
          </div>
        </section>
      </div>
    </a-spin>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { CopyOutlined, DownloadOutlined } from '@ant-design/icons-vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import type { ExternalPublicApiCatalog, ExternalPublicApiDocItem } from '@/types/domain'
import {
  apiMarkdownFilename,
  apiStatusColor,
  apiStatusText,
  buildApiDocUrl,
  buildApiMarkdown,
  buildCurl,
  detectCurlCommandPlatform,
  downloadTextFile,
  formatFieldExample,
  formatJson,
  formatResponseExample,
  resolvePublicApiBaseUrl,
  type CurlCommandPlatform
} from './externalSourceApiDocs'

const open = defineModel<boolean>('open', { required: true })

const loading = ref(false)
const keyword = ref('')
const apiCatalog = ref<ExternalPublicApiCatalog>()
const selectedApiDocId = ref<string>()
const publicApiBaseUrl = computed(() => resolvePublicApiBaseUrl())
const curlCommandPlatform = computed<CurlCommandPlatform>(() => detectCurlCommandPlatform())

const filteredApiDocs = computed(() => {
  const keywordValue = keyword.value.trim().toLowerCase()
  const items = apiCatalog.value?.items ?? []
  if (!keywordValue) return items
  return items.filter((item) => [
    item.name,
    item.path,
    item.summary
  ].some((value) => value.toLowerCase().includes(keywordValue)))
})

const selectedApiDoc = computed(() => {
  const items = filteredApiDocs.value
  if (!items.length) return undefined
  return items.find((item) => item.id === selectedApiDocId.value) ?? items[0]
})

watch(open, (value) => {
  if (value) {
    void loadApiDocs()
  }
})

async function loadApiDocs(): Promise<void> {
  if (apiCatalog.value) {
    selectedApiDocId.value = selectedApiDoc.value?.id ?? apiCatalog.value.items[0]?.id
    return
  }
  loading.value = true
  try {
    apiCatalog.value = await api.externalIntegrationSources.apiDocs()
    selectedApiDocId.value = apiCatalog.value.items[0]?.id
  } catch (error) {
    message.error(extractApiErrorMessage(error, '加载公开接口文档失败'))
  } finally {
    loading.value = false
  }
}

function selectApiDoc(id: string): void {
  selectedApiDocId.value = id
}

function apiDocUrl(item: ExternalPublicApiDocItem): string {
  return buildApiDocUrl(item, publicApiBaseUrl.value)
}

function copyCurl(item: ExternalPublicApiDocItem | undefined): void {
  void copyTextToClipboard(buildCurl(item, publicApiBaseUrl.value, curlCommandPlatform.value), 'curl 已复制')
}

function exportApiMarkdown(item: ExternalPublicApiDocItem | undefined): void {
  if (!item) return
  downloadTextFile(
    apiMarkdownFilename(item),
    buildApiMarkdown(item, publicApiBaseUrl.value, curlCommandPlatform.value)
  )
  message.success('Markdown 文档已导出')
}
</script>

<style scoped>
:global(.api-doc-modal-wrap .ant-modal) {
  top: 30px;
  width: calc(100vw - 60px) !important;
  max-width: calc(100vw - 60px) !important;
  padding-bottom: 30px;
}

:global(.api-doc-modal-wrap .ant-modal-body) {
  max-height: none;
  overflow: hidden;
  padding: 16px 24px 24px;
}

.api-doc-layout {
  display: grid;
  grid-template-columns: 300px minmax(0, 1fr);
  gap: 16px;
  height: calc(100vh - 156px);
  min-height: 0;
}

.api-doc-sidebar {
  display: flex;
  min-height: 0;
  min-width: 0;
  flex-direction: column;
  gap: 12px;
  border-right: 1px solid #edf1f7;
  padding-right: 16px;
}

.api-doc-code,
.api-doc-field-row code,
.api-doc-list-path {
  font-family: ui-monospace, SFMono-Regular, Consolas, 'Liberation Mono', Menlo, monospace;
}

.api-doc-list {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
  gap: 8px;
  overflow-y: auto;
}

.api-doc-list-item {
  display: flex;
  width: 100%;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
  border: 1px solid #edf1f7;
  border-radius: 8px;
  padding: 10px;
  background: #fff;
  cursor: pointer;
  text-align: left;
}

.api-doc-list-item.active,
.api-doc-list-item:hover {
  border-color: #1677ff;
}

.api-doc-list-title {
  color: #0f172a;
  font-weight: 600;
}

.api-doc-list-path {
  overflow: hidden;
  color: #64748b;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.api-doc-detail {
  min-height: 0;
  min-width: 0;
  overflow-y: auto;
  padding-right: 4px;
}

.api-doc-detail-head,
.api-doc-title-line {
  display: flex;
  align-items: center;
  gap: 8px;
}

.api-doc-detail-head {
  justify-content: space-between;
  margin-bottom: 14px;
}

.api-doc-actions {
  display: flex;
  flex-shrink: 0;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 8px;
}

.api-doc-detail-head p {
  margin: 6px 0 0;
  color: #64748b;
}

.api-doc-title-line h3 {
  margin: 0;
  font-size: 18px;
  line-height: 1.35;
}

.api-doc-section {
  margin-top: 16px;
}

.api-doc-section h4,
.api-doc-section h5 {
  margin: 0 0 8px;
}

.api-doc-section h5 {
  margin-top: 12px;
  color: #334155;
  font-size: 13px;
}

.api-doc-content-type {
  margin-bottom: 8px;
  color: #475569;
}

.api-doc-field-table {
  display: grid;
  gap: 1px;
  overflow: hidden;
  border: 1px solid #edf1f7;
  border-radius: 8px;
}

.api-doc-field-row {
  display: grid;
  grid-template-columns: minmax(150px, 1fr) minmax(110px, 0.7fr) 64px minmax(220px, 1.5fr) minmax(140px, 1fr);
  gap: 10px;
  padding: 9px 10px;
  background: #fff;
}

.api-doc-field-row > * {
  min-width: 0;
  overflow-wrap: anywhere;
}

.api-doc-field-row.head {
  background: #f8fafc;
  color: #64748b;
  font-weight: 600;
}

.api-doc-code {
  overflow: auto;
  max-height: 260px;
  margin: 0;
  border: 1px solid #edf1f7;
  border-radius: 8px;
  padding: 12px;
  background: #0f172a;
  color: #e5edf7;
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
}

@media (max-width: 720px) {
  .api-doc-layout {
    grid-template-columns: 1fr;
    height: auto;
    max-height: calc(100vh - 156px);
    overflow-y: auto;
  }

  .api-doc-sidebar {
    max-height: 380px;
    border-right: 0;
    border-bottom: 1px solid #edf1f7;
    padding-right: 0;
    padding-bottom: 12px;
  }

  .api-doc-detail {
    overflow: visible;
  }

  .api-doc-detail-head {
    align-items: stretch;
    flex-direction: column;
  }

  .api-doc-actions {
    justify-content: flex-start;
  }

  .api-doc-field-row {
    grid-template-columns: 1fr;
  }
}
</style>
