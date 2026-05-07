<template>
  <div class="row-actions" :class="[`row-actions-${variant}`]" :style="rootStyle">
    <template v-for="action in actions" :key="action.key">
      <a-popconfirm
        v-if="action.confirmTitle"
        :title="action.confirmTitle"
        :ok-text="action.confirmOkText || '确认'"
        cancel-text="取消"
        :disabled="action.disabled"
        @confirm="emitAction(action)"
      >
        <a-tooltip :title="iconOnly ? action.label : undefined">
          <a-button
            class="row-action-button"
            :class="actionClass(action)"
            :danger="isDanger(action)"
            :disabled="action.disabled"
            :size="size"
            :type="buttonType(action)"
          >
            <template #icon>
              <component :is="actionIcon(action)" />
            </template>
            <span v-if="!iconOnly">{{ action.label }}</span>
          </a-button>
        </a-tooltip>
      </a-popconfirm>
      <a-tooltip v-else :title="iconOnly ? action.label : undefined">
        <a-button
          class="row-action-button"
          :class="actionClass(action)"
          :danger="isDanger(action)"
          :disabled="action.disabled"
          :size="size"
          :type="buttonType(action)"
          @click="emitAction(action)"
        >
          <template #icon>
            <component :is="actionIcon(action)" />
          </template>
          <span v-if="!iconOnly">{{ action.label }}</span>
        </a-button>
      </a-tooltip>
    </template>

    <a-dropdown v-if="moreActions.length" :trigger="['click']">
      <a-tooltip :title="iconOnly ? moreTitle : undefined">
        <a-button class="row-action-button row-action-more-button" :size="size" :type="iconOnly ? 'text' : 'default'">
          <template #icon>
            <MoreOutlined />
          </template>
          <span v-if="!iconOnly">更多</span>
        </a-button>
      </a-tooltip>
      <template #overlay>
        <a-menu class="row-action-menu" @click="handleMenuClick">
          <template v-for="item in moreActions" :key="item.key">
            <a-sub-menu v-if="item.children?.length" :key="item.key" :disabled="item.disabled">
              <template #title>
                <span class="row-action-menu-label" :class="menuItemClass(item)">
                  <component :is="actionIcon(item)" class="row-action-menu-icon" />
                  <span>{{ item.label }}</span>
                </span>
              </template>
              <a-menu-item
                v-for="child in item.children"
                :key="child.key"
                :danger="isDanger(child)"
                :disabled="child.disabled"
              >
                <span class="row-action-menu-label" :class="menuItemClass(child)">
                  <component :is="actionIcon(child)" class="row-action-menu-icon" />
                  <span>{{ child.label }}</span>
                </span>
              </a-menu-item>
            </a-sub-menu>
            <a-menu-item v-else :key="item.key" :danger="isDanger(item)" :disabled="item.disabled">
              <span class="row-action-menu-label" :class="menuItemClass(item)">
                <component :is="actionIcon(item)" class="row-action-menu-icon" />
                <span>{{ item.label }}</span>
              </span>
            </a-menu-item>
          </template>
        </a-menu>
      </template>
    </a-dropdown>
  </div>
</template>

<script setup lang="ts">
import {
  CheckCircleOutlined,
  CopyOutlined,
  DeleteOutlined,
  DisconnectOutlined,
  EditOutlined,
  ExperimentOutlined,
  EyeOutlined,
  FileSearchOutlined,
  KeyOutlined,
  LinkOutlined,
  MoreOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  ReloadOutlined,
  RollbackOutlined,
  SettingOutlined,
  SwapOutlined,
  TeamOutlined,
  ThunderboltOutlined
} from '@ant-design/icons-vue'
import { computed, type CSSProperties } from 'vue'

import type { RowActionIcon, RowActionItem, RowActionTone } from './rowActions'

type ButtonSize = 'small' | 'middle' | 'large'
type RowActionVariant = 'icon' | 'button'

const props = withDefaults(defineProps<{
  actions?: RowActionItem[]
  moreActions?: RowActionItem[]
  moreTitle?: string
  size?: ButtonSize
  variant?: RowActionVariant
}>(), {
  actions: () => [],
  moreActions: () => [],
  moreTitle: '更多操作',
  size: 'small',
  variant: 'icon'
})

const emit = defineEmits<{
  (event: 'action-click', key: string): void
}>()

const iconMap = {
  bind: LinkOutlined,
  copy: CopyOutlined,
  delete: DeleteOutlined,
  detail: FileSearchOutlined,
  disable: PauseCircleOutlined,
  edit: EditOutlined,
  enable: PlayCircleOutlined,
  members: TeamOutlined,
  migrate: SwapOutlined,
  more: MoreOutlined,
  password: KeyOutlined,
  pause: PauseCircleOutlined,
  refresh: ReloadOutlined,
  reset: RollbackOutlined,
  restore: CheckCircleOutlined,
  resume: PlayCircleOutlined,
  revoke: DisconnectOutlined,
  settings: SettingOutlined,
  superPriority: ThunderboltOutlined,
  test: ExperimentOutlined,
  view: EyeOutlined
} satisfies Record<RowActionIcon, unknown>

const iconOnly = computed(() => props.variant === 'icon')
const actionCount = computed(() => props.actions.length + (props.moreActions.length ? 1 : 0))
const rootStyle = computed(() => props.variant === 'button'
  ? ({ '--row-action-columns': String(Math.max(1, Math.min(actionCount.value, 3))) } as CSSProperties)
  : undefined)

function emitAction(action: RowActionItem) {
  if (action.disabled) return
  emit('action-click', action.key)
}

function handleMenuClick(event: { key: string | number }) {
  emit('action-click', String(event.key))
}

function actionIcon(action: RowActionItem) {
  const icon = action.icon ?? defaultIcon(action.key)
  return iconMap[icon]
}

function defaultIcon(key: string): RowActionIcon {
  if (key.includes('edit') || key.includes('config')) return 'edit'
  if (key.includes('delete') || key.includes('remove')) return 'delete'
  if (key.includes('test') || key.includes('check')) return 'test'
  if (key.includes('enable')) return 'enable'
  if (key.includes('resume')) return 'resume'
  if (key.includes('disable') || key.includes('pause')) return 'pause'
  if (key.includes('restore')) return 'restore'
  if (key.includes('reset')) return 'reset'
  if (key.includes('migrate') || key.includes('switch')) return 'migrate'
  if (key.includes('member')) return 'members'
  if (key.includes('password')) return 'password'
  if (key.includes('revoke')) return 'revoke'
  if (key.includes('copy')) return 'copy'
  if (key.includes('detail') || key.includes('usage')) return 'detail'
  return 'settings'
}

function actionTone(action: RowActionItem): RowActionTone {
  if (action.tone) return action.tone
  if (action.danger || isDangerKey(action.key)) return 'danger'
  if (action.key.includes('enable') || action.key.includes('resume') || action.key.includes('restore')) return 'success'
  if (action.key.includes('disable') || action.key.includes('pause') || action.key.includes('reset')) return 'warning'
  if (action.key.includes('migrate') || action.key.includes('bind') || action.key.includes('switch')) return 'purple'
  if (action.key.includes('test') || action.key.includes('detail') || action.key.includes('usage')) return 'info'
  if (action.key.includes('edit')) return 'primary'
  return 'default'
}

function isDanger(action: RowActionItem): boolean {
  return actionTone(action) === 'danger'
}

function isDangerKey(key: string): boolean {
  return key.includes('delete') || key.includes('remove') || key.includes('revoke')
}

function buttonType(action: RowActionItem): 'default' | 'link' | 'text' | 'primary' | 'dashed' {
  if (props.variant === 'icon') return 'text'
  return actionTone(action) === 'primary' ? 'primary' : 'default'
}

function actionClass(action: RowActionItem): string {
  return `row-action-tone-${actionTone(action)}`
}

function menuItemClass(action: RowActionItem): string {
  return `row-action-menu-tone-${actionTone(action)}`
}
</script>

<style scoped>
.row-actions {
  align-items: center;
}

.row-actions-icon {
  display: inline-flex;
  gap: 2px;
}

.row-actions-button {
  display: grid;
  grid-template-columns: repeat(var(--row-action-columns), minmax(0, 1fr));
  gap: 8px;
  width: 100%;
}

.row-action-button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
}

.row-actions-icon .row-action-button {
  width: 28px;
  min-width: 28px;
  padding-inline: 0;
}

.row-actions-button .row-action-button,
.row-actions-button :deep(.ant-dropdown-trigger),
.row-actions-button :deep(.ant-popconfirm-open) {
  width: 100%;
}

.row-action-tone-default {
  color: #64748b;
}

.row-action-tone-primary {
  color: #1677ff;
}

.row-action-tone-success {
  color: #16a34a;
}

.row-action-tone-warning {
  color: #d97706;
}

.row-action-tone-info {
  color: #0891b2;
}

.row-action-tone-purple {
  color: #7c3aed;
}

.row-action-tone-danger {
  color: #dc2626;
}

.row-actions-icon .row-action-button:hover {
  background: #f1f5f9;
}

.row-actions-icon .row-action-tone-primary:hover {
  color: #0958d9;
  background: #e6f4ff;
}

.row-actions-icon .row-action-tone-success:hover {
  color: #15803d;
  background: #f0fdf4;
}

.row-actions-icon .row-action-tone-warning:hover {
  color: #b45309;
  background: #fffbeb;
}

.row-actions-icon .row-action-tone-info:hover {
  color: #0e7490;
  background: #ecfeff;
}

.row-actions-icon .row-action-tone-purple:hover {
  color: #6d28d9;
  background: #f5f3ff;
}

.row-actions-icon .row-action-tone-danger:hover {
  color: #b91c1c;
  background: #fef2f2;
}

.row-action-more-button {
  color: #64748b;
}

.row-action-menu-label {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

.row-action-menu-icon {
  font-size: 14px;
}

.row-action-menu-tone-default .row-action-menu-icon {
  color: #64748b;
}

.row-action-menu-tone-primary .row-action-menu-icon {
  color: #1677ff;
}

.row-action-menu-tone-success .row-action-menu-icon {
  color: #16a34a;
}

.row-action-menu-tone-warning .row-action-menu-icon {
  color: #d97706;
}

.row-action-menu-tone-info .row-action-menu-icon {
  color: #0891b2;
}

.row-action-menu-tone-purple .row-action-menu-icon {
  color: #7c3aed;
}

.row-action-menu-tone-danger .row-action-menu-icon {
  color: #dc2626;
}
</style>
