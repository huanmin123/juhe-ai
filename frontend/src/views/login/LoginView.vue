<template>
  <div ref="pageRef" class="login-page" @pointerenter="handlePointerEnter" @pointerleave="handlePointerLeave" @pointermove="handlePointerMove">
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
        <h2>用户登录</h2>
      </div>
      <a-form layout="vertical" @submit.prevent="handleLogin">
        <a-form-item label="系统账户">
          <a-input v-model:value="form.username" size="large" autocomplete="username" placeholder="请输入用户名" />
        </a-form-item>
        <a-form-item label="密码">
          <a-input-password v-model:value="form.password" size="large" autocomplete="current-password" placeholder="请输入密码" />
        </a-form-item>
        <a-button block size="large" type="primary" :loading="loading" @click="handleLogin">进入控制台</a-button>
      </a-form>
    </a-card>
  </div>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { login } from '@/composables/useAuth'
import { appBrand, loadGlobalBrandSettings } from '@/composables/useAppBrand'
import { getPreferredEntryPath } from '@/composables/useMenuMode'
import { extractApiErrorMessage } from '@/shared/apiError'

import LoginBackground from './LoginBackground.vue'
import LoginBrandPanel from './LoginBrandPanel.vue'

const router = useRouter()
const route = useRoute()
const loading = ref(false)
const form = reactive({ username: '', password: '' })
const whitespacePattern = /\s/

const loginTitle = computed(() => `${appBrand.appName} 管理平台`)
const loginSubtitle = '统一接入、统一调度、统一可观测。'
const loginBadge = '统一接入平台'

const cursorLightOffset = 160
const pageRef = ref<HTMLElement | null>(null)
let pageRect: DOMRect | undefined
let cursorLightElement: HTMLElement | null = null
let pointerFrame = 0
let pendingPointer: { x: number; y: number } | undefined

function handlePointerMove(event: PointerEvent): void {
  if (event.pointerType === 'touch') return
  if (!pageRect) updatePageRect()
  if (!pageRect) return
  pendingPointer = {
    x: event.clientX - pageRect.left,
    y: event.clientY - pageRect.top
  }
  if (pointerFrame) return
  pointerFrame = window.requestAnimationFrame(applyPointerPosition)
}

function handlePointerEnter(): void {
  updatePageRect()
  updateCursorLightElement()
  pageRef.value?.classList.add('login-page-pointer-active')
}

function handlePointerLeave(): void {
  pageRef.value?.classList.remove('login-page-pointer-active')
}

function updatePageRect(): void {
  pageRect = pageRef.value?.getBoundingClientRect()
}

function updateCursorLightElement(): void {
  cursorLightElement = pageRef.value?.querySelector<HTMLElement>('.login-cursor-light') ?? null
}

function applyPointerPosition(): void {
  pointerFrame = 0
  const element = cursorLightElement
  const point = pendingPointer
  if (!element || !point) return
  element.style.transform = `translate3d(${Math.round(point.x - cursorLightOffset)}px, ${Math.round(point.y - cursorLightOffset)}px, 0)`
}

async function handleLogin() {
  if (loading.value) return
  if (!form.username.trim() || !form.password) {
    message.warning('请输入账号和密码')
    return
  }
  if (hasWhitespace(form.username) || hasWhitespace(form.password)) {
    message.warning('用户名和密码不能包含空格')
    return
  }
  loading.value = true
  try {
    const user = await login({
      username: form.username.trim(),
      password: form.password
    })
    if (user.mustChangePassword) {
      message.warning('当前账户使用初始密码，请先完成修改')
    }
    await router.replace(resolveLoginRedirect(user))
  } catch (error) {
    console.error(error)
    message.error(getLoginErrorMessage(error))
  } finally {
    loading.value = false
  }
}

function getLoginErrorMessage(error: unknown): string {
  return extractApiErrorMessage(error, '登录失败，请检查账号或密码')
}

function resolveLoginRedirect(user: Awaited<ReturnType<typeof login>>): string {
  const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : ''
  if (redirect.startsWith('/') && !redirect.startsWith('//')) {
    const resolved = router.resolve(redirect)
    if (resolved.name !== 'not-found' && resolved.path !== '/login') {
      return resolved.fullPath
    }
  }
  return getPreferredEntryPath(user)
}

function hasWhitespace(value: string): boolean {
  return whitespacePattern.test(value)
}

onMounted(async () => {
  updatePageRect()
  updateCursorLightElement()
  window.addEventListener('resize', updatePageRect, { passive: true })
  await loadBrandSettings()
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', updatePageRect)
  if (pointerFrame) window.cancelAnimationFrame(pointerFrame)
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
  box-sizing: border-box;
  position: relative;
  width: 100%;
  min-height: 100vh;
  display: grid;
  grid-template-columns: minmax(540px, 720px) minmax(360px, 430px);
  justify-content: center;
  gap: clamp(36px, 3.6vw, 68px);
  align-items: center;
  overflow: hidden;
  padding: 60px clamp(28px, 4vw, 56px);
  background: #020617;
  color: #fff;
}

.login-card {
  position: relative;
  z-index: 1;
  width: 100%;
  max-width: 430px;
  margin-top: 0;
  justify-self: center;
  overflow: hidden;
  color: #0f172a;
  background:
    linear-gradient(180deg, rgba(255, 255, 255, 0.98), rgba(241, 246, 255, 0.96)),
    radial-gradient(circle at 0% 0%, rgba(96, 165, 250, 0.16), transparent 38%);
  border: 1px solid rgba(219, 234, 254, 0.86);
  border-radius: 8px;
  box-shadow: 0 34px 96px rgba(0, 0, 0, 0.46), 0 0 0 1px rgba(255, 255, 255, 0.72) inset;
  animation: cardFloat 7.5s ease-in-out infinite;
  will-change: transform;
}

.login-card::before {
  content: '';
  position: absolute;
  top: 0;
  right: 0;
  left: 0;
  height: 3px;
  background: linear-gradient(90deg, #2563eb, #38bdf8 58%, #818cf8);
}

.login-card::after {
  content: '';
  position: absolute;
  inset: 0;
  background:
    radial-gradient(circle at 100% 0%, rgba(191, 219, 254, 0.28), transparent 26%),
    linear-gradient(180deg, rgba(255, 255, 255, 0.38), transparent 22%);
  opacity: 0.78;
  pointer-events: none;
}

.login-card :deep(.ant-card-body) {
  padding: 30px 36px 34px;
}

.login-card :deep(.ant-form-item-label > label) {
  color: #253349;
  font-weight: 700;
}

.login-card :deep(.ant-input),
.login-card :deep(.ant-input-affix-wrapper) {
  color: #0f172a;
  background: rgba(248, 250, 252, 0.98);
  border-color: rgba(191, 219, 254, 0.92);
  border-radius: 8px;
  box-shadow: inset 0 1px 1px rgba(255, 255, 255, 0.78);
  transition: border-color 0.18s ease, box-shadow 0.18s ease, background 0.18s ease;
}

.login-card :deep(.ant-input::placeholder),
.login-card :deep(.ant-input-affix-wrapper input::placeholder) {
  color: #94a3b8;
}

.login-card :deep(.ant-input:hover),
.login-card :deep(.ant-input-affix-wrapper:hover) {
  border-color: rgba(96, 165, 250, 0.72);
}

.login-card :deep(.ant-input:focus),
.login-card :deep(.ant-input-affix-wrapper-focused) {
  border-color: #60a5fa;
  box-shadow: 0 0 0 3px rgba(96, 165, 250, 0.16);
}

.login-card :deep(.ant-btn) {
  height: 44px;
  border-radius: 8px;
  font-weight: 800;
  box-shadow: 0 14px 34px rgba(37, 99, 235, 0.28);
  transition: transform 0.18s ease, box-shadow 0.18s ease;
}

.login-card :deep(.ant-btn-primary) {
  background: linear-gradient(90deg, #2563eb, #0ea5e9);
  border-color: transparent;
}

.login-card :deep(.ant-btn-primary:hover) {
  transform: translateY(-1px);
  box-shadow: 0 18px 38px rgba(37, 99, 235, 0.34);
}

.login-card-heading {
  margin-bottom: 24px;
  text-align: center;
}

.login-card-heading h2 {
  margin: 0;
  color: #0f172a;
  font-size: 28px;
  font-weight: 900;
  line-height: 1.2;
}

@keyframes cardFloat {
  0%, 100% {
    transform: translate3d(0, 0, 0);
  }
  50% {
    transform: translate3d(0, -4px, 0);
  }
}

@media (min-width: 1600px) {
  .login-page {
    align-items: start;
    grid-template-columns: minmax(620px, 800px) minmax(392px, 452px);
    gap: clamp(52px, 4vw, 88px);
    padding: clamp(72px, 8vh, 108px) clamp(40px, 4vw, 72px) 72px;
  }

  .login-card {
    max-width: 452px;
    margin-top: 56px;
  }

  .login-card :deep(.ant-card-body) {
    padding: 34px 40px 38px;
  }

  .login-card-heading {
    margin-bottom: 28px;
  }

  .login-card-heading h2 {
    font-size: 30px;
  }
}

@media (min-width: 1920px) {
  .login-page {
    align-items: start;
    grid-template-columns: minmax(680px, 860px) minmax(404px, 468px);
    gap: clamp(64px, 4.6vw, 112px);
    padding: clamp(86px, 10vh, 132px) clamp(48px, 4.8vw, 92px) 84px;
  }

  .login-card {
    max-width: 468px;
    margin-top: 72px;
  }
}

@media (max-width: 1200px) {
  .login-page {
    grid-template-columns: minmax(480px, 620px) minmax(340px, 408px);
    gap: clamp(28px, 3vw, 44px);
    padding: 44px clamp(20px, 3vw, 36px);
  }

  .login-card {
    max-width: 408px;
  }
}

@media (max-width: 1080px) {
  .login-page {
    min-height: 100dvh;
    grid-template-columns: 1fr;
    justify-items: center;
    align-content: center;
    gap: 0;
    padding: 36px 24px;
  }

  .login-card {
    margin-top: 0;
    justify-self: center;
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
    border-radius: 8px;
  }

  .login-card :deep(.ant-card-body) {
    padding: 24px;
  }

  .login-card:hover {
    transform: none;
  }

  .login-card-heading h2 {
    font-size: 22px;
  }

}

</style>
