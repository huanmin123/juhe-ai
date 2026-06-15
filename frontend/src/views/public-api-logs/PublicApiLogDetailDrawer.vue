<template>
  <a-drawer
    :open="open"
    width="min(960px, 96vw)"
    title="公开接口日志详情"
    :body-style="{ padding: '18px' }"
    @update:open="emit('update:open', $event)"
    @close="emit('close')"
  >
    <a-spin :spinning="loading">
      <template v-if="detail">
        <a-descriptions bordered size="small" :column="2" class="detail-descriptions">
          <a-descriptions-item label="调用时间">{{ formatDateTime(detail.createdAt) }}</a-descriptions-item>
          <a-descriptions-item label="结果">
            <a-tag :color="detail.success ? 'green' : 'red'">{{ detail.success ? '成功' : '失败' }}</a-tag>
          </a-descriptions-item>
          <a-descriptions-item label="来源系统">{{ detail.sourceName || '-' }}</a-descriptions-item>
          <a-descriptions-item label="测试 token">{{ detail.isTestToken ? '是' : '否' }}</a-descriptions-item>
          <a-descriptions-item label="token">{{ detail.tokenName || '-' }} / {{ detail.tokenPrefix || '-' }}</a-descriptions-item>
          <a-descriptions-item label="接口">{{ detail.method }} {{ detail.path }}</a-descriptions-item>
          <a-descriptions-item label="状态码">{{ detail.statusCode ?? '-' }}</a-descriptions-item>
          <a-descriptions-item label="耗时">{{ formatPublicApiLogDuration(detail.durationMs) }}</a-descriptions-item>
          <a-descriptions-item label="客户端 IP">{{ detail.clientIp || '-' }}</a-descriptions-item>
          <a-descriptions-item label="traceId">{{ detail.traceId || '-' }}</a-descriptions-item>
          <a-descriptions-item label="User-Agent" :span="2">{{ detail.userAgent || '-' }}</a-descriptions-item>
          <a-descriptions-item label="错误" :span="2">{{ detail.errorMessage || detail.errorCode || '-' }}</a-descriptions-item>
        </a-descriptions>

        <a-tabs>
          <a-tab-pane key="request" tab="请求数据">
            <ReadonlyCodeViewer content-type="application/json" :text="prettyPublicApiLogJson(detail.requestData)" title="请求摘要" />
          </a-tab-pane>
          <a-tab-pane key="response" tab="响应数据">
            <ReadonlyCodeViewer content-type="application/json" :text="prettyPublicApiLogJson(detail.responseData)" title="响应摘要" />
          </a-tab-pane>
        </a-tabs>
      </template>
    </a-spin>
  </a-drawer>
</template>

<script setup lang="ts">
import ReadonlyCodeViewer from '@/components/ReadonlyCodeViewer.vue'
import { formatDateTime } from '@/shared/formatters'
import type { PublicApiLogDetail } from '@/types/domain'
import {
  formatPublicApiLogDuration,
  prettyPublicApiLogJson
} from './publicApiLogFormatters'

defineProps<{
  detail?: PublicApiLogDetail
  loading: boolean
  open: boolean
}>()

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'update:open', open: boolean): void
}>()
</script>

<style scoped>
.detail-descriptions {
  margin-bottom: 16px;
}
</style>
