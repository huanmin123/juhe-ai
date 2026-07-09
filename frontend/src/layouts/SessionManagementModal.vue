<template>
  <a-modal
    :open="open"
    title="会话管理"
    width="760px"
    :footer="null"
    @cancel="$emit('update:open', false)"
  >
    <div class="session-modal-toolbar">
      <span class="session-modal-hint">当前账号未过期会话</span>
      <a-button :loading="loading" @click="$emit('refresh')">
        <template #icon><ReloadOutlined /></template>
        刷新
      </a-button>
    </div>

    <a-table
      row-key="id"
      size="middle"
      :columns="columns"
      :data-source="sessions"
      :loading="loading"
      :pagination="false"
      :scroll="{ x: 680 }"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'current'">
          <a-tag :color="record.current ? 'green' : 'default'">{{ record.current ? '当前会话' : '其他会话' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'createdAt'">
          <span class="muted-cell">{{ formatDateTime(record.createdAt) }}</span>
        </template>
        <template v-else-if="column.key === 'lastSeenAt'">
          <span class="muted-cell">{{ formatDateTime(record.lastSeenAt) }}</span>
        </template>
        <template v-else-if="column.key === 'expiresAt'">
          <span class="muted-cell">{{ formatDateTime(record.expiresAt) }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <a-popconfirm
            :title="record.current ? '撤销当前会话后需要重新登录，确认撤销？' : '确认撤销该会话？'"
            ok-text="撤销"
            cancel-text="取消"
            @confirm="$emit('revoke', record)"
          >
            <a-button danger type="text" :loading="revokingSessionId === record.id">
              <template #icon><DeleteOutlined /></template>
              撤销
            </a-button>
          </a-popconfirm>
        </template>
      </template>
    </a-table>

    <div v-if="showPagination" class="session-modal-pagination">
      <a-pagination
        size="small"
        :current="page"
        :page-size="pageSize"
        :total="total"
        :show-size-changer="false"
        @change="$emit('page-change', $event)"
      />
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { DeleteOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import type { TableColumnsType } from 'ant-design-vue'
import { computed } from 'vue'

import { formatDateTime } from '@/shared/formatters'
import type { AuthSessionSummary } from '@/types/domain'

const props = defineProps<{
  open: boolean
  sessions: AuthSessionSummary[]
  loading: boolean
  revokingSessionId?: string
  page: number
  pageSize: number
  total: number
  hasMore: boolean
}>()

defineEmits<{
  (event: 'update:open', value: boolean): void
  (event: 'refresh'): void
  (event: 'page-change', page: number): void
  (event: 'revoke', session: AuthSessionSummary): void
}>()

const columns = computed<TableColumnsType<AuthSessionSummary>>(() => [
  { title: '状态', key: 'current', width: 120 },
  { title: '创建时间', key: 'createdAt', width: 180 },
  { title: '最近活跃', key: 'lastSeenAt', width: 180 },
  { title: '过期时间', key: 'expiresAt', width: 180 },
  { title: '操作', key: 'actions', width: 110, fixed: 'right' }
])

const showPagination = computed(() => props.total > props.pageSize || props.hasMore)
</script>

<style scoped>
.session-modal-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 14px;
}

.session-modal-hint {
  color: #64748b;
  font-size: 13px;
}

.session-modal-pagination {
  display: flex;
  justify-content: flex-end;
  margin-top: 16px;
}
</style>
