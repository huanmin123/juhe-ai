<template>
  <a-layout class="app-shell" :class="{ 'app-shell-mobile': isMobile }">
    <a-layout-sider
      v-if="!isMobile"
      width="232"
      :collapsed-width="80"
      :collapsed="sidebarCollapsed"
      :trigger="null"
      collapsible
      theme="dark"
      class="sidebar"
    >
      <div class="brand">
        <img class="brand-icon" :src="appBrand.appIcon" :alt="`${appBrand.appName} 图标`" />
        <span class="brand-text">{{ appBrand.appName }}</span>
      </div>
      <a-menu :selectedKeys="selectedKeys" theme="dark" mode="inline" :items="menuItems" @click="handleMenuClick" />
      <button class="collapse-toggle" type="button" @click="sidebarCollapsed = !sidebarCollapsed">
        <MenuUnfoldOutlined v-if="sidebarCollapsed" />
        <MenuFoldOutlined v-else />
        <span v-if="!sidebarCollapsed">收起</span>
      </button>
    </a-layout-sider>
    <a-drawer
      v-else
      v-model:open="sidebarOpen"
      placement="left"
      :closable="false"
      :width="280"
      class="mobile-drawer"
      :body-style="{ padding: '0', background: 'transparent' }"
    >
      <div class="brand brand-drawer">
        <img class="brand-icon" :src="appBrand.appIcon" :alt="`${appBrand.appName} 图标`" />
        <span class="brand-text">{{ appBrand.appName }}</span>
      </div>
      <a-menu :selectedKeys="selectedKeys" theme="dark" mode="inline" :items="menuItems" @click="handleMenuClick" />
    </a-drawer>
    <a-layout class="main-shell">
      <a-layout-header class="header">
        <a-space align="center" class="header-copy">
          <a-button v-if="isMobile" type="text" class="menu-trigger" @click="sidebarOpen = true">
            <MenuOutlined />
          </a-button>
          <div>
            <div class="title">{{ currentPageTitle }}</div>
            <div class="subtitle">{{ currentPageDescription }}</div>
          </div>
        </a-space>
      </a-layout-header>
      <a-layout-content class="content">
        <router-view />
      </a-layout-content>
    </a-layout>
  </a-layout>
</template>

<script setup lang="ts">
import {
  ApartmentOutlined,
  GlobalOutlined,
  HistoryOutlined,
  MenuFoldOutlined,
  MenuOutlined,
  MenuUnfoldOutlined,
  NodeIndexOutlined,
  SettingOutlined,
  UserSwitchOutlined
} from '@ant-design/icons-vue'
import type { ItemType } from 'ant-design-vue'
import { computed, h, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { appBrand, loadAppBrandSettings } from '@/composables/useAppBrand'
import { menuRoutes } from '@/router'

const router = useRouter()
const route = useRoute()
const isMobile = ref(false)
const sidebarOpen = ref(false)
const sidebarCollapsed = ref(false)

const selectedKeys = computed(() => [route.path])
const currentPageTitle = computed(() => route.meta.title || '轻量中转管理')
const currentPageDescription = computed(() => route.meta.description || '第一期：OpenAI OAuth + API Key')

const ApiKeyMenuIcon = () =>
  h('span', { class: 'anticon menu-api-key-icon', role: 'img', 'aria-hidden': 'true' }, [
    h(
      'svg',
      {
        viewBox: '0 0 1024 1024',
        width: '1em',
        height: '1em',
        fill: 'none',
        focusable: 'false'
      },
      [
        h('circle', { cx: '336', cy: '512', r: '152', stroke: 'currentColor', 'stroke-width': '72' }),
        h('circle', { cx: '336', cy: '512', r: '42', fill: 'currentColor' }),
        h('path', {
          d: 'M488 512h360M648 512v112M768 512v96',
          stroke: 'currentColor',
          'stroke-width': '72',
          'stroke-linecap': 'round',
          'stroke-linejoin': 'round'
        })
      ]
    )
  ])

const menuIconMap = {
  '/providers': GlobalOutlined,
  '/accounts': UserSwitchOutlined,
  '/groups': ApartmentOutlined,
  '/api-keys': ApiKeyMenuIcon,
  '/proxies': NodeIndexOutlined,
  '/usage-records': HistoryOutlined,
  '/settings': SettingOutlined
}

const menuItems: ItemType[] = menuRoutes.map((item) => ({
  key: item.path,
  label: item.meta.title,
  icon: () => h(menuIconMap[item.path as keyof typeof menuIconMap])
}))

function handleMenuClick(event: { key: string }) {
  router.push(event.key)
  sidebarOpen.value = false
}

function updateViewport() {
  isMobile.value = window.innerWidth < 992
  if (!isMobile.value) {
    sidebarOpen.value = false
  }
}

function handleResize() {
  updateViewport()
}

onMounted(() => {
  updateViewport()
  loadAppBrandSettings().catch((error) => {
    console.error(error)
  })
  window.addEventListener('resize', handleResize, { passive: true })
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', handleResize)
})

watch(
  () => route.path,
  () => {
    if (isMobile.value) {
      sidebarOpen.value = false
    }
  }
)
</script>

<style scoped>
.app-shell {
  min-height: 100vh;
  background: #f5f7fb;
}

.main-shell {
  min-width: 0;
  background: #f5f7fb;
}

.sidebar {
  position: sticky;
  top: 0;
  height: 100vh;
  overflow: hidden;
  background: linear-gradient(180deg, #061a2e 0%, #03111f 100%) !important;
  box-shadow: 8px 0 24px rgba(3, 17, 31, 0.08);
}

:deep(.sidebar .ant-layout-sider-children) {
  display: flex;
  flex-direction: column;
  min-height: 100%;
}

:deep(.sidebar .ant-menu) {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  overflow-x: hidden;
}

.brand {
  height: 76px;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 22px 0;
  color: #fff;
  font-size: 18px;
  font-weight: 800;
  letter-spacing: 0.2px;
  line-height: 1;
  white-space: nowrap;
  overflow: hidden;
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

.header {
  min-height: 92px;
  height: auto;
  display: flex;
  align-items: center;
  padding: 18px 28px 16px;
  line-height: normal;
  background: rgba(255, 255, 255, 0.96);
  border-bottom: 1px solid #edf1f7;
  box-shadow: 0 8px 24px rgba(15, 23, 42, 0.04);
  z-index: 2;
}

.header-copy {
  display: flex;
  align-items: center;
  gap: 12px;
  line-height: 1.2;
}

.title {
  color: #0f172a;
  font-size: 20px;
  font-weight: 800;
  line-height: 28px;
}

.subtitle {
  color: #64748b;
  font-size: 13px;
  line-height: 20px;
}

.content {
  padding: 26px 24px 36px;
  background:
    radial-gradient(circle at 20% 0%, rgba(22, 119, 255, 0.06), transparent 28%),
    #f5f7fb;
}

.menu-trigger {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 40px;
  height: 40px;
  margin-left: -8px;
  color: #0f172a;
}

.brand-drawer {
  height: 72px;
  padding: 12px 20px 0;
}

:deep(.mobile-drawer .ant-drawer-content-wrapper) {
  box-shadow: 18px 0 32px rgba(3, 17, 31, 0.2);
}

:deep(.mobile-drawer .ant-drawer-content) {
  background: linear-gradient(180deg, #061a2e 0%, #03111f 100%);
}

:deep(.mobile-drawer .ant-menu-dark) {
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

:deep(.ant-menu-dark .ant-menu-item-selected) {
  background: linear-gradient(135deg, #1677ff 0%, #2f80ed 100%);
  box-shadow: 0 8px 20px rgba(22, 119, 255, 0.26);
}

@media (max-width: 991px) {
  .header {
    min-height: 76px;
    padding: 12px 16px;
  }

  .header-copy {
    align-items: center;
    width: 100%;
  }

  .title {
    font-size: 18px;
    line-height: 26px;
  }

  .subtitle {
    font-size: 12px;
    line-height: 18px;
  }

  .content {
    padding: 16px;
  }
}

@media (max-width: 768px) {
  .header-copy {
    gap: 8px;
  }
}
</style>
