<template>
  <a-card class="page-card settings-page-card">
    <div class="settings-shell">
      <a-form v-if="isAdmin" layout="vertical" class="settings-form">
        <section class="settings-section global-section">
          <div class="section-heading">
            <div>
              <h3>全局展示配置</h3>
              <p>只管理系统名称和系统图标路径；登录页文案与样式按设计固定。</p>
            </div>
            <div class="global-preview-stack">
              <div class="brand-preview">
                <img class="brand-preview-icon" :src="globalForm.appIcon" :alt="`${globalForm.appName} 图标`" />
                <span>{{ globalForm.appName }}</span>
              </div>
            </div>
          </div>

          <a-alert class="setting-alert section-alert" type="info" show-icon>
            <template #message>全局配置仅包含系统名称和系统图标路径，仅管理员可修改；普通用户不会看到这些字段。</template>
          </a-alert>

          <div class="settings-grid">
            <div class="setting-item">
              <a-form-item label="系统名称" extra="保存后同步显示到左侧菜单标题、浏览器 tab，并用于登录页“系统名称 + 管理平台”标题。">
                <a-input v-model:value="globalForm.appName" placeholder="请输入系统名称" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="系统图标路径" extra="默认使用 /brand-icon.svg；也可上传 256KB 以内图片并以 Data URL 保存。">
                <a-input v-model:value="globalForm.appIcon" placeholder="/brand-icon.svg" />
                <a-space class="brand-icon-actions">
                  <a-upload accept="image/svg+xml,image/png,image/jpeg,image/webp" :before-upload="handleIconUpload" :show-upload-list="false">
                    <a-button>上传图标</a-button>
                  </a-upload>
                  <a-button type="link" @click="restoreDefaultIcon">恢复默认图标</a-button>
                </a-space>
              </a-form-item>
            </div>
          </div>

          <div class="settings-actions">
            <a-space>
              <a-button type="primary" :loading="savingGlobal" @click="saveGlobalSettings">保存全局配置</a-button>
              <a-button :disabled="savingGlobal" @click="resetGlobalDefaults">恢复默认配置</a-button>
            </a-space>
          </div>
        </section>
      </a-form>

      <a-form layout="vertical" class="settings-form">
        <section class="settings-section">
          <div class="section-heading">
            <div>
              <h3>账户调度默认值</h3>
              <p>这些配置按当前系统账户隔离保存；管理员编辑的是自己的默认值，不会覆盖其他用户。</p>
            </div>
          </div>
          <div class="settings-grid">
            <div class="setting-item">
              <a-form-item label="默认账号并发上限" extra="第一期先保存配置值，后续并发调度按账号级配置执行。">
                <a-input-number v-model:value="systemForm.defaultAccountConcurrencyLimit" :min="1" :max="999" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="临时不可调用暂停时长（分钟）" extra="未知异常、策略冷却和流熔断都会使用这个用户级默认时长。">
                <a-input-number v-model:value="systemForm.defaultTemporaryUnschedulableMinutes" :min="1" :max="1440" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="临时状态重试间隔（秒）" extra="标记临时不可调用前，每次短暂重试之间等待多久。">
                <a-input-number v-model:value="systemForm.temporaryUnschedulableRetryIntervalSeconds" :min="0" :max="3600" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="临时状态重试次数" extra="默认失败后重试 3 次，仍失败才进入临时不可调用。">
                <a-input-number v-model:value="systemForm.temporaryUnschedulableRetryAttempts" :min="0" :max="10" style="width: 100%" />
              </a-form-item>
            </div>
          </div>
        </section>

        <section class="settings-section">
          <div class="section-heading">
            <div>
              <h3>流式超时与换号</h3>
              <p>分别控制“首包前等多久换账号”和“输出中停多久算中断”；累计失败达到阈值后，账号会临时不可调用。</p>
            </div>
            <a-switch v-model:checked="systemForm.streamCircuitBreakerEnabled" checked-children="启用" un-checked-children="关闭" />
          </div>

          <a-alert class="setting-alert section-alert" type="info" show-icon>
            <template #message>流式请求如果首包等待过久会自动换账号重试；已经开始输出后长时间没有新数据，则记录为流式中断。</template>
          </a-alert>

          <div class="settings-grid">
            <div class="setting-item">
              <a-form-item label="首包等待上限（秒）" extra="发起流式请求后，超过这个时间还没有收到第一段内容，就中断当前账号并换下一个账号重试。">
                <a-input-number v-model:value="systemForm.streamRequestTimeoutSeconds" :min="10" :max="3600" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="输出停顿上限（秒）" extra="已经收到第一段内容后，如果连续这么久没有新内容，就认为本次流式响应中断。默认 30 秒。">
                <a-input-number v-model:value="systemForm.streamIdleTimeoutSeconds" :min="1" :max="3600" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="失败触发次数" extra="同一账号在统计窗口内累计到这个失败次数后，进入临时不可调用。">
                <a-input-number v-model:value="systemForm.streamFailureThresholdCount" :min="1" :max="100" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="失败统计窗口（分钟）" extra="只统计这个时间窗口内的流式失败；超过窗口后重新计数。">
                <a-input-number v-model:value="systemForm.streamFailureThresholdWindowMinutes" :min="1" :max="1440" style="width: 100%" />
              </a-form-item>
            </div>
          </div>
        </section>

        <div class="settings-actions">
          <a-space>
            <a-button type="primary" :loading="savingSystem" @click="saveSystemSettings">保存系统设置</a-button>
            <a-button :disabled="savingSystem" @click="resetSystemDefaults">恢复默认配置</a-button>
          </a-space>
        </div>
      </a-form>
    </div>
  </a-card>
</template>

<script setup lang="ts">
import { message } from 'ant-design-vue'
import { onMounted, reactive, ref } from 'vue'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { applyAppBrand, defaultAppBrand } from '@/composables/useAppBrand'
import type { GlobalSettings, SystemSettings } from '@/types/domain'

interface GlobalForm {
  appName: string
  appIcon: string
}

interface SystemForm {
  defaultAccountConcurrencyLimit: number
  defaultTemporaryUnschedulableMinutes: number
  temporaryUnschedulableRetryIntervalSeconds: number
  temporaryUnschedulableRetryAttempts: number
  streamCircuitBreakerEnabled: boolean
  streamRequestTimeoutSeconds: number
  streamIdleTimeoutSeconds: number
  streamFailureThresholdCount: number
  streamFailureThresholdWindowMinutes: number
}

const defaultGlobalSettings: GlobalForm = {
  appName: defaultAppBrand.appName,
  appIcon: defaultAppBrand.appIcon
}

const defaultSystemSettings: SystemForm = {
  defaultAccountConcurrencyLimit: 3,
  defaultTemporaryUnschedulableMinutes: 5,
  temporaryUnschedulableRetryIntervalSeconds: 3,
  temporaryUnschedulableRetryAttempts: 3,
  streamCircuitBreakerEnabled: true,
  streamRequestTimeoutSeconds: 180,
  streamIdleTimeoutSeconds: 30,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 10
}

const isAdmin = authState.isAdmin
const savingGlobal = ref(false)
const savingSystem = ref(false)
const globalForm = reactive<GlobalForm>({ ...defaultGlobalSettings })
const systemForm = reactive<SystemForm>({ ...defaultSystemSettings })

async function loadSettings() {
  try {
    const [systemSettings, globalSettings] = await Promise.all([
      api.settings.get(),
      isAdmin.value ? api.settings.global() : api.settings.public()
    ])
    Object.assign(systemForm, normalizeSystemSettings(systemSettings))
    Object.assign(globalForm, normalizeGlobalSettings(globalSettings))
    applyAppBrand(globalSettings)
  } catch (error) {
    console.error(error)
    message.error('加载系统设置失败')
  }
}

async function saveGlobalSettings() {
  savingGlobal.value = true
  try {
    const next = await api.settings.updateGlobal({ ...normalizeGlobalSettings(globalForm) })
    Object.assign(globalForm, normalizeGlobalSettings(next))
    applyAppBrand(next)
    message.success('全局配置已保存')
  } catch (error) {
    console.error(error)
    message.error('保存全局配置失败')
  } finally {
    savingGlobal.value = false
  }
}

async function saveSystemSettings() {
  savingSystem.value = true
  try {
    const next = await api.settings.update({ ...normalizeSystemSettings(systemForm) })
    Object.assign(systemForm, normalizeSystemSettings(next))
    message.success('系统设置已保存')
  } catch (error) {
    console.error(error)
    message.error('保存系统设置失败')
  } finally {
    savingSystem.value = false
  }
}

function resetGlobalDefaults() {
  Object.assign(globalForm, defaultGlobalSettings)
}

function resetSystemDefaults() {
  Object.assign(systemForm, defaultSystemSettings)
}

function restoreDefaultIcon() {
  globalForm.appIcon = defaultGlobalSettings.appIcon
}

function handleIconUpload(file: File): boolean {
  if (!file.type.startsWith('image/')) {
    message.warning('请上传图片格式的图标')
    return false
  }
  if (file.size > 256 * 1024) {
    message.warning('图标文件不能超过 256KB')
    return false
  }

  const reader = new FileReader()
  reader.onload = () => {
    if (typeof reader.result === 'string') {
      globalForm.appIcon = reader.result
      message.success('图标已读取，保存全局配置后生效')
    }
  }
  reader.onerror = () => {
    message.error('读取图标失败')
  }
  reader.readAsDataURL(file)
  return false
}

function normalizeGlobalSettings(settings: GlobalSettings | GlobalForm): GlobalForm {
  return {
    appName: stringValue(settings.appName, defaultGlobalSettings.appName),
    appIcon: stringValue(settings.appIcon, defaultGlobalSettings.appIcon)
  }
}

function normalizeSystemSettings(settings: SystemSettings | SystemForm): SystemForm {
  return {
    defaultAccountConcurrencyLimit: numberValue(settings.defaultAccountConcurrencyLimit, defaultSystemSettings.defaultAccountConcurrencyLimit, 1, 999),
    defaultTemporaryUnschedulableMinutes: numberValue(settings.defaultTemporaryUnschedulableMinutes, defaultSystemSettings.defaultTemporaryUnschedulableMinutes, 1, 1440),
    temporaryUnschedulableRetryIntervalSeconds: numberValue(settings.temporaryUnschedulableRetryIntervalSeconds, defaultSystemSettings.temporaryUnschedulableRetryIntervalSeconds, 0, 3600),
    temporaryUnschedulableRetryAttempts: numberValue(settings.temporaryUnschedulableRetryAttempts, defaultSystemSettings.temporaryUnschedulableRetryAttempts, 0, 10),
    streamCircuitBreakerEnabled: booleanValue(settings.streamCircuitBreakerEnabled, defaultSystemSettings.streamCircuitBreakerEnabled),
    streamRequestTimeoutSeconds: numberValue(settings.streamRequestTimeoutSeconds, defaultSystemSettings.streamRequestTimeoutSeconds, 10, 3600),
    streamIdleTimeoutSeconds: numberValue(settings.streamIdleTimeoutSeconds, defaultSystemSettings.streamIdleTimeoutSeconds, 1, 3600),
    streamFailureThresholdCount: numberValue(settings.streamFailureThresholdCount, defaultSystemSettings.streamFailureThresholdCount, 1, 100),
    streamFailureThresholdWindowMinutes: numberValue(settings.streamFailureThresholdWindowMinutes, defaultSystemSettings.streamFailureThresholdWindowMinutes, 1, 1440)
  }
}

function numberValue(value: unknown, fallback: number, min: number, max: number): number {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  if (!Number.isFinite(number)) return fallback
  return Math.min(Math.max(Math.trunc(number), min), max)
}

function booleanValue(value: unknown, fallback: boolean): boolean {
  return typeof value === 'boolean' ? value : fallback
}

function stringValue(value: unknown, fallback: string): string {
  return typeof value === 'string' && value.trim() ? value.trim() : fallback
}

onMounted(loadSettings)
</script>

<style scoped>
.settings-page-card {
  margin-top: 4px;
}

.settings-shell {
  display: flex;
  flex-direction: column;
  gap: 18px;
}

.setting-alert {
  border-radius: 12px;
}

.section-alert {
  margin-bottom: 16px;
}

.settings-form {
  max-width: 1120px;
}

.settings-section {
  padding: 20px;
  background: #fff;
  border: 1px solid #edf1f7;
  border-radius: 16px;
}

.global-section {
  background:
    radial-gradient(circle at top right, rgba(22, 119, 255, 0.08), transparent 34%),
    #fff;
}

.settings-section + .settings-section {
  margin-top: 18px;
}

.section-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 18px;
}

.section-heading h3 {
  margin: 0;
  color: #0f172a;
  font-size: 16px;
  font-weight: 800;
}

.section-heading p {
  margin: 6px 0 0;
  color: #64748b;
  font-size: 13px;
  line-height: 20px;
}

.global-preview-stack {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  justify-content: flex-end;
}

.brand-preview,
.login-preview {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  max-width: 280px;
  padding: 10px 14px;
  color: #0f172a;
  background: rgba(248, 250, 252, 0.88);
  border: 1px solid #edf1f7;
  border-radius: 12px;
}

.brand-preview {
  font-weight: 700;
}

.login-preview {
  flex-direction: column;
  align-items: flex-start;
  gap: 3px;
}

.login-preview span {
  color: #1677ff;
  font-size: 11px;
  font-weight: 800;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.login-preview strong {
  font-size: 13px;
}

.brand-preview-icon {
  width: 28px;
  height: 28px;
  flex: 0 0 auto;
}

.brand-icon-actions {
  margin-top: 10px;
}

.settings-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
}

.setting-item {
  min-height: 118px;
  padding: 16px 16px 0;
  background: #f8fafc;
  border: 1px solid #edf1f7;
  border-radius: 14px;
}

.setting-item-wide {
  grid-column: 1 / -1;
}

.setting-switch-item {
  display: flex;
  align-items: center;
}

.settings-actions {
  display: flex;
  justify-content: flex-end;
  margin-top: 18px;
}

:deep(.ant-form-item-extra) {
  color: #94a3b8;
  font-size: 12px;
  line-height: 18px;
}

@media (max-width: 900px) {
  .settings-grid {
    grid-template-columns: 1fr;
  }

  .section-heading {
    align-items: flex-start;
    flex-direction: column;
  }

  .global-preview-stack {
    justify-content: flex-start;
  }
}
</style>
