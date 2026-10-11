<template>
  <div class="usage-stats-account-panel">
    <section class="usage-stats-account-panel-section">
      <div class="usage-stats-account-panel-head">
        <span class="usage-stats-account-panel-title">当前账户</span>
        <span class="usage-stats-account-panel-count">{{ collectionItems.length }}/{{ maxAccounts }}</span>
      </div>
      <div v-if="collectionItems.length" class="usage-stats-account-panel-collection">
        <div
          v-for="item in collectionItems"
          :key="item.account.id"
          class="usage-stats-account-panel-row"
          :class="{ hidden: !item.selected }"
        >
          <button
            type="button"
            class="usage-stats-account-panel-main"
            :aria-pressed="item.selected"
            :disabled="disabled"
            @click="emit('toggle', item.account.id)"
          >
            <span class="usage-stats-account-panel-dot" :style="{ backgroundColor: item.color }" />
            <span class="usage-stats-account-panel-name">{{ item.label }}</span>
            <span v-if="item.isDefault" class="usage-stats-account-panel-default-tag">默认</span>
          </button>
          <button
            v-if="item.removable"
            type="button"
            class="usage-stats-account-panel-remove"
            :aria-label="`移除${item.label}`"
            :disabled="disabled"
            @click.stop="emit('remove', item.account.id)"
          >
            <CloseOutlined />
          </button>
        </div>
      </div>
      <div v-else class="usage-stats-account-panel-hint">暂无账户，等待用量数据或搜索添加</div>
    </section>
    <section class="usage-stats-account-panel-section">
      <div class="usage-stats-account-panel-head">
        <span class="usage-stats-account-panel-title">添加账户</span>
      </div>
      <a-input v-model:value="keyword" allow-clear placeholder="输入账户名称搜索添加" :disabled="disabled" />
      <a-spin v-if="keyword.trim()" class="usage-stats-account-panel-results" :spinning="Boolean(loading)">
        <div v-if="candidateRows.length" class="usage-stats-account-panel-option-list">
          <button
            v-for="option in candidateRows"
            :key="option.id"
            type="button"
            class="usage-stats-account-panel-option"
            :disabled="disabled || atCap"
            @click="handleAdd(option.id)"
          >
            <span class="usage-stats-account-panel-option-name">{{ option.name }}</span>
            <span class="usage-stats-account-panel-option-provider">{{ option.providerName }}</span>
          </button>
        </div>
        <div v-else class="usage-stats-account-panel-hint">无匹配账户</div>
        <div v-if="atCap" class="usage-stats-account-panel-hint">已达展示上限，请先移除账户</div>
      </a-spin>
      <div v-else class="usage-stats-account-panel-hint">输入名称搜索账户（默认已展示用量前 10）</div>
    </section>
    <div class="usage-stats-account-panel-footer">
      <a-button :disabled="disabled" @click="handleResetDefaults">恢复默认</a-button>
      <span class="usage-stats-account-panel-footer-hint">最多展示 {{ maxAccounts }} 个账户</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { CloseOutlined } from '@ant-design/icons-vue'
import { computed, ref, watch } from 'vue'

import type { AccountUsageStatsOption } from '@/types/domain'

interface UsageStatsAccountPanelItem {
  account: { id: string }
  label: string
  color: string
  selected: boolean
  removable: boolean
  isDefault: boolean
}

const props = defineProps<{
  collectionItems: UsageStatsAccountPanelItem[]
  options: AccountUsageStatsOption[]
  collectionIds: string[]
  loading?: boolean
  maxAccounts: number
  disabled?: boolean
}>()

const emit = defineEmits<{
  (event: 'toggle', id: string): void
  (event: 'remove', id: string): void
  (event: 'add', id: string): void
  (event: 'search', keyword: string): void
  (event: 'reset-defaults'): void
}>()

const keyword = ref('')
const candidateRows = computed(() => props.options.filter((option) => !props.collectionIds.includes(option.id)))
const atCap = computed(() => props.collectionItems.length >= props.maxAccounts)
// “恢复默认”清空本地搜索词时不触发搜索请求，避免恢复动作额外请求账户候选。
let suppressNextSearchEmit = false

watch(keyword, (value) => {
  if (suppressNextSearchEmit) {
    suppressNextSearchEmit = false
    return
  }
  emit('search', value)
})

function handleAdd(id: string) {
  if (props.disabled || atCap.value) return
  emit('add', id)
}

function handleResetDefaults() {
  if (keyword.value) {
    suppressNextSearchEmit = true
    keyword.value = ''
  }
  emit('reset-defaults')
}
</script>

<style scoped>
.usage-stats-account-panel {
  display: grid;
  gap: 14px;
}

.usage-stats-account-panel-section {
  display: grid;
  gap: 8px;
}

.usage-stats-account-panel-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}

.usage-stats-account-panel-title {
  color: var(--juhe-fg);
  font-size: 13px;
  font-weight: 600;
}

.usage-stats-account-panel-count {
  color: var(--juhe-muted);
  font-size: 12px;
}

.usage-stats-account-panel-collection {
  display: flex;
  flex-direction: column;
  max-height: 240px;
  overflow-y: auto;
}

.usage-stats-account-panel-row {
  display: flex;
  align-items: center;
  border-radius: 6px;
  transition: opacity 0.16s ease;
}

.usage-stats-account-panel-row:hover {
  background: var(--juhe-accent-soft);
}

.usage-stats-account-panel-row.hidden {
  opacity: 0.46;
}

.usage-stats-account-panel-main {
  display: inline-flex;
  align-items: center;
  flex: 1 1 auto;
  min-width: 0;
  gap: 6px;
  padding: 5px 8px;
  border: 0;
  color: var(--juhe-fg-soft);
  background: transparent;
  font-size: 13px;
  line-height: 20px;
  text-align: left;
  cursor: pointer;
}

.usage-stats-account-panel-main:disabled {
  cursor: default;
}

.usage-stats-account-panel-dot {
  width: 10px;
  height: 10px;
  flex: 0 0 auto;
  border-radius: 50%;
}

.usage-stats-account-panel-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.usage-stats-account-panel-default-tag {
  flex: 0 0 auto;
  padding: 0 4px;
  border-radius: 4px;
  color: var(--juhe-muted);
  background: var(--juhe-accent-soft);
  font-size: 12px;
  line-height: 16px;
}

.usage-stats-account-panel-remove {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  flex: 0 0 auto;
  margin-right: 4px;
  padding: 0;
  border: 0;
  border-radius: 5px;
  color: var(--juhe-muted);
  background: transparent;
  font-size: 12px;
  cursor: pointer;
  transition: background-color 0.16s ease, color 0.16s ease;
}

.usage-stats-account-panel-remove:hover {
  color: var(--juhe-danger);
  background: var(--juhe-danger-soft);
}

.usage-stats-account-panel-option-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 200px;
  overflow-y: auto;
}

.usage-stats-account-panel-option {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 2px;
  padding: 5px 8px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  text-align: left;
  cursor: pointer;
}

.usage-stats-account-panel-option:hover:not(:disabled) {
  background: var(--juhe-accent-soft);
}

.usage-stats-account-panel-option:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}

.usage-stats-account-panel-option-name {
  max-width: 100%;
  overflow: hidden;
  color: var(--juhe-fg-soft);
  font-size: 13px;
  line-height: 18px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.usage-stats-account-panel-option-provider {
  max-width: 100%;
  overflow: hidden;
  color: var(--juhe-muted);
  font-size: 12px;
  line-height: 16px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.usage-stats-account-panel-hint {
  color: var(--juhe-muted);
  font-size: 12px;
}

.usage-stats-account-panel-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.usage-stats-account-panel-footer-hint {
  color: var(--juhe-muted);
  font-size: 12px;
}
</style>
