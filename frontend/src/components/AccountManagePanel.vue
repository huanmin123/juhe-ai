<template>
  <div class="account-manage-panel">
    <section class="account-manage-panel-section">
      <div class="account-manage-panel-head">
        <span class="account-manage-panel-title">当前账户</span>
        <span class="account-manage-panel-count">{{ collectionItems.length }}{{ collectionMax ? '/' + collectionMax : '' }}</span>
      </div>
      <div v-if="collectionItems.length" class="account-manage-panel-collection">
        <div
          v-for="item in collectionItems"
          :key="item.id"
          class="account-manage-panel-row"
          :class="{ hidden: !item.visible }"
        >
          <button
            type="button"
            class="account-manage-panel-main"
            :aria-pressed="item.visible"
            :disabled="disabled"
            @click="emit('toggle', item.id)"
          >
            <span class="account-manage-panel-dot" :style="{ backgroundColor: item.color }" />
            <span class="account-manage-panel-name">{{ item.label }}</span>
            <span v-if="item.isDefault" class="account-manage-panel-default-tag">默认</span>
          </button>
          <button
            v-if="item.removable"
            type="button"
            class="account-manage-panel-remove"
            :aria-label="`移除${item.label}`"
            :disabled="disabled"
            @click.stop="emit('remove', item.id)"
          >
            <CloseOutlined />
          </button>
        </div>
      </div>
      <div v-else class="account-manage-panel-hint">暂无账户，等待用量数据或搜索添加</div>
    </section>
    <section class="account-manage-panel-section">
      <div class="account-manage-panel-head">
        <span class="account-manage-panel-title">添加账户</span>
      </div>
      <a-input v-model:value="keyword" allow-clear placeholder="输入账户名称搜索添加" :disabled="disabled" />
      <a-spin v-if="keyword.trim()" class="account-manage-panel-results" :spinning="Boolean(loading)">
        <div v-if="candidateRows.length" class="account-manage-panel-option-list">
          <button
            v-for="option in candidateRows"
            :key="option.id"
            type="button"
            class="account-manage-panel-option"
            :disabled="disabled || atCap"
            @click="handleAdd(option.id)"
          >
            <span class="account-manage-panel-option-name">{{ option.name }}</span>
            <span v-if="option.providerName" class="account-manage-panel-option-provider">{{ option.providerName }}</span>
          </button>
        </div>
        <div v-else class="account-manage-panel-hint">无匹配账户</div>
        <div v-if="atCap" class="account-manage-panel-hint">已达添加上限，请先移除添加的账户</div>
      </a-spin>
      <div v-else class="account-manage-panel-hint">{{ searchHint }}</div>
    </section>
    <div class="account-manage-panel-footer">
      <a-button :disabled="disabled" @click="handleResetDefaults">恢复默认</a-button>
      <span class="account-manage-panel-footer-hint">最多添加 {{ maxAddedAccounts }} 个账户</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { CloseOutlined } from '@ant-design/icons-vue'
import { computed, ref, watch } from 'vue'

export interface AccountManagePanelItem {
  id: string
  label: string
  color: string
  visible: boolean
  removable: boolean
  isDefault: boolean
}

export interface AccountManagePanelOption {
  id: string
  name: string
  providerName?: string
}

const props = withDefaults(defineProps<{
  collectionItems: AccountManagePanelItem[]
  options: AccountManagePanelOption[]
  maxAddedAccounts: number
  collectionMax?: number
  loading?: boolean
  disabled?: boolean
  searchHint?: string
}>(), {
  loading: false,
  disabled: false,
  searchHint: '输入名称搜索账户（默认已展示用量前 10）'
})

const emit = defineEmits<{
  (event: 'toggle', id: string): void
  (event: 'remove', id: string): void
  (event: 'add', id: string): void
  (event: 'search', keyword: string): void
  (event: 'reset-defaults'): void
}>()

const keyword = ref('')
const collectionIdSet = computed(() => new Set(props.collectionItems.map((item) => item.id)))
const candidateRows = computed(() => props.options.filter((option) => !collectionIdSet.value.has(option.id)))
const atCap = computed(() => props.collectionItems.filter((item) => !item.isDefault).length >= props.maxAddedAccounts)
// “恢复默认”或添加账户后清空本地搜索词时不触发搜索请求，避免动作本身额外请求账户候选。
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
  // 添加后清空本地搜索词并隐藏候选列表；候选在下次展开面板或重新搜索时刷新。
  if (keyword.value) {
    suppressNextSearchEmit = true
    keyword.value = ''
  }
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
.account-manage-panel {
  display: grid;
  gap: 14px;
}

.account-manage-panel-section {
  display: grid;
  gap: 8px;
}

.account-manage-panel-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}

.account-manage-panel-title {
  color: var(--juhe-fg);
  font-size: 13px;
  font-weight: 600;
}

.account-manage-panel-count {
  color: var(--juhe-muted);
  font-size: 12px;
}

.account-manage-panel-collection {
  display: flex;
  flex-direction: column;
  max-height: 240px;
  overflow-y: auto;
}

.account-manage-panel-row {
  display: flex;
  align-items: center;
  border-radius: 6px;
  transition: opacity 0.16s ease;
}

.account-manage-panel-row:hover {
  background: var(--juhe-accent-soft);
}

.account-manage-panel-row.hidden {
  opacity: 0.46;
}

.account-manage-panel-main {
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

.account-manage-panel-main:disabled {
  cursor: default;
}

.account-manage-panel-dot {
  width: 10px;
  height: 10px;
  flex: 0 0 auto;
  border-radius: 50%;
}

.account-manage-panel-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.account-manage-panel-default-tag {
  flex: 0 0 auto;
  padding: 0 4px;
  border-radius: 4px;
  color: var(--juhe-muted);
  background: var(--juhe-accent-soft);
  font-size: 12px;
  line-height: 16px;
}

.account-manage-panel-remove {
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

.account-manage-panel-remove:hover {
  color: var(--juhe-danger);
  background: var(--juhe-danger-soft);
}

.account-manage-panel-option-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 200px;
  overflow-y: auto;
}

.account-manage-panel-option {
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

.account-manage-panel-option:hover:not(:disabled) {
  background: var(--juhe-accent-soft);
}

.account-manage-panel-option:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}

.account-manage-panel-option-name {
  max-width: 100%;
  overflow: hidden;
  color: var(--juhe-fg-soft);
  font-size: 13px;
  line-height: 18px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.account-manage-panel-option-provider {
  max-width: 100%;
  overflow: hidden;
  color: var(--juhe-muted);
  font-size: 12px;
  line-height: 16px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.account-manage-panel-hint {
  color: var(--juhe-muted);
  font-size: 12px;
}

.account-manage-panel-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.account-manage-panel-footer-hint {
  color: var(--juhe-muted);
  font-size: 12px;
}
</style>
