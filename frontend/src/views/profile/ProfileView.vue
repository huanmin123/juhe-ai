<template>
  <section class="profile-page">
    <div v-if="loading" class="profile-state-card page-card" aria-busy="true">
      <a-spin size="large" tip="正在加载个人信息" />
    </div>

    <a-result
      v-else-if="loadError"
      class="profile-state-card page-card"
      status="error"
      title="个人信息加载失败"
      :sub-title="loadError"
    >
      <template #extra>
        <a-button type="primary" @click="loadProfile">重新加载</a-button>
      </template>
    </a-result>

    <template v-else-if="profile">
      <a-card class="profile-hero page-card" :bordered="false">
        <div class="profile-hero-content">
          <div class="profile-avatar" aria-hidden="true">{{ avatarText }}</div>
          <div class="profile-identity">
            <div class="profile-title-row">
              <h2>{{ profile.displayName }}</h2>
              <div class="profile-tags">
                <a-tag :color="systemAccountRoleColor(profile.role)">{{ systemAccountRoleLabel(profile.role) }}</a-tag>
                <a-tag :color="accountStatusColor">{{ accountStatusLabel }}</a-tag>
              </div>
            </div>
            <div class="profile-username">登录账号：{{ profile.username }}</div>
            <div class="profile-summary">查看账号资料、权限能力并维护用户名称与登录密码。</div>
          </div>
        </div>
      </a-card>

      <div v-if="mustChangePassword" class="profile-required-notice" role="alert">
        <span class="profile-required-icon"><LockOutlined /></span>
        <div>
          <strong>请先修改初始密码</strong>
          <p>完成密码修改后才能继续使用控制台；当前会话会保留，其他登录会话将失效。</p>
        </div>
      </div>

      <div class="profile-grid">
        <a-card class="page-card profile-section-card profile-basic-card" title="基本资料">
          <div class="profile-basic-editor">
            <div class="profile-basic-editor-head">
              <span class="profile-basic-editor-icon"><EditOutlined /></span>
              <div>
                <label for="profile-display-name">用户名称</label>
                <span>用于头像、页面头部和系统内用户展示</span>
              </div>
            </div>

            <a-form layout="vertical">
              <div class="profile-basic-editor-control">
                <a-input
                  id="profile-display-name"
                  v-model:value="displayNameForm.displayName"
                  :disabled="displayNameSaving || mustChangePassword"
                  :maxlength="80"
                  autocomplete="name"
                  placeholder="请输入用户名称"
                  size="large"
                  @keyup.enter="saveDisplayName"
                />
                <a-button
                  type="primary"
                  size="large"
                  :loading="displayNameSaving"
                  :disabled="displayNameSaveDisabled"
                  @click="saveDisplayName"
                >
                  <SaveOutlined />
                  保存修改
                </a-button>
              </div>
              <p class="profile-basic-editor-help">不能包含空格，最多 80 个字符。</p>
            </a-form>
          </div>

          <div class="profile-account-section">
            <div class="profile-account-title">
              <strong>账户信息</strong>
              <span>由系统维护，不可在此修改</span>
            </div>

            <div class="profile-account-list">
              <div class="profile-account-row">
                <span class="profile-account-icon"><UserOutlined /></span>
                <div class="profile-account-copy">
                  <span>登录账号</span>
                  <a-typography-text class="profile-copy-value" :copyable="{ text: profile.username }">
                    {{ profile.username }}
                  </a-typography-text>
                </div>
              </div>

              <div class="profile-account-row">
                <span class="profile-account-icon"><IdcardOutlined /></span>
                <div class="profile-account-copy">
                  <span>账户 ID</span>
                  <a-typography-text class="profile-copy-value mono-cell" :copyable="{ text: profile.id }">
                    {{ profile.id }}
                  </a-typography-text>
                </div>
              </div>

              <div class="profile-account-row">
                <span class="profile-account-icon"><FileTextOutlined /></span>
                <div class="profile-account-copy">
                  <span>账户说明</span>
                  <strong :class="profile.description ? '' : 'muted-cell'">
                    {{ profile.description || '暂无说明' }}
                  </strong>
                </div>
              </div>
            </div>
          </div>
        </a-card>

        <a-card class="page-card profile-section-card" title="权限与能力">
          <div class="profile-capability-list">
            <div class="profile-capability-item">
              <span class="profile-capability-icon role"><SafetyCertificateOutlined /></span>
              <div class="profile-capability-copy">
                <span>系统角色</span>
                <strong>{{ systemAccountRoleLabel(profile.role) }}</strong>
                <small>{{ accessModeDescription }}</small>
              </div>
              <a-tag :color="systemAccountRoleColor(profile.role)">已生效</a-tag>
            </div>

            <div class="profile-capability-item">
              <span class="profile-capability-icon image"><PictureOutlined /></span>
              <div class="profile-capability-copy">
                <span>图像生成</span>
                <strong>{{ profile.imageGenerationEnabled ? '已开通' : '未开通' }}</strong>
                <small>{{ imageGenerationDescription }}</small>
              </div>
              <a-tag :color="profile.imageGenerationEnabled ? 'green' : 'default'">
                {{ profile.imageGenerationEnabled ? '可使用' : '未授权' }}
              </a-tag>
            </div>

            <div class="profile-capability-item">
              <span class="profile-capability-icon status"><CheckCircleOutlined /></span>
              <div class="profile-capability-copy">
                <span>账户状态</span>
                <strong>{{ accountStatusLabel }}</strong>
                <small>角色、状态与能力由系统账户管理统一配置。</small>
              </div>
              <a-tag :color="accountStatusColor">{{ profile.status === 'active' ? '正常' : '受限' }}</a-tag>
            </div>
          </div>
        </a-card>
      </div>

      <div ref="securitySection">
        <a-card class="page-card profile-security-card" title="账号安全">
          <template #extra>
            <span class="profile-security-extra">修改后其他登录会话会失效</span>
          </template>
          <a-form layout="vertical">
            <div class="profile-password-grid" :class="{ forced: mustChangePassword }">
              <a-form-item v-if="!mustChangePassword" label="当前密码" required>
                <a-input-password
                  v-model:value="passwordForm.oldPassword"
                  :disabled="passwordSaving"
                  autocomplete="current-password"
                  placeholder="请输入当前密码"
                  @keyup.enter="savePassword"
                />
              </a-form-item>
              <a-form-item label="新密码" required extra="至少 4 位，不能包含空格。">
                <a-input-password
                  v-model:value="passwordForm.newPassword"
                  :disabled="passwordSaving"
                  autocomplete="new-password"
                  placeholder="请输入新密码"
                  @keyup.enter="savePassword"
                />
              </a-form-item>
              <a-form-item label="确认新密码" required>
                <a-input-password
                  v-model:value="passwordForm.confirmPassword"
                  :disabled="passwordSaving"
                  autocomplete="new-password"
                  placeholder="请再次输入新密码"
                  @keyup.enter="savePassword"
                />
              </a-form-item>
            </div>
            <a-button type="primary" :loading="passwordSaving" @click="savePassword">
              <LockOutlined />
              {{ mustChangePassword ? '保存并进入控制台' : '修改登录密码' }}
            </a-button>
          </a-form>
        </a-card>
      </div>

      <a-card class="page-card profile-time-card" :bordered="false">
        <div class="profile-time-grid">
          <div>
            <span>最近登录</span>
            <a-tooltip :title="profile.lastLoginAt || '暂无记录'">
              <strong>{{ profile.lastLoginAt ? formatDateTime(profile.lastLoginAt) : '暂无记录' }}</strong>
            </a-tooltip>
          </div>
          <div>
            <span>账户创建</span>
            <a-tooltip :title="profile.createdAt">
              <strong>{{ formatDateTime(profile.createdAt) }}</strong>
            </a-tooltip>
          </div>
          <div>
            <span>资料更新</span>
            <a-tooltip :title="profile.updatedAt">
              <strong>{{ formatDateTime(profile.updatedAt) }}</strong>
            </a-tooltip>
          </div>
        </div>
      </a-card>
    </template>
  </section>
</template>

<script setup lang="ts">
import {
  CheckCircleOutlined,
  EditOutlined,
  FileTextOutlined,
  IdcardOutlined,
  LockOutlined,
  PictureOutlined,
  SafetyCertificateOutlined,
  SaveOutlined,
  UserOutlined
} from '@ant-design/icons-vue'
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { api } from '@/api/client'
import { authState, changePassword, updateProfile } from '@/composables/useAuth'
import { getPreferredEntryPath } from '@/composables/useMenuMode'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime } from '@/shared/formatters'
import { isAdminRole, systemAccountRoleColor, systemAccountRoleLabel } from '@/shared/systemAccountRoles'
import type { CurrentUserProfile } from '@/types/domain'
import { normalizeProfileRedirectPath } from './profileNavigation'

const route = useRoute()
const router = useRouter()
const profile = ref<CurrentUserProfile>()
const loading = ref(true)
const loadError = ref('')
const displayNameSaving = ref(false)
const passwordSaving = ref(false)
const securitySection = ref<HTMLElement>()
const displayNameForm = reactive({ displayName: '' })
const passwordForm = reactive({ oldPassword: '', newPassword: '', confirmPassword: '' })
let profileLoadGeneration = 0

const currentUser = authState.currentUser
const mustChangePassword = computed(() => Boolean(currentUser.value?.mustChangePassword))
const avatarText = computed(() => {
  const name = profile.value?.displayName.trim() || '用户'
  return /^[\x00-\x7F]+$/.test(name) ? name.slice(0, 2).toUpperCase() : name.slice(0, 1)
})
const displayNameSaveDisabled = computed(() => {
  const value = displayNameForm.displayName.trim()
  return displayNameSaving.value
    || mustChangePassword.value
    || !value
    || value === profile.value?.displayName
})
const accessModeDescription = computed(() => isAdminRole(profile.value?.role)
  ? '可使用用户模式和管理模式。'
  : '可使用用户模式。')
const imageGenerationDescription = computed(() => profile.value?.imageGenerationEnabled
  ? '当前账号可在支持的模型与路由中使用图像生成。'
  : '需要管理员在系统账户管理中开启。')
const accountStatusLabel = computed(() => profile.value?.status === 'active' ? '启用中' : '已停用')
const accountStatusColor = computed(() => profile.value?.status === 'active' ? 'green' : 'red')
watch(
  () => currentUser.value?.id,
  (systemAccountId) => {
    if (systemAccountId) void loadProfile()
  },
  { immediate: true }
)

async function loadProfile(options: { silent?: boolean } = {}): Promise<void> {
  const requestUserId = currentUser.value?.id
  if (!requestUserId) return
  const generation = ++profileLoadGeneration
  if (!options.silent) {
    loading.value = true
    loadError.value = ''
  }
  try {
    const nextProfile = await api.auth.profile()
    if (generation !== profileLoadGeneration || currentUser.value?.id !== requestUserId) return
    profile.value = nextProfile
    displayNameForm.displayName = nextProfile.displayName
    if (!options.silent) await focusRequestedSection()
  } catch (error) {
    if (generation !== profileLoadGeneration || currentUser.value?.id !== requestUserId) return
    console.error(error)
    if (!options.silent) loadError.value = extractApiErrorMessage(error, '请稍后重试')
    else message.error(extractApiErrorMessage(error, '刷新个人信息失败'))
  } finally {
    if (generation === profileLoadGeneration && !options.silent) loading.value = false
  }
}

async function saveDisplayName(): Promise<void> {
  if (displayNameSaving.value || mustChangePassword.value) return
  const displayName = displayNameForm.displayName.trim()
  if (!displayName) {
    message.warning('请输入用户名称')
    return
  }
  if (/\s/.test(displayNameForm.displayName)) {
    message.warning('用户名称不能包含空格')
    return
  }
  if (displayName === profile.value?.displayName) return
  displayNameSaving.value = true
  try {
    const user = await updateProfile({ displayName })
    if (profile.value?.id === user.id) {
      profile.value = { ...profile.value, displayName: user.displayName }
    }
    displayNameForm.displayName = user.displayName
    message.success('用户名称已修改')
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '修改用户名称失败'))
  } finally {
    displayNameSaving.value = false
  }
}

async function savePassword(): Promise<void> {
  if (passwordSaving.value) return
  if (!mustChangePassword.value && !passwordForm.oldPassword) {
    message.warning('请输入当前密码')
    return
  }
  if (/\s/.test(passwordForm.oldPassword) || /\s/.test(passwordForm.newPassword) || /\s/.test(passwordForm.confirmPassword)) {
    message.warning('登录密码不能包含空格')
    return
  }
  if (passwordForm.newPassword.length < 4) {
    message.warning('新密码至少 4 位')
    return
  }
  if (passwordForm.newPassword !== passwordForm.confirmPassword) {
    message.warning('两次输入的新密码不一致')
    return
  }
  const wasRequired = mustChangePassword.value
  passwordSaving.value = true
  try {
    const user = await changePassword({
      oldPassword: wasRequired ? undefined : passwordForm.oldPassword,
      newPassword: passwordForm.newPassword
    })
    resetPasswordForm()
    message.success('登录密码已修改')
    if (wasRequired) {
      const redirect = normalizeProfileRedirectPath(route.query.redirect)
      await router.replace(redirect || getPreferredEntryPath(user))
      return
    }
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '修改密码失败'))
  } finally {
    passwordSaving.value = false
  }
}

function resetPasswordForm(): void {
  passwordForm.oldPassword = ''
  passwordForm.newPassword = ''
  passwordForm.confirmPassword = ''
}

async function focusRequestedSection(): Promise<void> {
  if (route.query.section !== 'security' && !mustChangePassword.value) return
  await nextTick()
  securitySection.value?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}
</script>

<style scoped>
.profile-page {
  width: min(100%, 1080px);
  display: grid;
  gap: 18px;
  margin: 0 auto;
}

.profile-state-card {
  min-height: 420px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: #fff;
}

.profile-hero :deep(.ant-card-body) {
  padding: 28px 30px;
}

.profile-hero-content {
  display: flex;
  align-items: center;
  gap: 20px;
}

.profile-avatar {
  width: 72px;
  height: 72px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 72px;
  color: #fff;
  font-size: 24px;
  font-weight: 800;
  line-height: 1;
  background: linear-gradient(145deg, #0f766e, #14b8a6);
  border: 4px solid #ccfbf1;
  border-radius: 50%;
  box-shadow: 0 10px 24px rgba(13, 148, 136, 0.2);
}

.profile-identity {
  min-width: 0;
  flex: 1;
}

.profile-title-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 12px;
}

.profile-title-row h2 {
  margin: 0;
  color: #0f172a;
  font-size: 24px;
  line-height: 32px;
}

.profile-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.profile-tags :deep(.ant-tag),
.profile-capability-item :deep(.ant-tag) {
  margin-inline-end: 0;
}

.profile-username {
  margin-top: 5px;
  color: #475569;
  font-size: 14px;
}

.profile-summary {
  margin-top: 8px;
  color: #64748b;
  font-size: 13px;
  line-height: 20px;
}

.profile-required-notice {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 16px 18px;
  color: #92400e;
  background: #fffbeb;
  border: 1px solid #fde68a;
  border-radius: 14px;
  box-shadow: 0 8px 22px rgba(146, 64, 14, 0.06);
}

.profile-required-icon {
  width: 34px;
  height: 34px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 34px;
  color: #b45309;
  background: #fef3c7;
  border-radius: 10px;
}

.profile-required-notice strong {
  display: block;
  font-size: 15px;
}

.profile-required-notice p {
  margin: 4px 0 0;
  color: #a16207;
  font-size: 13px;
  line-height: 20px;
}

.profile-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.08fr) minmax(0, 0.92fr);
  gap: 18px;
  align-items: stretch;
}

.profile-section-card {
  height: 100%;
}

.profile-basic-card :deep(.ant-card-body) {
  padding: 22px 24px 24px;
}

.profile-basic-editor {
  padding-bottom: 20px;
  border-bottom: 1px solid #eef2f7;
}

.profile-basic-editor-head {
  display: flex;
  align-items: center;
  gap: 11px;
  margin-bottom: 14px;
}

.profile-basic-editor-head > div {
  min-width: 0;
  display: grid;
  gap: 2px;
}

.profile-basic-editor-head label {
  width: max-content;
  color: #0f172a;
  font-size: 14px;
  font-weight: 650;
  cursor: pointer;
}

.profile-basic-editor-head span:last-child {
  color: #64748b;
  font-size: 12px;
  line-height: 18px;
}

.profile-basic-editor-icon {
  width: 36px;
  height: 36px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 36px;
  color: #0f766e;
  font-size: 16px;
  background: #ccfbf1;
  border-radius: 10px;
}

.profile-basic-editor-control {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 10px;
}

.profile-basic-editor-control :deep(.ant-input),
.profile-basic-editor-control :deep(.ant-btn) {
  border-radius: 10px;
}

.profile-basic-editor-help {
  margin: 7px 0 0;
  color: #94a3b8;
  font-size: 12px;
  line-height: 18px;
}

.profile-account-section {
  padding-top: 18px;
}

.profile-account-title {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 10px;
}

.profile-account-title strong {
  color: #334155;
  font-size: 13px;
}

.profile-account-title span {
  color: #94a3b8;
  font-size: 12px;
}

.profile-account-list {
  display: grid;
  gap: 8px;
}

.profile-account-row {
  min-width: 0;
  display: grid;
  grid-template-columns: 34px minmax(0, 1fr);
  align-items: center;
  gap: 10px;
  padding: 9px 11px;
  background: #f8fafc;
  border: 1px solid #eef2f7;
  border-radius: 10px;
}

.profile-account-icon {
  width: 34px;
  height: 34px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: #475569;
  font-size: 15px;
  background: #fff;
  border: 1px solid #e2e8f0;
  border-radius: 9px;
}

.profile-account-copy {
  min-width: 0;
  display: grid;
  gap: 1px;
}

.profile-account-copy > span {
  color: #64748b;
  font-size: 12px;
  line-height: 18px;
}

.profile-account-copy > strong,
.profile-account-copy :deep(.ant-typography) {
  min-width: 0;
  margin-bottom: 0;
  color: #0f172a;
  font-size: 14px;
  font-weight: 600;
  line-height: 20px;
}

.profile-copy-value {
  word-break: break-all;
}

.profile-capability-list {
  display: grid;
  gap: 12px;
}

.profile-capability-item {
  display: grid;
  grid-template-columns: 42px minmax(0, 1fr) auto;
  align-items: center;
  gap: 12px;
  padding: 14px;
  background: #f8fafc;
  border: 1px solid #eef2f7;
  border-radius: 14px;
}

.profile-capability-icon {
  width: 42px;
  height: 42px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 18px;
  border-radius: 12px;
}

.profile-capability-icon.role {
  color: #1d4ed8;
  background: #dbeafe;
}

.profile-capability-icon.image {
  color: #7c3aed;
  background: #ede9fe;
}

.profile-capability-icon.status {
  color: #047857;
  background: #d1fae5;
}

.profile-capability-copy {
  min-width: 0;
  display: grid;
  gap: 2px;
}

.profile-capability-copy span {
  color: #64748b;
  font-size: 12px;
}

.profile-capability-copy strong {
  color: #0f172a;
  font-size: 15px;
}

.profile-capability-copy small {
  color: #64748b;
  font-size: 12px;
  line-height: 18px;
}

.profile-security-card {
  scroll-margin-top: 18px;
}

.profile-security-extra {
  color: #64748b;
  font-size: 12px;
}

.profile-password-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 16px;
}

.profile-password-grid.forced {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.profile-time-card :deep(.ant-card-body) {
  padding: 18px 24px;
}

.profile-time-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 16px;
}

.profile-time-grid > div {
  min-width: 0;
  display: grid;
  gap: 4px;
  padding-left: 14px;
  border-left: 3px solid #e2e8f0;
}

.profile-time-grid span {
  color: #64748b;
  font-size: 12px;
}

.profile-time-grid strong {
  overflow: hidden;
  color: #334155;
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@media (max-width: 900px) {
  .profile-grid {
    grid-template-columns: 1fr;
  }

  .profile-password-grid,
  .profile-password-grid.forced {
    grid-template-columns: 1fr;
    gap: 0;
  }

}

@media (max-width: 640px) {
  .profile-page {
    gap: 14px;
  }

  .profile-hero :deep(.ant-card-body) {
    padding: 20px 18px;
  }

  .profile-hero-content {
    align-items: flex-start;
    gap: 14px;
  }

  .profile-avatar {
    width: 54px;
    height: 54px;
    flex-basis: 54px;
    font-size: 18px;
    border-width: 3px;
  }

  .profile-title-row {
    align-items: flex-start;
    flex-direction: column;
    gap: 7px;
  }

  .profile-title-row h2 {
    font-size: 20px;
    line-height: 28px;
  }

  .profile-capability-item {
    grid-template-columns: 38px minmax(0, 1fr);
  }

  .profile-capability-icon {
    width: 38px;
    height: 38px;
  }

  .profile-capability-item :deep(.ant-tag) {
    grid-column: 2;
    width: max-content;
  }

  .profile-basic-card :deep(.ant-card-body) {
    padding: 20px 18px;
  }

  .profile-basic-editor-control {
    grid-template-columns: 1fr;
  }

  .profile-account-title {
    align-items: flex-start;
    flex-direction: column;
    gap: 2px;
  }

  .profile-security-card :deep(.ant-card-head) {
    align-items: flex-start;
  }

  .profile-security-extra {
    display: none;
  }

  .profile-time-grid {
    grid-template-columns: 1fr;
    gap: 12px;
  }

  .profile-time-grid > div {
    min-height: 42px;
  }

  .profile-section-card :deep(.ant-btn-primary),
  .profile-security-card :deep(.ant-btn-primary) {
    width: 100%;
  }
}
</style>
