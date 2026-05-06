<template>
  <div class="login-page" :style="mouseStyle" @pointermove="handlePointerMove">
    <LoginBackground />
    <LoginBrandPanel
      :app-icon="appBrand.appIcon"
      :app-name="appBrand.appName"
      :title="loginTitle"
      :subtitle="loginSubtitle"
      :badge="loginBadge"
    />

    <a-card class="login-card" :bordered="false">
      <div class="login-card-heading">
        <div>
          <h2>安全登录</h2>
        </div>
      </div>
      <a-form layout="vertical" @submit.prevent="handleLogin">
        <a-form-item label="系统账户">
          <a-input v-model:value="form.username" size="large" autocomplete="username" placeholder="请输入用户名" />
        </a-form-item>
        <a-form-item label="密码">
          <a-input-password v-model:value="form.password" size="large" autocomplete="current-password" placeholder="请输入密码" />
        </a-form-item>
        <a-form-item label="验证码">
          <div class="captcha-row">
            <a-input v-model:value="form.captchaCode" size="large" autocomplete="off" maxlength="6" placeholder="请输入验证码" />
            <button class="captcha-image-button" type="button" :disabled="captchaLoading" title="点击刷新验证码" @click="refreshCaptcha">
              <img v-if="captcha?.image" :src="captcha.image" alt="验证码" />
              <span v-else>{{ captchaLoading ? '加载中' : '刷新' }}</span>
            </button>
          </div>
        </a-form-item>
        <a-button block size="large" type="primary" :loading="loading" @click="handleLogin">进入控制台</a-button>
      </a-form>
    </a-card>
  </div>
</template>

<script setup lang="ts">
import axios from 'axios'
import { message } from '@/lib/antd'
import { computed, onMounted, reactive, ref, type CSSProperties } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { loadCaptcha, login } from '@/composables/useAuth'
import { appBrand, loadGlobalBrandSettings } from '@/composables/useAppBrand'
import type { CaptchaChallengeSummary } from '@/types/domain'

import LoginBackground from './LoginBackground.vue'
import LoginBrandPanel from './LoginBrandPanel.vue'

const router = useRouter()
const route = useRoute()
const loading = ref(false)
const captchaLoading = ref(false)
const captcha = ref<CaptchaChallengeSummary>()
const form = reactive({ username: '', password: '', captchaCode: '' })

const loginTitle = computed(() => `${appBrand.appName} 管理平台`)
const loginSubtitle = '统一接入、统一调度、统一可观测。'
const loginBadge = '统一接入平台'

const mouse = reactive({ x: 50, y: 50 })
const mouseStyle = computed<CSSProperties>(() => ({
  '--mouse-x': `${mouse.x}%`,
  '--mouse-y': `${mouse.y}%`
} as CSSProperties))

function handlePointerMove(event: PointerEvent): void {
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect()
  mouse.x = Math.round(((event.clientX - rect.left) / rect.width) * 100)
  mouse.y = Math.round(((event.clientY - rect.top) / rect.height) * 100)
}

async function handleLogin() {
  if (loading.value) return
  if (!form.username.trim() || !form.password || !form.captchaCode.trim()) {
    message.warning('请输入账号、密码和验证码')
    return
  }
  if (!captcha.value?.captchaId) {
    message.warning('验证码未加载，请刷新验证码')
    return
  }
  loading.value = true
  try {
    const user = await login({
      username: form.username.trim(),
      password: form.password,
      captchaId: captcha.value.captchaId,
      captchaCode: form.captchaCode
    })
    if (user.mustChangePassword) {
      message.warning('当前账户仍在使用初始密码，请尽快在右上角修改密码')
    }
    await router.replace(typeof route.query.redirect === 'string' ? route.query.redirect : '/accounts')
  } catch (error) {
    console.error(error)
    message.error(getLoginErrorMessage(error))
    await refreshCaptcha()
  } finally {
    loading.value = false
  }
}

async function refreshCaptcha(): Promise<void> {
  captchaLoading.value = true
  try {
    captcha.value = await loadCaptcha()
    form.captchaCode = ''
  } catch (error) {
    console.error(error)
    message.error('验证码加载失败，请刷新页面重试')
  } finally {
    captchaLoading.value = false
  }
}

function getLoginErrorMessage(error: unknown): string {
  if (axios.isAxiosError<{ message?: string }>(error)) {
    return error.response?.data?.message ?? '登录失败，请检查账号、密码或验证码'
  }
  return '登录失败，请检查账号、密码或验证码'
}

onMounted(async () => {
  await Promise.all([loadBrandSettings(), refreshCaptcha()])
})

async function loadBrandSettings(): Promise<void> {
  try {
    await loadGlobalBrandSettings()
  } catch (error) {
    console.error(error)
  }
}
</script>

<style scoped>
.login-page {
  position: relative;
  min-height: 100vh;
  display: grid;
  grid-template-columns: minmax(0, 1.1fr) 430px;
  gap: 44px;
  align-items: center;
  overflow: hidden;
  padding: 64px min(7vw, 92px);
  background: #03111f;
  color: #fff;
  --mouse-x: 50%;
  --mouse-y: 50%;
}

.login-card {
  position: relative;
  z-index: 1;
  padding: 12px;
  background: rgba(255, 255, 255, 0.92);
  border: 1px solid rgba(255, 255, 255, 0.5);
  border-radius: 24px;
  box-shadow: 0 28px 70px rgba(2, 8, 23, 0.38);
  backdrop-filter: blur(18px);
}

.login-card-heading h2 {
  margin: 0 0 30px;
  color: #0f172a;
  font-size: 24px;
  font-weight: 900;
  text-align: center;
}

.login-card-heading p {
  margin: 8px 0 24px;
  color: #64748b;
  line-height: 1.6;
}

.captcha-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 144px;
  gap: 12px;
  align-items: center;
}

.captcha-image-button {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 144px;
  height: 46px;
  padding: 0;
  overflow: hidden;
  color: #2563eb;
  font-weight: 700;
  background: #eff6ff;
  border: 1px solid #bfdbfe;
  border-radius: 10px;
  cursor: pointer;
  transition: border-color 0.18s ease, box-shadow 0.18s ease;
}

.captcha-image-button:hover:not(:disabled) {
  border-color: #60a5fa;
  box-shadow: 0 0 0 3px rgba(96, 165, 250, 0.16);
}

.captcha-image-button:disabled {
  cursor: not-allowed;
  opacity: 0.72;
}

.captcha-image-button img {
  display: block;
  width: 144px;
  height: 46px;
}

@media (max-width: 980px) {
  .login-page {
    grid-template-columns: 1fr;
    padding: 32px 18px;
  }

  .login-card {
    width: 100%;
  }
}

@media (max-width: 820px) {
  .login-page {
    min-height: 100dvh;
    grid-template-columns: minmax(0, 430px);
    justify-content: center;
    gap: 0;
    padding: 24px 16px;
  }

  .login-card {
    width: 100%;
    max-width: 430px;
    padding: 8px;
    border-radius: 22px;
  }

  .login-card:hover {
    transform: none;
  }

  .login-card-heading h2 {
    font-size: 22px;
  }

  .login-card-heading p {
    margin-bottom: 20px;
    font-size: 13px;
  }

  .captcha-row {
    grid-template-columns: 1fr;
  }

  .captcha-image-button,
  .captcha-image-button img {
    width: 100%;
  }
}
</style>
