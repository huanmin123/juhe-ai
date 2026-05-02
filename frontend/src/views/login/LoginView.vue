<template>
  <div class="login-page">
    <div class="login-bg-grid" />
    <div class="login-orb login-orb-blue" />
    <div class="login-orb login-orb-cyan" />

    <section class="hero-panel">
      <div class="hero-badge">{{ loginBadge }}</div>
      <div class="brand-lockup">
        <img class="brand-icon" :src="appBrand.appIcon" :alt="`${appBrand.appName} 图标`" />
        <span>{{ appBrand.appName }}</span>
      </div>
      <h1>{{ loginTitle }}</h1>
      <p>{{ loginSubtitle }}</p>
      <div class="hero-divider">
        <span />
        <strong>AI GATEWAY PLATFORM</strong>
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
          <p>默认管理员账号为 admin / admin，首次登录后请修改密码。</p>
        </div>
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
import { message } from 'ant-design-vue'
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { login } from '@/composables/useAuth'
import { appBrand, loadGlobalBrandSettings } from '@/composables/useAppBrand'
import type { GlobalSettings } from '@/types/domain'

const router = useRouter()
const route = useRoute()
const loading = ref(false)
const globalSettings = ref<GlobalSettings>({})
const form = reactive({ username: 'admin', password: 'admin' })

const loginTitle = computed(() => stringValue(globalSettings.value.loginTitle, `${appBrand.appName} 管理平台`))
const loginSubtitle = computed(() => stringValue(globalSettings.value.loginSubtitle, '统一接入、统一调度、统一可观测。'))
const loginBadge = computed(() => stringValue(globalSettings.value.loginBadge, '统一接入平台'))

const signals = [
  { index: '01', value: '多厂商接入', title: '统一纳管模型供应商与上游账号' },
  { index: '02', value: '智能调度', title: '围绕分组、密钥和策略完成路由' },
  { index: '03', value: '安全隔离', title: '系统账户、配置和记录独立成域' }
]

async function handleLogin() {
  if (!form.username.trim() || !form.password) {
    message.warning('请输入账号和密码')
    return
  }
  loading.value = true
  try {
    const user = await login({ username: form.username.trim(), password: form.password })
    if (user.mustChangePassword) {
      message.warning('当前使用初始密码，请尽快在右上角修改密码')
    }
    await router.replace(typeof route.query.redirect === 'string' ? route.query.redirect : '/accounts')
  } catch (error) {
    console.error(error)
    message.error('登录失败，请检查账号或密码')
  } finally {
    loading.value = false
  }
}

function stringValue(value: unknown, fallback: string): string {
  return typeof value === 'string' && value.trim() ? value.trim() : fallback
}

onMounted(async () => {
  try {
    globalSettings.value = await loadGlobalBrandSettings()
  } catch (error) {
    console.error(error)
  }
})
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
}

.login-orb-cyan {
  bottom: 8%;
  left: 9%;
  background: radial-gradient(circle, rgba(45, 212, 191, 0.56), transparent 68%);
}

.hero-panel,
.login-card {
  position: relative;
  z-index: 1;
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
}

.brand-lockup {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 30px;
  color: #f8fafc;
  font-size: 18px;
  font-weight: 800;
}

.brand-icon {
  width: 38px;
  height: 38px;
  padding: 7px;
  background: rgba(255, 255, 255, 0.94);
  border-radius: 12px;
  box-shadow: 0 0 28px rgba(59, 130, 246, 0.42);
}

.hero-panel h1 {
  max-width: 680px;
  margin: 22px 0 14px;
  color: #fff;
  font-size: clamp(40px, 5vw, 68px);
  font-weight: 900;
  line-height: 1.02;
  letter-spacing: -0.04em;
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
  margin-top: 34px;
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
  margin: 0;
  color: #0f172a;
  font-size: 24px;
  font-weight: 900;
}

.login-card-heading p {
  margin: 8px 0 24px;
  color: #64748b;
  line-height: 1.6;
}

@media (max-width: 980px) {
  .login-page {
    grid-template-columns: 1fr;
    padding: 32px 18px;
  }

  .signal-grid {
    grid-template-columns: 1fr;
  }

  .login-card {
    width: 100%;
  }
}
</style>
