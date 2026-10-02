<template>
  <a-layout-sider
    v-if="!isMobile"
    width="232"
    :collapsed-width="80"
    :collapsed="collapsed"
    :trigger="null"
    collapsible
    theme="light"
    class="sidebar"
  >
    <div class="brand">
      <img class="brand-icon" :src="appIcon" :alt="`${appName} 图标`" />
      <span class="brand-text">{{ appName }}</span>
    </div>
    <a-menu :openKeys="menuOpenKeys" :selectedKeys="selectedKeys" theme="light" mode="inline" :items="menuItems" @click="emit('menu-click', $event)" @openChange="handleOpenChange" />
    <button class="collapse-toggle" type="button" @click="collapsed = !collapsed">
      <MenuUnfoldOutlined v-if="collapsed" />
      <MenuFoldOutlined v-else />
      <span v-if="!collapsed">收起</span>
    </button>
  </a-layout-sider>
  <a-drawer
    v-else
    v-model:open="open"
    placement="left"
    :closable="false"
    :width="280"
    root-class-name="mobile-drawer"
    :body-style="{ padding: '0', background: 'transparent' }"
  >
    <div class="brand brand-drawer">
      <img class="brand-icon" :src="appIcon" :alt="`${appName} 图标`" />
      <span class="brand-text">{{ appName }}</span>
    </div>
    <a-menu :openKeys="menuOpenKeys" :selectedKeys="selectedKeys" theme="light" mode="inline" :items="menuItems" @click="emit('menu-click', $event)" @openChange="handleOpenChange" />
  </a-drawer>
</template>

<script setup lang="ts">
import { MenuFoldOutlined, MenuUnfoldOutlined } from '@ant-design/icons-vue'
import type { ItemType } from 'ant-design-vue'
import { ref, watch } from 'vue'

const open = defineModel<boolean>('open', { required: true })
const collapsed = defineModel<boolean>('collapsed', { required: true })

const props = defineProps<{
  appIcon: string
  appName: string
  isMobile: boolean
  menuItems: ItemType[]
  openKeys: string[]
  selectedKeys: string[]
}>()

const emit = defineEmits<{
  (event: 'menu-click', menuEvent: { key: string | number }): void
}>()

const menuOpenKeys = ref<string[]>([])

function handleOpenChange(keys: Array<string | number>) {
  menuOpenKeys.value = keys.map((key) => String(key))
}

watch(
  () => props.openKeys,
  (keys) => {
    if (!keys.length) return
    menuOpenKeys.value = [...new Set([...menuOpenKeys.value, ...keys])]
  },
  { immediate: true }
)
</script>

<style scoped>
.sidebar {
  position: sticky;
  top: 0;
  height: 100vh;
  overflow: hidden;
  background: var(--juhe-rail) !important;
  border-right: 1px solid var(--juhe-border);
  box-shadow: none;
}

.sidebar :deep(.ant-layout-sider-children) {
  display: flex;
  flex-direction: column;
  min-height: 100%;
}

.sidebar :deep(.ant-menu) {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  overflow-x: hidden;
  scrollbar-color: rgba(60, 68, 70, 0.3) transparent;
  scrollbar-gutter: stable;
  scrollbar-width: thin;
}

.sidebar :deep(.ant-menu::-webkit-scrollbar) {
  width: 8px;
}

.sidebar :deep(.ant-menu::-webkit-scrollbar-track) {
  background: transparent;
}

.sidebar :deep(.ant-menu::-webkit-scrollbar-thumb) {
  min-height: 44px;
  background-color: rgba(60, 68, 70, 0.3);
  background-clip: content-box;
  border: 2px solid transparent;
  border-radius: 999px;
}

.sidebar :deep(.ant-menu::-webkit-scrollbar-thumb:hover) {
  background-color: rgba(60, 68, 70, 0.5);
}

.sidebar :deep(.ant-menu::-webkit-scrollbar-button) {
  width: 0;
  height: 0;
  display: none;
}

.brand {
  height: 76px;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 22px 0;
  overflow: hidden;
  color: var(--juhe-fg);
  font-family: var(--juhe-font-display);
  font-size: 16px;
  font-weight: 650;
  letter-spacing: 0.2px;
  line-height: 1;
  white-space: nowrap;
}

.brand-icon {
  width: 28px;
  height: 28px;
  flex: 0 0 auto;
  display: block;
}

.brand-text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}

.collapse-toggle {
  width: calc(100% - 12px);
  height: 40px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  margin: 10px 6px 14px;
  padding: 0 12px;
  color: var(--juhe-muted);
  background: #fff;
  border: 1px solid var(--juhe-border);
  border-radius: 8px;
  cursor: pointer;
  transition:
    color 0.2s,
    background 0.2s,
    border-color 0.2s;
}

.collapse-toggle:hover {
  color: var(--juhe-accent);
  background: var(--juhe-accent-soft);
  border-color: var(--juhe-accent);
}

.collapse-toggle span {
  font-size: 14px;
}

.brand-drawer {
  height: 72px;
  padding: 12px 20px 0;
  color: var(--juhe-fg);
}

:global(.mobile-drawer .ant-drawer-content-wrapper) {
  box-shadow: 18px 0 32px rgba(34, 40, 43, 0.2);
}

:global(.mobile-drawer .ant-drawer-content) {
  background: var(--juhe-rail);
}

:global(.mobile-drawer .ant-menu-light) {
  background: transparent;
}

:global(.mobile-drawer .ant-menu-item) {
  height: 36px;
  margin: 4px 8px;
  border-radius: 9px;
  line-height: 36px;
}

:global(.mobile-drawer .ant-menu-item-selected) {
  position: relative;
  background: var(--juhe-accent-soft);
  color: var(--juhe-accent);
}

:deep(.ant-menu-light) {
  background: transparent;
}

:deep(.ant-menu-item) {
  height: 36px;
  margin: 4px 8px;
  border-radius: 9px;
  line-height: 36px;
}

:deep(.ant-menu-item-group-title) {
  padding: 14px 20px 4px;
  color: var(--juhe-faint);
  font-size: 11px;
  letter-spacing: 0.12em;
  line-height: 18px;
}

:deep(.ant-menu-inline-collapsed .ant-menu-item-group-title) {
  display: none;
}

:deep(.ant-menu-item-selected) {
  position: relative;
  background: var(--juhe-accent-soft);
  color: var(--juhe-accent);
}

:deep(.ant-menu-item-selected)::before {
  content: "";
  position: absolute;
  left: -8px;
  top: 8px;
  bottom: 8px;
  width: 3px;
  border-radius: 3px 2px 2px 3px;
  background: linear-gradient(180deg, var(--juhe-fg), var(--juhe-accent));
  transform: rotate(-0.5deg);
}
</style>
