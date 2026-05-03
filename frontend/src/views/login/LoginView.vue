<template>
  <div class="login-page" :style="mouseStyle" @pointermove="handlePointerMove">
    <div class="login-bg-grid" />
    <div class="login-orb login-orb-blue" />
    <div class="login-orb login-orb-cyan" />
    <div class="login-mouse-glow" />
    <div class="login-scanline" />
    <div class="login-data-streams" aria-hidden="true">
      <span v-for="stream in dataStreams" :key="stream" />
    </div>

    <section class="hero-panel">
      <div class="hero-title-row">
        <img class="brand-icon" :src="appBrand.appIcon" :alt="`${appBrand.appName} 图标`" />
        <h1>{{ loginTitle }}</h1>
      </div>
      <p>{{ loginSubtitle }}</p>
      <div class="hero-divider">
        <span />
        <strong>AI GATEWAY PLATFORM</strong>
      </div>
      <div class="hero-topline">
        <div class="hero-badge">{{ loginBadge }}</div>
        <div class="hero-status">
          <span />
          Control Plane Online
        </div>
      </div>
      <div class="hero-command-panel">
        <div class="command-grid">
          <span v-for="(node, index) in commandNodes" :key="node" :style="{ '--delay': `${index * 0.18}s` }" />
        </div>
        <div class="command-copy">
          <span>统一纳管</span>
          <strong>供应商 · 账号 · 分组 · 密钥 · 记录</strong>
        </div>
        <div class="command-glow" />
      </div>
      <div class="signal-grid">
        <div v-for="item in signals" :key="item.title" class="signal-card">
          <span class="signal-index">{{ item.index }}</span>
          <span class="signal-value">{{ item.value }}</span>
          <span class="signal-title">{{ item.title }}</span>
        </div>
      </div>
      <div class="hero-footnote">为多模型、多供应商和多账号体系预留统一控制面。</div>
    </section>

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
import { message } from 'ant-design-vue'
import { computed, onMounted, reactive, ref, type CSSProperties } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { loadCaptcha, login } from '@/composables/useAuth'
import { appBrand, loadGlobalBrandSettings } from '@/composables/useAppBrand'
import type { CaptchaChallengeSummary, GlobalSettings } from '@/types/domain'

const router = useRouter()
const route = useRoute()
const loading = ref(false)
const captchaLoading = ref(false)
const globalSettings = ref<GlobalSettings>({})
const captcha = ref<CaptchaChallengeSummary>()
const form = reactive({ username: '', password: '', captchaCode: '' })

const loginTitle = computed(() => stringValue(globalSettings.value.loginTitle, `${appBrand.appName} 管理平台`))
const loginSubtitle = computed(() => stringValue(globalSettings.value.loginSubtitle, '统一接入、统一调度、统一可观测。'))
const loginBadge = computed(() => stringValue(globalSettings.value.loginBadge, '统一接入平台'))

const signals = [
  { index: '01', value: '多厂商接入', title: '统一纳管模型供应商与上游账号' },
  { index: '02', value: '智能调度', title: '围绕分组、密钥和策略完成路由' },
  { index: '03', value: '安全隔离', title: '系统账户、配置和记录独立成域' }
]
const commandNodes = ['providers', 'accounts', 'groups', 'keys', 'usage', 'settings']
const dataStreams = Array.from({ length: 9 }, (_, index) => index)
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

function stringValue(value: unknown, fallback: string): string {
  return typeof value === 'string' && value.trim() ? value.trim() : fallback
}

onMounted(async () => {
  await Promise.all([loadBrandSettings(), refreshCaptcha()])
})

async function loadBrandSettings(): Promise<void> {
  try {
    globalSettings.value = await loadGlobalBrandSettings()
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

.login-bg-grid {
  position: absolute;
  inset: 0;
  background:
    linear-gradient(rgba(148, 163, 184, 0.08) 1px, transparent 1px),
    linear-gradient(90deg, rgba(148, 163, 184, 0.08) 1px, transparent 1px),
    radial-gradient(circle at 20% 20%, rgba(22, 119, 255, 0.26), transparent 34%),
    radial-gradient(circle at 75% 65%, rgba(34, 211, 238, 0.16), transparent 30%);
  background-size: 42px 42px, 42px 42px, auto, auto;
  animation: gridDrift 18s linear infinite;
}

.login-orb {
  position: absolute;
  width: 260px;
  height: 260px;
  border-radius: 999px;
  filter: blur(8px);
  opacity: 0.38;
}

.login-orb-blue {
  top: 11%;
  right: 18%;
  background: radial-gradient(circle, rgba(59, 130, 246, 0.72), transparent 66%);
  animation: orbFloat 9s ease-in-out infinite;
}

.login-orb-cyan {
  bottom: 8%;
  left: 9%;
  background: radial-gradient(circle, rgba(45, 212, 191, 0.56), transparent 68%);
  animation: orbFloat 11s ease-in-out infinite reverse;
}

.login-mouse-glow {
  position: absolute;
  inset: 0;
  pointer-events: none;
  background: radial-gradient(circle at var(--mouse-x) var(--mouse-y), rgba(56, 189, 248, 0.18), rgba(37, 99, 235, 0.08) 16%, transparent 34%);
  mix-blend-mode: screen;
  transition: background 0.12s ease-out;
}

.login-scanline {
  position: absolute;
  inset: 0;
  pointer-events: none;
  background: linear-gradient(180deg, transparent 0%, rgba(125, 211, 252, 0.08) 48%, transparent 56%);
  opacity: 0.38;
  animation: scanlineMove 8s linear infinite;
}

.login-data-streams {
  position: absolute;
  inset: 0;
  overflow: hidden;
  pointer-events: none;
}

.login-data-streams span {
  position: absolute;
  top: -18%;
  width: 1px;
  height: 22vh;
  background: linear-gradient(180deg, transparent, rgba(96, 165, 250, 0.42), transparent);
  opacity: 0.28;
  animation: streamFall 7s linear infinite;
}

.login-data-streams span:nth-child(1) { left: 8%; animation-delay: 0s; }
.login-data-streams span:nth-child(2) { left: 18%; animation-delay: 1.4s; height: 18vh; }
.login-data-streams span:nth-child(3) { left: 31%; animation-delay: 2.8s; }
.login-data-streams span:nth-child(4) { left: 44%; animation-delay: 0.8s; height: 26vh; }
.login-data-streams span:nth-child(5) { left: 58%; animation-delay: 3.6s; }
.login-data-streams span:nth-child(6) { left: 69%; animation-delay: 1.9s; height: 16vh; }
.login-data-streams span:nth-child(7) { left: 78%; animation-delay: 4.5s; }
.login-data-streams span:nth-child(8) { left: 87%; animation-delay: 2.2s; height: 20vh; }
.login-data-streams span:nth-child(9) { left: 95%; animation-delay: 5.2s; }

.hero-panel,
.login-card {
  position: relative;
  z-index: 1;
}

.hero-panel {
  max-width: 980px;
}

.hero-topline {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  align-items: center;
  margin-top: 30px;
}

.hero-badge {
  display: inline-flex;
  padding: 8px 12px;
  color: #93c5fd;
  background: rgba(15, 23, 42, 0.62);
  border: 1px solid rgba(96, 165, 250, 0.26);
  border-radius: 999px;
  font-size: 12px;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  box-shadow: 0 0 24px rgba(59, 130, 246, 0.12);
  transition: transform 0.22s ease, border-color 0.22s ease, box-shadow 0.22s ease;
}

.hero-badge:hover {
  transform: translateY(-1px);
  border-color: rgba(125, 211, 252, 0.48);
  box-shadow: 0 0 30px rgba(59, 130, 246, 0.22);
}

.hero-status {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: #8aa6c9;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 11px;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.hero-status span {
  width: 7px;
  height: 7px;
  background: #22c55e;
  border-radius: 999px;
  box-shadow: 0 0 14px rgba(34, 197, 94, 0.82);
  animation: statusPulse 1.8s ease-in-out infinite;
}

.hero-command-panel {
  position: relative;
  display: grid;
  grid-template-columns: 210px minmax(0, 1fr);
  gap: 22px;
  align-items: center;
  max-width: 620px;
  min-height: 112px;
  margin-top: 14px;
  padding: 18px 20px;
  overflow: hidden;
  background:
    linear-gradient(90deg, rgba(15, 23, 42, 0.74), rgba(15, 23, 42, 0.38)),
    radial-gradient(circle at 22% 50%, rgba(37, 99, 235, 0.26), transparent 42%);
  border: 1px solid rgba(96, 165, 250, 0.18);
  border-radius: 24px;
  box-shadow: 0 20px 60px rgba(2, 8, 23, 0.22), inset 0 1px 0 rgba(255, 255, 255, 0.04);
  backdrop-filter: blur(16px);
  transition: transform 0.28s ease, border-color 0.28s ease, box-shadow 0.28s ease;
}

.hero-command-panel::before {
  content: '';
  position: absolute;
  inset: -1px;
  background: linear-gradient(120deg, transparent 10%, rgba(125, 211, 252, 0.28) 34%, transparent 58%);
  opacity: 0;
  transform: translateX(-40%);
  transition: opacity 0.28s ease;
}

.hero-command-panel:hover {
  transform: translateY(-3px);
  border-color: rgba(125, 211, 252, 0.34);
  box-shadow: 0 24px 80px rgba(8, 47, 73, 0.38), inset 0 1px 0 rgba(255, 255, 255, 0.06);
}

.hero-command-panel:hover::before {
  opacity: 1;
  animation: panelSweep 1.5s ease forwards;
}

.command-grid {
  position: relative;
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 12px;
}

.command-grid::before,
.command-grid::after {
  content: '';
  position: absolute;
  inset: 50% 14px auto;
  height: 1px;
  background: linear-gradient(90deg, rgba(96, 165, 250, 0), rgba(96, 165, 250, 0.6), rgba(96, 165, 250, 0));
}

.command-grid::after {
  inset: 14px auto 14px 50%;
  width: 1px;
  height: auto;
  background: linear-gradient(180deg, rgba(96, 165, 250, 0), rgba(96, 165, 250, 0.54), rgba(96, 165, 250, 0));
}

.command-grid span {
  position: relative;
  z-index: 1;
  width: 42px;
  height: 42px;
  background: linear-gradient(180deg, rgba(30, 64, 175, 0.65), rgba(15, 23, 42, 0.7));
  border: 1px solid rgba(147, 197, 253, 0.28);
  border-radius: 14px;
  box-shadow: 0 0 26px rgba(37, 99, 235, 0.18);
  animation: nodeBreath 2.8s ease-in-out infinite;
  animation-delay: var(--delay);
}

.command-grid span::after {
  content: '';
  position: absolute;
  inset: 13px;
  background: #7dd3fc;
  border-radius: 999px;
  box-shadow: 0 0 14px rgba(125, 211, 252, 0.9);
  animation: nodeCore 1.9s ease-in-out infinite;
  animation-delay: var(--delay);
}

.command-copy {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.command-copy span {
  color: #7dd3fc;
  font-size: 12px;
  font-weight: 800;
  letter-spacing: 0.18em;
}

.command-copy strong {
  max-width: 320px;
  color: #eaf4ff;
  font-size: 20px;
  font-weight: 900;
  line-height: 1.4;
}

.command-glow {
  position: absolute;
  right: -40px;
  bottom: -70px;
  width: 160px;
  height: 160px;
  background: radial-gradient(circle, rgba(34, 211, 238, 0.28), transparent 66%);
  animation: glowPulse 4s ease-in-out infinite;
}

.hero-title-row {
  display: flex;
  align-items: flex-start;
  gap: 18px;
  margin-top: 0;
}

.brand-icon {
  flex: 0 0 auto;
  width: 58px;
  height: 58px;
  margin-top: 3px;
  padding: 10px;
  background: rgba(255, 255, 255, 0.94);
  border-radius: 18px;
  box-shadow: 0 0 34px rgba(59, 130, 246, 0.46);
  animation: iconFloat 3.2s ease-in-out infinite;
}

.hero-panel h1 {
  max-width: 680px;
  margin: 0 0 14px;
  color: #fff;
  font-size: clamp(40px, 5vw, 68px);
  font-weight: 900;
  line-height: 1.02;
  letter-spacing: -0.04em;
  text-shadow: 0 0 32px rgba(96, 165, 250, 0.18);
}

.hero-panel p {
  max-width: 560px;
  margin: 0;
  color: #b6c6dc;
  font-size: 16px;
  line-height: 1.8;
}

.hero-divider {
  display: flex;
  align-items: center;
  gap: 12px;
  max-width: 600px;
  margin-top: 30px;
  color: #8fbaff;
  font-size: 11px;
  font-weight: 800;
  letter-spacing: 0.18em;
}

.hero-divider span {
  width: 48px;
  height: 1px;
  background: linear-gradient(90deg, #60a5fa, rgba(96, 165, 250, 0));
}

.hero-divider strong {
  font-weight: 800;
}

.signal-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 12px;
  max-width: 600px;
  margin-top: 24px;
}

.signal-card {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 124px;
  padding: 18px 16px 17px;
  background: linear-gradient(180deg, rgba(15, 23, 42, 0.66), rgba(15, 23, 42, 0.5));
  border: 1px solid rgba(148, 163, 184, 0.18);
  border-radius: 18px;
  backdrop-filter: blur(14px);
  box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.04);
  transition: transform 0.22s ease, border-color 0.22s ease, background 0.22s ease, box-shadow 0.22s ease;
}

.signal-card:hover {
  transform: translateY(-5px);
  background: linear-gradient(180deg, rgba(15, 23, 42, 0.76), rgba(15, 23, 42, 0.56));
  border-color: rgba(125, 211, 252, 0.36);
  box-shadow: 0 18px 50px rgba(8, 47, 73, 0.28), inset 0 1px 0 rgba(255, 255, 255, 0.07);
}

.signal-card::before {
  content: '';
  position: absolute;
  top: 0;
  right: 18px;
  left: 18px;
  height: 1px;
  background: linear-gradient(90deg, rgba(96, 165, 250, 0), rgba(96, 165, 250, 0.85), rgba(34, 211, 238, 0));
}

.signal-index {
  color: #60a5fa;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.08em;
}

.signal-value {
  color: #f8fbff;
  font-size: 17px;
  font-weight: 800;
}

.signal-title {
  color: #9fb0c7;
  font-size: 12px;
  line-height: 1.55;
}

.hero-footnote {
  max-width: 600px;
  margin-top: 16px;
  color: #7890ad;
  font-size: 13px;
  line-height: 1.7;
}

.login-card {
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

  .hero-title-row {
    align-items: center;
  }

  .brand-icon {
    width: 50px;
    height: 50px;
    margin-top: 0;
    border-radius: 16px;
  }

  .signal-grid {
    grid-template-columns: 1fr;
  }

  .hero-command-panel {
    grid-template-columns: 1fr;
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

  .hero-panel,
  .login-orb,
  .login-mouse-glow,
  .login-scanline,
  .login-data-streams {
    display: none;
  }

  .login-bg-grid {
    background:
      linear-gradient(rgba(148, 163, 184, 0.06) 1px, transparent 1px),
      linear-gradient(90deg, rgba(148, 163, 184, 0.06) 1px, transparent 1px),
      radial-gradient(circle at 50% 18%, rgba(37, 99, 235, 0.18), transparent 44%);
    background-size: 36px 36px, 36px 36px, auto;
    animation: none;
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

@keyframes gridDrift {
  from {
    background-position: 0 0, 0 0, center, center;
  }
  to {
    background-position: 42px 42px, 42px 42px, center, center;
  }
}

@keyframes orbFloat {
  0%, 100% {
    transform: translate3d(0, 0, 0) scale(1);
  }
  50% {
    transform: translate3d(18px, -16px, 0) scale(1.08);
  }
}

@keyframes scanlineMove {
  from {
    transform: translateY(-120%);
  }
  to {
    transform: translateY(120%);
  }
}

@keyframes streamFall {
  from {
    transform: translateY(-28vh);
  }
  to {
    transform: translateY(128vh);
  }
}

@keyframes statusPulse {
  0%, 100% {
    opacity: 0.72;
    transform: scale(1);
  }
  50% {
    opacity: 1;
    transform: scale(1.35);
  }
}

@keyframes panelSweep {
  from {
    transform: translateX(-55%);
  }
  to {
    transform: translateX(55%);
  }
}

@keyframes nodeBreath {
  0%, 100% {
    border-color: rgba(147, 197, 253, 0.24);
    box-shadow: 0 0 20px rgba(37, 99, 235, 0.14);
  }
  50% {
    border-color: rgba(125, 211, 252, 0.5);
    box-shadow: 0 0 30px rgba(56, 189, 248, 0.28);
  }
}

@keyframes nodeCore {
  0%, 100% {
    opacity: 0.72;
    transform: scale(0.9);
  }
  50% {
    opacity: 1;
    transform: scale(1.15);
  }
}

@keyframes glowPulse {
  0%, 100% {
    opacity: 0.6;
    transform: scale(1);
  }
  50% {
    opacity: 1;
    transform: scale(1.18);
  }
}

@keyframes iconFloat {
  0%, 100% {
    transform: translateY(0);
  }
  50% {
    transform: translateY(-3px);
  }
}

@media (prefers-reduced-motion: reduce) {
  .login-bg-grid,
  .login-orb,
  .login-scanline,
  .login-data-streams span,
  .hero-status span,
  .command-grid span,
  .command-grid span::after,
  .command-glow,
  .brand-icon {
    animation: none;
  }
}
</style>
