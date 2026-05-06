<template>
  <a-layout-sider
    v-if="!isMobile"
    width="232"
    :collapsed-width="80"
    :collapsed="collapsed"
    :trigger="null"
    collapsible
    theme="dark"
    class="sidebar"
  >
    <div class="brand">
      <img class="brand-icon" :src="appIcon" :alt="`${appName} 图标`" />
      <span class="brand-text">{{ appName }}</span>
    </div>
    <a-menu :selectedKeys="selectedKeys" theme="dark" mode="inline" :items="menuItems" @click="emit('menu-click', $event)" />
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
    <a-menu :selectedKeys="selectedKeys" theme="dark" mode="inline" :items="menuItems" @click="emit('menu-click', $event)" />
  </a-drawer>
</template>

<script setup lang="ts">
import { MenuFoldOutlined, MenuUnfoldOutlined } from '@ant-design/icons-vue'
import type { ItemType } from 'ant-design-vue'

const open = defineModel<boolean>('open', { required: true })
const collapsed = defineModel<boolean>('collapsed', { required: true })

defineProps<{
  appIcon: string
  appName: string
  isMobile: boolean
  menuItems: ItemType[]
  selectedKeys: string[]
}>()

const emit = defineEmits<{
  (event: 'menu-click', menuEvent: { key: string | number }): void
}>()
</script>

<style scoped>
.sidebar {
  position: sticky;
  top: 0;
  height: 100vh;
  overflow: hidden;
  background: linear-gradient(180deg, #061a2e 0%, #03111f 100%) !important;
  box-shadow: 8px 0 24px rgba(3, 17, 31, 0.08);
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
  scrollbar-color: rgba(226, 232, 240, 0.28) transparent;
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
  background-color: rgba(226, 232, 240, 0.24);
  background-clip: content-box;
  border: 2px solid transparent;
  border-radius: 999px;
}

.sidebar :deep(.ant-menu::-webkit-scrollbar-thumb:hover) {
  background-color: rgba(226, 232, 240, 0.4);
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
  color: #fff;
  font-size: 18px;
  font-weight: 800;
  letter-spacing: 0.2px;
  line-height: 1;
  white-space: nowrap;
}

.brand-icon {
  width: 28px;
  height: 28px;
  flex: 0 0 auto;
  padding: 5px;
  background: rgba(255, 255, 255, 0.92);
  border-radius: 9px;
  box-shadow: 0 8px 20px rgba(22, 119, 255, 0.2);
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
  color: rgba(255, 255, 255, 0.78);
  background: rgba(255, 255, 255, 0.06);
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 8px;
  cursor: pointer;
  transition:
    color 0.2s,
    background 0.2s,
    border-color 0.2s;
}

.collapse-toggle:hover {
  color: #fff;
  background: rgba(22, 119, 255, 0.18);
  border-color: rgba(22, 119, 255, 0.32);
}

.collapse-toggle span {
  font-size: 14px;
}

.brand-drawer {
  height: 72px;
  padding: 12px 20px 0;
}

:global(.mobile-drawer .ant-drawer-content-wrapper) {
  box-shadow: 18px 0 32px rgba(3, 17, 31, 0.2);
}

:global(.mobile-drawer .ant-drawer-content) {
  background: linear-gradient(180deg, #061a2e 0%, #03111f 100%);
}

:global(.mobile-drawer .ant-menu-dark) {
  background: transparent;
}

:deep(.ant-menu-dark) {
  background: transparent;
}

:deep(.ant-menu-dark .ant-menu-item) {
  height: 40px;
  margin: 6px 6px;
  border-radius: 8px;
  line-height: 40px;
}

:deep(.ant-menu-item-group-title) {
  padding: 14px 20px 4px;
  color: rgba(255, 255, 255, 0.48);
  font-size: 12px;
  line-height: 18px;
}

:deep(.ant-menu-inline-collapsed .ant-menu-item-group-title) {
  display: none;
}

:deep(.ant-menu-dark .ant-menu-item-selected) {
  background: linear-gradient(135deg, #1677ff 0%, #2f80ed 100%);
  box-shadow: 0 8px 20px rgba(22, 119, 255, 0.26);
}
</style>
