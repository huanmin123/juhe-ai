<template>
  <a-layout class="app-shell" :class="{ 'app-shell-mobile': isMobile }">
    <AppSidebar
      v-model:open="sidebarOpen"
      v-model:collapsed="sidebarCollapsed"
      :app-icon="appBrand.appIcon"
      :app-name="appBrand.appName"
      :is-mobile="isMobile"
      :menu-items="menuItems"
      :open-keys="openMenuKeys"
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
        <div v-if="mustChangePassword" class="password-lock-state">
          <a-result status="warning" title="请先修改初始密码" sub-title="完成后将自动进入控制台。" />
        </div>
        <router-view v-else v-slot="{ Component, route: viewRoute }">
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
    <DisplayNameModal v-model:open="displayNameModalOpen" :form="displayNameForm" :saving="displayNameSaving" @ok="handleUpdateDisplayName" />
    <ChangePasswordModal v-model:open="passwordModalOpen" :forced="mustChangePassword" :form="passwordForm" :require-old-password="requireOldPasswordForPasswordChange" :saving="passwordSaving" @ok="handleChangePassword" />
  </a-layout>
</template>

<script setup lang="ts">
import {
  ApartmentOutlined,
  ApiOutlined,
  AppstoreOutlined,
  BellOutlined,
  GlobalOutlined,
  BarChartOutlined,
  BranchesOutlined,
  DatabaseOutlined,
  ExperimentOutlined,
  FilterOutlined,
  FundOutlined,
  HistoryOutlined,
  LinkOutlined,
  SearchOutlined,
  FileSearchOutlined,
  NodeIndexOutlined,
  ProfileOutlined,
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
import { authState, changePassword, logout, updateProfile } from '@/composables/useAuth'
import { appBrand, loadAppBrandSettings, syncDocumentTitle } from '@/composables/useAppBrand'
import {
  appMenuMode,
  getDefaultPathForMenuMode,
  saveMenuModePreference,
  setMenuModeFromRoute,
  syncMenuModeWithUser,
  type AppMenuMode
} from '@/composables/useMenuMode'
import { menuRoutes } from '@/router'
import { extractApiErrorMessage } from '@/shared/apiError'
import { isAdminRole, systemAccountRoleLabel } from '@/shared/systemAccountRoles'
import type { PublishedAnnouncementSummary } from '@/types/domain'
import AnnouncementModal from './AnnouncementModal.vue'
import AppHeader from './AppHeader.vue'
import AppSidebar from './AppSidebar.vue'
import ChangePasswordModal from './ChangePasswordModal.vue'
import DisplayNameModal from './DisplayNameModal.vue'

const router = useRouter()
const route = useRoute()
const isMobile = ref(false)
const sidebarOpen = ref(false)
const sidebarCollapsed = ref(false)
const displayNameModalOpen = ref(false)
const displayNameSaving = ref(false)
const displayNameForm = reactive({ displayName: '' })
const passwordModalOpen = ref(false)
const passwordSaving = ref(false)
const passwordForm = reactive({ oldPassword: '', newPassword: '', confirmPassword: '' })
const whitespacePattern = /\s/
const keepAliveMax = 48
const announcementModalOpen = ref(false)
const announcementsLoading = ref(false)
const announcements = ref<PublishedAnnouncementSummary[]>([])
let announcementsRefreshTimer: number | undefined
let announcementsRefreshRunning = false
let announcementsRequestId = 0

const selectedKeys = computed(() => [route.path])
const openMenuKeys = computed(() => {
  const currentRoute = visibleMenuRoutes.value.find((item) => item.path === route.path)
  return currentRoute?.meta?.menuGroup ? [`group:${currentRoute.meta.menuGroup}`] : []
})
const currentUser = authState.currentUser
const mustChangePassword = computed(() => Boolean(currentUser.value?.mustChangePassword))
const currentPageTitle = computed(() => mustChangePassword.value ? '修改登录密码' : route.meta.title || '轻量中转管理')
const currentPageDescription = computed(() => mustChangePassword.value ? '请先完成初始密码修改' : route.meta.description || 'OpenAI OAuth + API Key')
const requireOldPasswordForPasswordChange = computed(() => !mustChangePassword.value)
const canSwitchMenuMode = computed(() => isAdminRole(currentUser.value?.role))
const switchMenuModeLabel = computed(() => (appMenuMode.value === 'admin' ? '切换到用户模式' : '切换到管理模式'))
const userDisplayName = computed(() => currentUser.value?.displayName || '用户')
const userRoleLabel = computed(() => {
  const user = currentUser.value
  if (user && canSwitchMenuMode.value) {
    const label = systemAccountRoleLabel(user.role)
    return appMenuMode.value === 'admin' ? `${label} · 管理模式` : `${label} · 用户模式`
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
const hasNewAnnouncements = computed(() => !mustChangePassword.value && announcements.value.some((announcement) => !announcement.readAt))

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
  '/my-models': AppstoreOutlined,
  '/my-accounts': UserSwitchOutlined,
  '/accounts': UserSwitchOutlined,
  '/my-groups': ApartmentOutlined,
  '/groups': ApartmentOutlined,
  '/system-teams': TeamOutlined,
  '/authorization-teams': TeamOutlined,
  '/my-teams': TeamOutlined,
  '/my-authorizations': SafetyCertificateOutlined,
  '/my-authorization-team-usage': FundOutlined,
  '/my-authorization-user-usage': HistoryOutlined,
  '/authorizations': SafetyCertificateOutlined,
  '/authorization-team-usage': FundOutlined,
  '/authorization-user-usage': HistoryOutlined,
  '/my-api-keys': ApiKeyMenuIcon,
  '/api-keys': ApiKeyMenuIcon,
  '/my-route-strategies': BranchesOutlined,
  '/route-strategies': BranchesOutlined,
  '/my-model-checks': ExperimentOutlined,
  '/model-checks': ExperimentOutlined,
  '/proxies': NodeIndexOutlined,
  '/my-stats': BarChartOutlined,
  '/stats': BarChartOutlined,
  '/my-ai-performance': BarChartOutlined,
  '/ai-performance': BarChartOutlined,
  '/my-usage-stats': FundOutlined,
  '/usage-stats': FundOutlined,
  '/my-usage-records': HistoryOutlined,
  '/usage-records': HistoryOutlined,
  '/my-operation-logs': ProfileOutlined,
  '/operation-logs': ProfileOutlined,
  '/public-api-logs': ApiOutlined,
  '/audit-logs': FileSearchOutlined,
  '/runtime-logs': SearchOutlined,
  '/table-monitor': DatabaseOutlined,
  '/ip-stats': GlobalOutlined,
  '/response-inspection-policies': FilterOutlined,
  '/external-integration-sources': LinkOutlined,
  '/announcements': BellOutlined,
  '/settings': SettingOutlined,
  '/system-accounts': TeamOutlined
}

const menuGroupIconMap = {
  'ai-management': DatabaseOutlined,
  authorization: SafetyCertificateOutlined,
  'log-management': ProfileOutlined,
  'my-authorization': SafetyCertificateOutlined,
  'system-operations': SettingOutlined
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
  const items: ItemType[] = []
  const groupItems = new Map<string, ItemType & { children: ItemType[] }>()
  for (const item of visibleMenuRoutes.value) {
    const groupKey = item.meta?.menuGroup
    if (!groupKey) {
      items.push(routeToMenuItem(item))
      continue
    }
    let group = groupItems.get(groupKey)
    if (!group) {
      const groupIcon = menuGroupIconMap[groupKey as keyof typeof menuGroupIconMap]
      group = {
        key: `group:${groupKey}`,
        label: item.meta?.menuGroupTitle ?? item.meta?.title ?? '',
        children: [],
        ...(groupIcon ? { icon: () => h(groupIcon) } : {})
      }
      groupItems.set(groupKey, group)
      items.push(group)
    }
    group.children.push(routeToMenuItem(item))
  }
  return items
})

function handleMenuClick(event: { key: string | number }) {
  const key = String(event.key)
  if (key.startsWith('group:')) return
  if (mustChangePassword.value) {
    openPasswordModal(false)
    sidebarOpen.value = false
    return
  }
  router.push(key)
  sidebarOpen.value = false
}

async function handleUserMenuClick(event: Parameters<NonNullable<MenuProps['onClick']>>[0]) {
  if (mustChangePassword.value && event.key !== 'password' && event.key !== 'logout') {
    message.warning('请先修改初始密码')
    openPasswordModal(false)
    return
  }
  if (event.key === 'switch-mode') {
    await switchMenuMode()
    return
  }
  if (event.key === 'display-name') {
    openDisplayNameModal()
    return
  }
  if (event.key === 'password') {
    openPasswordModal()
    return
  }
  if (event.key === 'logout') {
    await logout()
    await router.replace('/login')
  }
}

async function openAnnouncements() {
  if (mustChangePassword.value) {
    message.warning('请先修改初始密码')
    openPasswordModal(false)
    return
  }
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

async function loadAnnouncements(): Promise<PublishedAnnouncementSummary[]> {
  const requestUserKey = currentAnnouncementUserKey()
  if (!requestUserKey || mustChangePassword.value) {
    announcementsRequestId += 1
    announcements.value = []
    announcementsLoading.value = false
    return []
  }
  const requestId = ++announcementsRequestId
  announcementsLoading.value = true
  try {
    const nextAnnouncements = await api.announcements.publicList({ limit: 30 })
    if (requestId !== announcementsRequestId || requestUserKey !== currentAnnouncementUserKey()) {
      return announcements.value
    }
    announcements.value = nextAnnouncements
    return nextAnnouncements
  } catch (error) {
    console.error(error)
    message.error('加载公告失败')
    return announcements.value
  } finally {
    if (requestId === announcementsRequestId) {
      announcementsLoading.value = false
    }
  }
}

function currentAnnouncementUserKey(): string {
  const user = currentUser.value
  return user?.id || user?.username || ''
}

async function markAnnouncementsViewed(visibleAnnouncements = announcements.value) {
  const requestUserKey = currentAnnouncementUserKey()
  if (!requestUserKey) return
  const unreadIds = visibleAnnouncements.filter((announcement) => !announcement.readAt).map((announcement) => announcement.id)
  if (!unreadIds.length) return
  try {
    const result = await api.announcements.markRead({ announcementIds: unreadIds })
    if (requestUserKey !== currentAnnouncementUserKey()) return
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
  if (mustChangePassword.value) {
    message.warning('请先修改初始密码')
    openPasswordModal(false)
    return
  }
  const nextMode: AppMenuMode = appMenuMode.value === 'admin' ? 'self' : 'admin'
  const savedMode = saveMenuModePreference(currentUser.value, nextMode)
  const targetPath = getDefaultPathForMenuMode(savedMode)
  message.success(savedMode === 'admin' ? '已切换到管理模式' : '已切换到用户模式')
  if (route.path !== targetPath) {
    await router.push(targetPath)
  }
}

async function handleUpdateDisplayName() {
  if (displayNameSaving.value) return
  const displayName = displayNameForm.displayName.trim()
  if (!displayName) {
    message.warning('请输入显示名称')
    return
  }
  if (hasWhitespace(displayNameForm.displayName)) {
    message.warning('显示名称不能包含空格')
    return
  }
  if (displayName === currentUser.value?.displayName) {
    displayNameModalOpen.value = false
    return
  }
  displayNameSaving.value = true
  try {
    await updateProfile({ displayName })
    message.success('显示名称已修改')
    displayNameModalOpen.value = false
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '修改显示名称失败'))
  } finally {
    displayNameSaving.value = false
  }
}

async function handleChangePassword() {
  if (requireOldPasswordForPasswordChange.value && !passwordForm.oldPassword) {
    message.warning('请输入当前密码')
    return
  }
  if ((passwordForm.oldPassword && hasWhitespace(passwordForm.oldPassword)) || hasWhitespace(passwordForm.newPassword) || hasWhitespace(passwordForm.confirmPassword)) {
    message.warning('登录密码不能包含空格')
    return
  }
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
    await changePassword({
      oldPassword: requireOldPasswordForPasswordChange.value ? passwordForm.oldPassword : undefined,
      newPassword: passwordForm.newPassword
    })
    message.success('密码已修改')
    passwordModalOpen.value = false
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '修改密码失败'))
  } finally {
    passwordSaving.value = false
  }
}

function openDisplayNameModal() {
  displayNameForm.displayName = currentUser.value?.displayName ?? ''
  displayNameModalOpen.value = true
}

function resetPasswordForm() {
  passwordForm.oldPassword = ''
  passwordForm.newPassword = ''
  passwordForm.confirmPassword = ''
}

function openPasswordModal(resetForm = true) {
  if (resetForm) {
    resetPasswordForm()
  }
  passwordModalOpen.value = true
}

function hasWhitespace(value: string): boolean {
  return whitespacePattern.test(value)
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

async function refreshAnnouncementsSafely() {
  if (mustChangePassword.value) return
  if (announcementsRefreshRunning) return
  announcementsRefreshRunning = true
  try {
    if (announcementModalOpen.value) {
      await refreshAnnouncementsInModal()
    } else {
      await loadAnnouncements()
    }
  } catch (error) {
    console.error(error)
  } finally {
    announcementsRefreshRunning = false
  }
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
    void refreshAnnouncementsSafely()
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
    if (user?.mustChangePassword) {
      announcementsRequestId += 1
      announcements.value = []
      announcementsLoading.value = false
      openPasswordModal()
    } else if (user) {
      void loadAnnouncements()
    } else {
      announcementsRequestId += 1
      announcements.value = []
      announcementsLoading.value = false
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
  mustChangePassword,
  (required) => {
    if (required) {
      openPasswordModal(false)
    } else if (passwordModalOpen.value && !passwordSaving.value) {
      passwordModalOpen.value = false
    }
  },
  { immediate: true }
)

watch(
  passwordModalOpen,
  (open) => {
    if (!open && mustChangePassword.value) {
      openPasswordModal(false)
    }
  }
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

watch(
  () => [route.meta.title, appBrand.appName],
  () => {
    syncDocumentTitle(typeof route.meta.title === 'string' ? route.meta.title : undefined)
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

.password-lock-state {
  min-height: calc(100vh - 154px);
  display: flex;
  align-items: center;
  justify-content: center;
}

.password-lock-state :deep(.ant-result-title) {
  color: #0f172a;
  font-weight: 800;
}

@media (max-width: 991px) {
  .content {
    padding: 16px;
  }
}
</style>
