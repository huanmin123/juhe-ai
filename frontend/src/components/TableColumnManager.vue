<template>
  <a-tooltip title="配置表格列显示、顺序和固定位置">
    <a-button class="table-column-manager-trigger" @click="openModal">
      <template #icon>
        <SettingOutlined />
      </template>
      {{ buttonText }}
    </a-button>
  </a-tooltip>

  <a-modal
    v-model:open="modalOpen"
    :title="title"
    width="640px"
    class="table-column-manager-modal"
    @cancel="closeModal"
  >
    <div class="table-column-manager">
      <div class="table-column-manager-header">
        <span>列名</span>
        <span>显示</span>
        <span>固定位置</span>
        <span>顺序</span>
      </div>
      <div class="table-column-manager-list">
        <div
          v-for="item in draftItems"
          :key="item.key"
          class="table-column-manager-item"
          :class="{
            dragging: draggedKey === item.key,
            'drag-over': dragOverKey === item.key,
            hidden: !item.visible
          }"
          draggable="true"
          @dragstart="handleDragStart($event, item.key)"
          @dragover.prevent="handleDragOver(item.key)"
          @dragleave="handleDragLeave(item.key)"
          @drop.prevent="handleDrop($event, item.key)"
          @dragend="clearDragState"
        >
          <DragOutlined class="table-column-manager-drag" />
          <span class="table-column-manager-title" :title="item.title">{{ item.title }}</span>
          <a-checkbox
            :checked="item.visible"
            :disabled="visibilityDisabled(item)"
            @update:checked="updateItemVisibility(item.key, Boolean($event))"
          >
            显示
          </a-checkbox>
          <a-segmented
            size="small"
            :value="item.fixed"
            :options="fixedOptions"
            @update:value="updateItemFixed(item.key, $event)"
          />
          <span class="table-column-manager-order">
            <a-tooltip title="上移">
              <a-button size="small" type="text" :disabled="!canMoveItemByOffset(item.key, -1)" @click="moveItemByOffset(item.key, -1)">
                <template #icon>
                  <ArrowUpOutlined />
                </template>
              </a-button>
            </a-tooltip>
            <a-tooltip title="下移">
              <a-button size="small" type="text" :disabled="!canMoveItemByOffset(item.key, 1)" @click="moveItemByOffset(item.key, 1)">
                <template #icon>
                  <ArrowDownOutlined />
                </template>
              </a-button>
            </a-tooltip>
          </span>
        </div>
      </div>
    </div>

    <template #footer>
      <div class="table-column-manager-footer">
        <a-button @click="handleReset">恢复默认</a-button>
        <span class="table-column-manager-footer-actions">
          <a-button @click="closeModal">取消</a-button>
          <a-button type="primary" @click="saveSettings">保存</a-button>
        </span>
      </div>
    </template>
  </a-modal>
</template>

<script setup lang="ts">
import { ArrowDownOutlined, ArrowUpOutlined, DragOutlined, SettingOutlined } from '@ant-design/icons-vue'
import { computed, ref } from 'vue'

import {
  buildTableColumnManagerItems,
  normalizeTableColumnFixedOrder,
  tableColumnSettingFromItem,
  type TableColumnFixed,
  type TableColumnManagerItem,
  type TableColumnSetting
} from './tableColumnSettings'

const props = withDefaults(defineProps<{
  buttonText?: string
  columns: Array<Record<string, any>>
  minVisible?: number
  requiredKeys?: string[]
  settings?: TableColumnSetting[]
  title?: string
}>(), {
  buttonText: '列管理',
  minVisible: 1,
  requiredKeys: () => [],
  settings: () => [],
  title: '列管理'
})

const emit = defineEmits<{
  (event: 'reset'): void
  (event: 'update:settings', value: TableColumnSetting[]): void
}>()

const modalOpen = ref(false)
const draftItems = ref<TableColumnManagerItem[]>([])
const draggedKey = ref<string>()
const dragOverKey = ref<string>()
const fixedOptions = [
  { label: '不固定', value: 'none' },
  { label: '左侧', value: 'left' },
  { label: '右侧', value: 'right' }
]

const visibleCount = computed(() => draftItems.value.filter((item) => item.visible).length)
const minVisibleCount = computed(() => Math.max(0, props.minVisible))

function openModal(): void {
  draftItems.value = buildDraftItems(props.settings)
  modalOpen.value = true
}

function closeModal(): void {
  modalOpen.value = false
  clearDragState()
}

function handleReset(): void {
  emit('reset')
  draftItems.value = buildDraftItems([])
}

function saveSettings(): void {
  emit('update:settings', normalizeDraftItemOrder(draftItems.value).map(tableColumnSettingFromItem))
  closeModal()
}

function buildDraftItems(settings: TableColumnSetting[]): TableColumnManagerItem[] {
  return buildTableColumnManagerItems(props.columns, settings, {
    minVisible: props.minVisible,
    requiredKeys: props.requiredKeys
  })
}

function visibilityDisabled(item: TableColumnManagerItem): boolean {
  return item.required || (item.visible && visibleCount.value <= minVisibleCount.value)
}

function updateItemVisibility(key: string, visible: boolean): void {
  draftItems.value = buildDraftItems(draftItems.value.map((item) => (
    item.key === key ? { ...item, visible } : item
  )))
}

function updateItemFixed(key: string, value: unknown): void {
  const fixed = normalizeFixedValue(value)
  draftItems.value = normalizeDraftItemOrder(draftItems.value.map((item) => (
    item.key === key ? { ...item, fixed } : item
  )))
}

function canMoveItemByOffset(key: string, offset: number): boolean {
  const fromIndex = draftItems.value.findIndex((item) => item.key === key)
  const toIndex = fromIndex + offset
  if (fromIndex < 0 || toIndex < 0 || toIndex >= draftItems.value.length) return false
  return draftItems.value[fromIndex].fixed === draftItems.value[toIndex].fixed
}

function moveItemByOffset(key: string, offset: number): void {
  const fromIndex = draftItems.value.findIndex((item) => item.key === key)
  const toIndex = fromIndex + offset
  if (!canMoveItemByOffset(key, offset)) return
  const nextItems = [...draftItems.value]
  const [item] = nextItems.splice(fromIndex, 1)
  nextItems.splice(toIndex, 0, item)
  draftItems.value = normalizeDraftItemOrder(nextItems)
}

function handleDragStart(event: DragEvent, key: string): void {
  draggedKey.value = key
  event.dataTransfer?.setData('text/plain', key)
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = 'move'
  }
}

function handleDragOver(key: string): void {
  if (draggedKey.value && canDropItemOnTarget(draggedKey.value, key)) {
    dragOverKey.value = key
  }
}

function handleDragLeave(key: string): void {
  if (dragOverKey.value === key) {
    dragOverKey.value = undefined
  }
}

function handleDrop(event: DragEvent, targetKey: string): void {
  const sourceKey = draggedKey.value
  if (!sourceKey || sourceKey === targetKey) {
    clearDragState()
    return
  }
  moveItemToDropPosition(sourceKey, targetKey, isDropAfterTarget(event))
  clearDragState()
}

function moveItemToDropPosition(sourceKey: string, targetKey: string, afterTarget: boolean): void {
  if (!canDropItemOnTarget(sourceKey, targetKey)) return
  const sourceIndex = draftItems.value.findIndex((item) => item.key === sourceKey)
  const targetIndex = draftItems.value.findIndex((item) => item.key === targetKey)
  if (sourceIndex < 0 || targetIndex < 0) return
  const nextItems = [...draftItems.value]
  const [sourceItem] = nextItems.splice(sourceIndex, 1)
  const nextTargetIndex = nextItems.findIndex((item) => item.key === targetKey)
  nextItems.splice(nextTargetIndex + (afterTarget ? 1 : 0), 0, sourceItem)
  draftItems.value = normalizeDraftItemOrder(nextItems)
}

function canDropItemOnTarget(sourceKey: string, targetKey: string): boolean {
  if (sourceKey === targetKey) return false
  const sourceItem = draftItems.value.find((item) => item.key === sourceKey)
  const targetItem = draftItems.value.find((item) => item.key === targetKey)
  return Boolean(sourceItem && targetItem && sourceItem.fixed === targetItem.fixed)
}

function isDropAfterTarget(event: DragEvent): boolean {
  const target = event.currentTarget
  if (!(target instanceof HTMLElement)) return false
  const rect = target.getBoundingClientRect()
  return event.clientY > rect.top + rect.height / 2
}

function clearDragState(): void {
  draggedKey.value = undefined
  dragOverKey.value = undefined
}

function normalizeFixedValue(value: unknown): TableColumnFixed {
  if (value === 'left' || value === 'right') return value
  return 'none'
}

function normalizeDraftItemOrder(items: TableColumnManagerItem[]): TableColumnManagerItem[] {
  return normalizeTableColumnFixedOrder(items)
}
</script>

<style scoped>
.table-column-manager-trigger {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.table-column-manager {
  display: grid;
  gap: 10px;
}

.table-column-manager-header,
.table-column-manager-item {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 74px 188px 74px;
  align-items: center;
  gap: 10px;
}

.table-column-manager-header {
  padding: 0 12px;
  color: #64748b;
  font-size: 12px;
  font-weight: 600;
}

.table-column-manager-list {
  display: grid;
  max-height: min(56vh, 520px);
  gap: 8px;
  overflow-y: auto;
  padding-right: 2px;
}

.table-column-manager-item {
  position: relative;
  padding: 10px 12px 10px 38px;
  border: 1px solid #e8edf5;
  border-radius: 8px;
  background: #fff;
  transition: border-color 0.16s ease, background-color 0.16s ease, opacity 0.16s ease;
}

.table-column-manager-item.hidden {
  background: #f8fafc;
}

.table-column-manager-item.dragging {
  opacity: 0.48;
}

.table-column-manager-item.drag-over {
  border-color: #1677ff;
  background: #f8fbff;
}

.table-column-manager-drag {
  position: absolute;
  left: 12px;
  color: #94a3b8;
  cursor: grab;
}

.table-column-manager-title {
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.table-column-manager-item.hidden .table-column-manager-title {
  color: #64748b;
}

.table-column-manager-order {
  display: inline-flex;
  justify-content: flex-end;
  gap: 2px;
}

.table-column-manager-footer {
  display: flex;
  justify-content: space-between;
  gap: 12px;
}

.table-column-manager-footer-actions {
  display: inline-flex;
  gap: 8px;
}

@media (max-width: 640px) {
  .table-column-manager-header {
    display: none;
  }

  .table-column-manager-item {
    grid-template-columns: minmax(0, 1fr) 74px;
    padding-left: 38px;
  }

  .table-column-manager-item :deep(.ant-segmented),
  .table-column-manager-order {
    grid-column: 1 / -1;
  }

  .table-column-manager-order {
    justify-content: flex-start;
  }

  .table-column-manager-footer,
  .table-column-manager-footer-actions {
    display: grid;
    grid-template-columns: 1fr;
  }
}
</style>
