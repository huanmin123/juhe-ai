<template>
  <a-modal :open="open" title="定时检查" width="760px" :footer="null" @update:open="emit('update:open', $event)">
    <a-alert class="schedule-alert" type="info" show-icon message="每个账户配置一条计划；最短每 10 分钟检查一次，检测模式使用统一质量配置。" />
    <a-form class="schedule-form" layout="vertical" @finish="save">
      <a-form-item label="检查账户" required>
        <a-select
          v-model:value="form.accountId"
          show-search
          allow-clear
          :filter-option="false"
          :loading="accountOptionsLoading"
          :options="accountOptions"
          placeholder="输入账户名称搜索"
          @search="emit('account-search', $event)"
        />
      </a-form-item>
      <a-form-item label="检查模型" required>
        <a-select v-model:value="form.model" :options="modelOptions" placeholder="请选择模型" />
      </a-form-item>
      <a-form-item label="检查间隔" required>
        <a-input-number v-model:value="form.intervalMinutes" :min="10" :max="10080" :precision="0" addon-after="分钟" />
      </a-form-item>
      <a-form-item label="启用">
        <a-switch v-model:checked="form.enabled" />
      </a-form-item>
      <a-button type="primary" html-type="submit" :loading="saving">{{ form.expectedRevision ? '保存修改' : '新增计划' }}</a-button>
      <a-button v-if="form.expectedRevision" @click="resetForm">取消编辑</a-button>
    </a-form>

    <a-divider>已配置账户</a-divider>
    <a-spin :spinning="loading">
      <a-empty v-if="!schedules.length" description="暂无定时检查计划" />
      <a-list v-else :data-source="schedules" item-layout="horizontal">
        <template #renderItem="{ item }">
          <a-list-item>
            <template #actions>
              <a-button type="link" size="small" @click="edit(item)">编辑</a-button>
              <a-popconfirm title="确认删除这条定时检查计划？" @confirm="emit('delete', item.id)">
                <a-button type="link" danger size="small">删除</a-button>
              </a-popconfirm>
            </template>
            <a-list-item-meta :title="item.accountName || item.accountId">
              <template #description>
                <a-space wrap>
                  <a-tag :color="item.enabled ? 'green' : 'default'">{{ item.enabled ? '已启用' : '已暂停' }}</a-tag>
                  <span>{{ item.model }}</span>
                  <span>每 {{ item.intervalMinutes }} 分钟</span>
                  <span>下次 {{ formatDateTime(item.nextRunAt) }}</span>
                  <span v-if="item.lastRunStatus">上次：{{ statusText(item.lastRunStatus) }}</span>
                  <a-tag v-if="item.currentEnforcementAction === 'quality_isolate'" color="red">质量隔离</a-tag>
                </a-space>
              </template>
            </a-list-item-meta>
          </a-list-item>
        </template>
      </a-list>
      <a-pagination
        v-if="total > pageSize"
        class="schedule-pagination"
        :current="page"
        :page-size="pageSize"
        :total="total"
        show-less-items
        @change="emit('page-change', $event)"
      />
    </a-spin>
  </a-modal>
</template>

<script setup lang="ts">
import { reactive, watch } from 'vue'
import { formatDateTime } from '@/shared/formatters'
import type { ModelQualitySchedule, ModelQualityScheduleMutationInput } from '@/types/domain'
import { statusText } from './modelCheckFormatters'

const props = defineProps<{
  accountOptions: Array<{ label: string; value: string }>
  accountOptionsLoading: boolean
  loading: boolean
  modelOptions: Array<{ label: string; value: string }>
  open: boolean
  page: number
  pageSize: number
  saving: boolean
  schedules: ModelQualitySchedule[]
  total: number
}>()
const emit = defineEmits<{
  (event: 'account-search', value: string): void
  (event: 'delete', id: string): void
  (event: 'page-change', page: number): void
  (event: 'save', value: ModelQualityScheduleMutationInput): void
  (event: 'update:open', value: boolean): void
}>()
const form = reactive<ModelQualityScheduleMutationInput>({ accountId: '', model: '', intervalMinutes: 60, enabled: true })

watch(() => props.open, (open) => { if (!open) resetForm() })

function save() {
  if (!form.accountId || !form.model || !Number.isInteger(form.intervalMinutes) || form.intervalMinutes < 10) return
  emit('save', { ...form })
}

function edit(item: ModelQualitySchedule) {
  form.accountId = item.accountId
  form.model = item.model
  form.intervalMinutes = item.intervalMinutes
  form.enabled = item.enabled
  form.expectedRevision = item.revision
  emit('account-search', item.accountName || item.accountId)
}

function resetForm() {
  form.accountId = ''
  form.model = props.modelOptions[0]?.value || ''
  form.intervalMinutes = 60
  form.enabled = true
  delete form.expectedRevision
}
</script>

<style scoped>
.schedule-alert { margin-bottom: 16px; }
.schedule-form { display: grid; grid-template-columns: minmax(180px, 1.5fr) minmax(150px, 1fr) 150px auto auto auto; align-items: end; gap: 10px; }
.schedule-form :deep(.ant-form-item) { margin-bottom: 0; }
.schedule-form :deep(.ant-select), .schedule-form :deep(.ant-input-number-group-wrapper) { width: 100%; }
.schedule-pagination { margin-top: 14px; text-align: right; }
@media (max-width: 900px) { .schedule-form { grid-template-columns: 1fr; align-items: stretch; } .schedule-form :deep(.ant-btn) { width: 100%; } }
</style>
