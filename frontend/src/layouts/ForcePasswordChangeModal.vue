<template>
  <a-modal
    :open="open"
    title="需要修改登录密码"
    :width="480"
    :closable="false"
    :mask-closable="false"
    :keyboard="false"
    :footer="null"
    centered
    class="force-password-change-modal"
  >
    <div class="force-password-change">
      <p class="force-password-change-lead">当前账户需要修改登录密码，修改后才能进入控制台。</p>
      <p v-if="userLabel" class="force-password-change-user">
        当前账户：<strong>{{ userLabel }}</strong>
      </p>
      <a-form layout="vertical">
        <a-form-item label="新密码" required extra="至少 4 位，不能包含空格。">
          <a-input-password
            v-model:value="passwordForm.newPassword"
            :disabled="saving"
            autocomplete="new-password"
            placeholder="请输入新密码"
            @keyup.enter="submitPassword"
          />
        </a-form-item>
        <a-form-item label="确认新密码" required>
          <a-input-password
            v-model:value="passwordForm.confirmPassword"
            :disabled="saving"
            autocomplete="new-password"
            placeholder="请再次输入新密码"
            @keyup.enter="submitPassword"
          />
        </a-form-item>
      </a-form>
      <div class="force-password-change-actions">
        <a-button type="primary" :loading="saving" :disabled="saving || loggingOut" @click="submitPassword">
          <LockOutlined />
          保存并进入控制台
        </a-button>
        <a-button
          type="link"
          class="force-password-change-logout"
          :loading="loggingOut"
          :disabled="saving || loggingOut"
          @click="handleLogout"
        >
          退出登录
        </a-button>
      </div>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { LockOutlined } from '@ant-design/icons-vue'
import { computed, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { authState, changePassword, loadCurrentUser, logout as logoutCurrentSession } from '@/composables/useAuth'
import { getPreferredEntryPath } from '@/composables/useMenuMode'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { normalizeProfileRedirectPath } from '@/views/profile/profileNavigation'

const route = useRoute()
const router = useRouter()
const currentUser = authState.currentUser
// 弹窗由会话状态驱动：后端返回 mustChangePassword 为 true 时覆盖整个应用，
// 改密成功后 changePassword 更新 authState，弹窗随 open 变为 false 自动关闭。
const open = computed(() => Boolean(currentUser.value?.mustChangePassword))
const userLabel = computed(() => currentUser.value?.displayName?.trim() || currentUser.value?.username || '')
const passwordForm = reactive({ newPassword: '', confirmPassword: '' })
const saving = ref(false)
const loggingOut = ref(false)

// 弹窗关闭后清空输入，避免下一个首次登录账户复用上一个账户残留的密码。
watch(open, (visible) => {
  if (!visible) resetPasswordForm()
})

async function submitPassword(): Promise<void> {
  if (saving.value || loggingOut.value) return
  if (/\s/.test(passwordForm.newPassword) || /\s/.test(passwordForm.confirmPassword)) {
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
  saving.value = true
  try {
    const user = await changePassword({ newPassword: passwordForm.newPassword })
    message.success('登录密码已修改')
    const redirect = normalizeProfileRedirectPath(route.query.redirect)
    await router.replace(redirect || getPreferredEntryPath(user))
  } catch (error) {
    // 不在控制台打印 AxiosError：其 config.data 含新密码。仅记录脱敏文案。
    const errorMessage = extractApiErrorMessage(error, '修改密码失败')
    // 会话可能在另一个标签页/管理端已完成改密；强制刷新摘要，若已无需改密
    // 则让弹窗随 authState 关闭，避免只有新密码输入框却要求当前密码的死路。
    try {
      const refreshed = await loadCurrentUser(true)
      if (refreshed && !refreshed.mustChangePassword) return
    } catch {
      // 刷新失败时保留原始错误提示；不改变当前会话。
    }
    console.error('修改密码失败', errorMessage)
    message.error(errorMessage)
  } finally {
    saving.value = false
  }
}

async function handleLogout(): Promise<void> {
  if (saving.value || loggingOut.value) return
  loggingOut.value = true
  try {
    await logoutCurrentSession()
    await router.replace('/login')
  } catch (error) {
    const errorMessage = extractApiErrorMessage(error, '退出登录失败，请稍后重试')
    console.error('退出登录失败', errorMessage)
    message.error(errorMessage)
  } finally {
    loggingOut.value = false
  }
}

function resetPasswordForm(): void {
  passwordForm.newPassword = ''
  passwordForm.confirmPassword = ''
}
</script>

<style scoped>
.force-password-change-lead {
  margin: 0 0 8px;
  color: var(--juhe-fg-soft);
  font-size: 14px;
  line-height: 24px;
}

.force-password-change-user {
  margin: 0 0 16px;
  color: var(--juhe-muted);
  font-size: 13px;
  line-height: 22px;
}

.force-password-change-user strong {
  color: var(--juhe-fg);
  font-weight: 600;
}

.force-password-change-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  margin-top: 4px;
}

.force-password-change-logout {
  margin-left: auto;
  padding-right: 0;
  color: var(--juhe-muted);
}

.force-password-change-logout:not(:disabled):hover {
  color: var(--juhe-danger);
}
</style>
