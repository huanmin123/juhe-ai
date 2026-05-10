<template>
  <a-layout class="app-shell" :class="{ 'app-shell-mobile': isMobile }">
    <AppSidebar
      v-model:open="sidebarOpen"
      v-model:collapsed="sidebarCollapsed"
      :app-icon="appBrand.appIcon"
      :app-name="appBrand.appName"
      :is-mobile="isMobile"
      :menu-items="menuItems"
      :selected-keys="selectedKeys"
      @menu-click="handleMenuClick"
    />
    <a-layout class="main-shell">
      <AppHeader
        :announcement-bell-shaking="hasNewAnnouncements"
        :can-switch-menu-mode="canSwitchMenuMode"
        :description="currentPageDescription"
        :has-new-announcements="hasNewAnnouncements"
        :is-mobile="isMobile"
        :switch-menu-mode-label="switchMenuModeLabel"
        :title="currentPageTitle"
        :user-avatar-text="userAvatarText"
        :user-display-name="userDisplayName"
        :user-role-label="userRoleLabel"
        @open-announcements="openAnnouncements"
        @open-sidebar="sidebarOpen = true"
        @user-menu-click="handleUserMenuClick"
      />
      <a-layout-content class="content">
        <router-view v-slot="{ Component, route: viewRoute }">
          <KeepAlive v-if="viewRoute.meta.keepAlive !== false" :max="keepAliveMax">
            <component :is="Component" :key="viewRoute.path" />
          </KeepAlive>
          <component :is="Component" v-else :key="viewRoute.path" />
        </router-view>
      </a-layout-content>
    </a-layout>
    <AnnouncementModal
      v-model:open="announcementModalOpen"
      :announcements="announcements"
      :loading="announcementsLoading"
    />
    <ChangePasswordModal v-model:open="passwordModalOpen" :form="passwordForm" :saving="passwordSaving" @ok="handleChangePassword" />
  </a-layout>
</template>

<script setup lang="ts">
import {
  ApartmentOutlined,
  BellOutlined,
  GlobalOutlined,
  BarChartOutlined,
  FundOutlined,
  HistoryOutlined,
  SearchOutlined,
  FileSearchOutlined,
  NodeIndexOutlined,
  SafetyCertificateOutlined,
  SettingOutlined,
  TeamOutlined,
  UserSwitchOutlined
} from '@ant-design/icons-vue'
import type { MenuProps } from 'ant-design-vue'
import { message } from '@/lib/antd'
import type { ItemType } from 'ant-design-vue'
import { computed, h, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { api } from '@/api/client'
import { authState, changePassword, logout } from '@/composables/useAuth'
import { appBrand, loadAppBrandSettings } from '@/composables/useAppBrand'
import {
  appMenuMode,
  getDefaultPathForMenuMode,
  saveMenuModePreference,
  setMenuModeFromRoute,
  syncMenuModeWithUser,
  type AppMenuMode
} from '@/composables/useMenuMode'
import { menuRoutes } from '@/router'
import type { AnnouncementSummary } from '@/types/domain'
import AnnouncementModal from './AnnouncementModal.vue'
import AppHeader from './AppHeader.vue'
import AppSidebar from './AppSidebar.vue'
import ChangePasswordModal from './ChangePasswordModal.vue'

const router = useRouter()
const route = useRoute()
const isMobile = ref(false)
const sidebarOpen = ref(false)
const sidebarCollapsed = ref(false)
const passwordModalOpen = ref(false)
const passwordSaving = ref(false)
const passwordForm = reactive({ newPassword: '', confirmPassword: '' })
const keepAliveMax = 18
const announcementModalOpen = ref(false)
const announcementsLoading = ref(false)
const announcements = ref<AnnouncementSummary[]>([])
let announcementsRefreshTimer: number | undefined

const selectedKeys = computed(() => [route.path])
const currentPageTitle = computed(() => route.meta.title || '轻量中转管理')
const currentPageDescription = computed(() => route.meta.description || 'OpenAI OAuth + API Key')
const currentUser = authState.currentUser
const canSwitchMenuMode = computed(() => currentUser.value?.role === 'admin')
const switchMenuModeLabel = computed(() => (appMenuMode.value === 'admin' ? '切换到用户模式' : '切换到管理模式'))
const userDisplayName = computed(() => currentUser.value?.displayName || currentUser.value?.username || '用户')
const userRoleLabel = computed(() => {
  if (canSwitchMenuMode.value) {
    return appMenuMode.value === 'admin' ? '管理员 · 管理模式' : '管理员 · 用户模式'
  }
  return '普通用户'
})
const userAvatarText = computed(() => {
  const name = userDisplayName.value.trim()
  if (!name) {
    return '用'
  }
  return /^[\x00-\x7F]+$/.test(name) ? name.slice(0, 2).toUpperCase() : name.slice(0, 1)
})
const hasNewAnnouncements = computed(() => announcements.value.some((announcement) => !announcement.readAt))

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
  '/my-accounts': UserSwitchOutlined,
  '/accounts': UserSwitchOutlined,
  '/my-groups': ApartmentOutlined,
  '/groups': ApartmentOutlined,
  '/system-teams': TeamOutlined,
  '/my-teams': TeamOutlined,
  '/my-authorizations': SafetyCertificateOutlined,
  '/authorizations': SafetyCertificateOutlined,
  '/my-api-keys': ApiKeyMenuIcon,
  '/api-keys': ApiKeyMenuIcon,
  '/proxies': NodeIndexOutlined,
  '/stats': BarChartOutlined,
  '/my-usage-stats': FundOutlined,
  '/usage-stats': FundOutlined,
  '/my-usage-records': HistoryOutlined,
  '/usage-records': HistoryOutlined,
  '/audit-logs': FileSearchOutlined,
  '/runtime-logs': SearchOutlined,
  '/announcements': BellOutlined,
  '/settings': SettingOutlined,
  '/system-accounts': TeamOutlined
}

function canAccessRoute(item: typeof menuRoutes[number]): boolean {
  return !item.meta?.roles?.length || Boolean(currentUser.value && item.meta.roles.includes(currentUser.value.role))
}

function routeToMenuItem(item: typeof menuRoutes[number]): ItemType {
  const iconComponent = menuIconMap[item.path as keyof typeof menuIconMap]
  return {
    key: item.path,
    label: item.meta?.title ?? '',
    ...(iconComponent ? { icon: () => h(iconComponent) } : {})
  }
}

const visibleMenuRoutes = computed(() =>
  menuRoutes.filter((item) => canAccessRoute(item) && (item.meta?.viewScope ?? 'admin') === appMenuMode.value)
)

const menuItems = computed<ItemType[]>(() => {
  return visibleMenuRoutes.value.map(routeToMenuItem)
})

function handleMenuClick(event: { key: string | number }) {
  router.push(String(event.key))
  sidebarOpen.value = false
}

async function handleUserMenuClick(event: Parameters<NonNullable<MenuProps['onClick']>>[0]) {
  if (event.key === 'switch-mode') {
    await switchMenuMode()
    return
  }
  if (event.key === 'password') {
    passwordForm.newPassword = ''
    passwordForm.confirmPassword = ''
    passwordModalOpen.value = true
    return
  }
  if (event.key === 'logout') {
    await logout()
    await router.replace('/login')
  }
}

async function openAnnouncements() {
  announcementModalOpen.value = true
  const visibleAnnouncements = await loadAnnouncements()
  await markAnnouncementsViewed(visibleAnnouncements)
}

async function refreshAnnouncementsInModal() {
  const visibleAnnouncements = await loadAnnouncements()
  if (announcementModalOpen.value) {
    await markAnnouncementsViewed(visibleAnnouncements)
  }
}

async function loadAnnouncements(): Promise<AnnouncementSummary[]> {
  if (!currentUser.value) return announcements.value
  announcementsLoading.value = true
  try {
    const nextAnnouncements = await api.announcements.publicList({ limit: 30 })
    announcements.value = nextAnnouncements
    return nextAnnouncements
  } catch (error) {
    console.error(error)
    message.error('加载公告失败')
    return announcements.value
  } finally {
    announcementsLoading.value = false
  }
}

async function markAnnouncementsViewed(visibleAnnouncements = announcements.value) {
  const unreadIds = visibleAnnouncements.filter((announcement) => !announcement.readAt).map((announcement) => announcement.id)
  if (!unreadIds.length) return
  try {
    const result = await api.announcements.markRead({ announcementIds: unreadIds })
    const readIds = new Set(unreadIds)
    announcements.value = announcements.value.map((announcement) => readIds.has(announcement.id)
      ? { ...announcement, readAt: result.readAt }
      : announcement)
  } catch (error) {
    console.error(error)
    message.error('记录公告已读失败')
  }
}

async function switchMenuMode() {
  const nextMode: AppMenuMode = appMenuMode.value === 'admin' ? 'self' : 'admin'
  const savedMode = saveMenuModePreference(currentUser.value, nextMode)
  const targetPath = getDefaultPathForMenuMode(savedMode)
  message.success(savedMode === 'admin' ? '已切换到管理模式' : '已切换到用户模式')
  if (route.path !== targetPath) {
    await router.push(targetPath)
  }
}

async function handleChangePassword() {
  if (passwordForm.newPassword.length < 4) {
    message.warning('新密码至少 4 位')
    return
  }
  if (passwordForm.newPassword !== passwordForm.confirmPassword) {
    message.warning('两次输入的密码不一致')
    return
  }
  passwordSaving.value = true
  try {
    await changePassword({ newPassword: passwordForm.newPassword })
    message.success('密码已修改')
    passwordModalOpen.value = false
  } catch (error) {
    console.error(error)
    message.error('修改密码失败')
  } finally {
    passwordSaving.value = false
  }
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
  loadAnnouncements().catch((error) => {
    console.error(error)
  })
  announcementsRefreshTimer = window.setInterval(() => {
    const refreshTask = announcementModalOpen.value ? refreshAnnouncementsInModal() : loadAnnouncements()
    refreshTask.catch((error) => {
      console.error(error)
    })
  }, 60000)
  window.addEventListener('resize', handleResize, { passive: true })
})

onBeforeUnmount(() => {
  if (announcementsRefreshTimer) {
    window.clearInterval(announcementsRefreshTimer)
  }
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

watch(
  currentUser,
  (user) => {
    if (user) {
      void loadAnnouncements()
    } else {
      announcements.value = []
    }
    if (route.meta.viewScope === 'admin' || route.meta.viewScope === 'self') {
      setMenuModeFromRoute(user, route.meta.viewScope)
      return
    }
    syncMenuModeWithUser(user)
  },
  { immediate: true }
)

watch(
  () => route.meta.viewScope,
  (viewScope) => {
    if (viewScope === 'admin' || viewScope === 'self') {
      setMenuModeFromRoute(currentUser.value, viewScope)
    }
  },
  { immediate: true }
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

.content {
  padding: 26px 24px 36px;
  background:
    radial-gradient(circle at 20% 0%, rgba(22, 119, 255, 0.06), transparent 28%),
    #f5f7fb;
}

@media (max-width: 991px) {
  .content {
    padding: 16px;
  }
}
</style>
