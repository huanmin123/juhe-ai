<template>
  <a-card class="page-card settings-page-card" title="系统设置">
    <div class="settings-shell">
      <div class="settings-hero">
        <div>
          <div class="hero-title">轻量默认策略</div>
          <div class="hero-subtitle">这些设置只作为本地网关和账户调度的默认策略，不覆盖账号编辑里的显式配置。</div>
        </div>
        <a-tag color="blue" class="hero-tag">SQLite 本地持久化</a-tag>
      </div>

      <a-alert class="setting-alert" type="info" show-icon>
        <template #message>流熔断默认关闭；启用后只在流式响应长时间无数据或异常中断时累计失败次数。</template>
      </a-alert>

      <a-form layout="vertical" class="settings-form">
        <section class="settings-section">
          <div class="section-heading">
            <div>
              <h3>基础默认值</h3>
              <p>用于新建 OpenAI 账户时的默认基础配置。</p>
            </div>
          </div>
          <div class="settings-grid">
            <div class="setting-item setting-item-wide">
              <a-form-item label="默认 OpenAI Base URL" extra="只作为新账户默认值，已有账户按自己的 Base URL 转发。">
                <a-input v-model:value="form.defaultOpenAIBaseUrl" placeholder="https://api.openai.com/v1" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="默认账号并发上限" extra="第一期先保存配置值，后续并发调度按账号级配置执行。">
                <a-input-number v-model:value="form.defaultAccountConcurrencyLimit" :min="1" :max="999" style="width: 100%" />
              </a-form-item>
            </div>
          </div>
        </section>

        <section class="settings-section">
          <div class="section-heading">
            <div>
              <h3>流熔断与账号处理</h3>
              <p>参考 sub2api 的流超时处理语义，轻量版只做本地计数、临时冷却和标记错误。</p>
            </div>
            <a-switch v-model:checked="form.streamCircuitBreakerEnabled" checked-children="启用" un-checked-children="关闭" />
          </div>

          <div class="settings-grid">
            <div class="setting-item">
              <a-form-item label="流空闲超时（秒）" extra="流式响应超过该时间没有新数据，会记录一次流失败。">
                <a-input-number v-model:value="form.streamIdleTimeoutSeconds" :min="10" :max="3600" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="失败后账号处理" extra="达到阈值后执行：临时冷却、标记错误，或仅记录失败。">
                <a-select v-model:value="form.streamFailureAction" :options="streamActionOptions" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="阈值次数" extra="同一账号在统计窗口内累计到该次数后触发处理。">
                <a-input-number v-model:value="form.streamFailureThresholdCount" :min="1" :max="100" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="统计窗口（分钟）" extra="超过窗口后失败次数重新计算。">
                <a-input-number v-model:value="form.streamFailureThresholdWindowMinutes" :min="1" :max="1440" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="账号冷却时长（分钟）" extra="处理方式为临时冷却时，账号在冷却结束前不参与调度。">
                <a-input-number v-model:value="form.streamAccountCooldownMinutes" :min="1" :max="1440" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item setting-switch-item">
              <a-form-item label="上游过载自动冷却" extra="上游返回 429/503 时，可临时冷却该账号并尝试下一个账号。">
                <a-switch v-model:checked="form.overloadCooldownEnabled" checked-children="启用" un-checked-children="关闭" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="过载冷却时长（分钟）" extra="用于 429/503 这类过载或限流响应。">
                <a-input-number v-model:value="form.overloadCooldownMinutes" :min="1" :max="1440" style="width: 100%" />
              </a-form-item>
            </div>
          </div>
        </section>

        <div class="settings-actions">
          <a-space>
            <a-button type="primary" :loading="saving" @click="saveSettings">保存设置</a-button>
            <a-button :disabled="saving" @click="resetDefaults">还原推荐默认值</a-button>
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
import type { SystemSettings } from '@/types/domain'

type StreamFailureAction = NonNullable<SystemSettings['streamFailureAction']>

interface SettingsForm {
  defaultOpenAIBaseUrl: string
  defaultAccountConcurrencyLimit: number
  streamCircuitBreakerEnabled: boolean
  streamIdleTimeoutSeconds: number
  streamFailureAction: StreamFailureAction
  streamAccountCooldownMinutes: number
  streamFailureThresholdCount: number
  streamFailureThresholdWindowMinutes: number
  overloadCooldownEnabled: boolean
  overloadCooldownMinutes: number
}

const defaultSettings: SettingsForm = {
  defaultOpenAIBaseUrl: 'https://api.openai.com/v1',
  defaultAccountConcurrencyLimit: 3,
  streamCircuitBreakerEnabled: false,
  streamIdleTimeoutSeconds: 180,
  streamFailureAction: 'cooldown',
  streamAccountCooldownMinutes: 5,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 10,
  overloadCooldownEnabled: true,
  overloadCooldownMinutes: 10
}

const streamActionOptions = [
  { label: '临时冷却账号', value: 'cooldown' },
  { label: '标记错误并停调度', value: 'disable' },
  { label: '只记录失败', value: 'none' }
]

const saving = ref(false)
const form = reactive<SettingsForm>({ ...defaultSettings })

async function loadSettings() {
  try {
    const settings = await api.settings.get()
    Object.assign(form, normalizeSettings(settings))
  } catch (error) {
    console.error(error)
    message.error('加载系统设置失败')
  }
}

async function saveSettings() {
  saving.value = true
  try {
    const next = await api.settings.update({ ...normalizeSettings(form) })
    Object.assign(form, normalizeSettings(next))
    message.success('系统设置已保存')
  } catch (error) {
    console.error(error)
    message.error('保存系统设置失败')
  } finally {
    saving.value = false
  }
}

function resetDefaults() {
  Object.assign(form, defaultSettings)
}

function normalizeSettings(settings: SystemSettings | SettingsForm): SettingsForm {
  return {
    defaultOpenAIBaseUrl: stringValue(settings.defaultOpenAIBaseUrl, defaultSettings.defaultOpenAIBaseUrl),
    defaultAccountConcurrencyLimit: numberValue(settings.defaultAccountConcurrencyLimit, defaultSettings.defaultAccountConcurrencyLimit, 1, 999),
    streamCircuitBreakerEnabled: booleanValue(settings.streamCircuitBreakerEnabled, defaultSettings.streamCircuitBreakerEnabled),
    streamIdleTimeoutSeconds: numberValue(settings.streamIdleTimeoutSeconds, defaultSettings.streamIdleTimeoutSeconds, 10, 3600),
    streamFailureAction: actionValue(settings.streamFailureAction),
    streamAccountCooldownMinutes: numberValue(settings.streamAccountCooldownMinutes, defaultSettings.streamAccountCooldownMinutes, 1, 1440),
    streamFailureThresholdCount: numberValue(settings.streamFailureThresholdCount, defaultSettings.streamFailureThresholdCount, 1, 100),
    streamFailureThresholdWindowMinutes: numberValue(settings.streamFailureThresholdWindowMinutes, defaultSettings.streamFailureThresholdWindowMinutes, 1, 1440),
    overloadCooldownEnabled: booleanValue(settings.overloadCooldownEnabled, defaultSettings.overloadCooldownEnabled),
    overloadCooldownMinutes: numberValue(settings.overloadCooldownMinutes, defaultSettings.overloadCooldownMinutes, 1, 1440)
  }
}

function stringValue(value: unknown, fallback: string): string {
  return typeof value === 'string' && value.trim() ? value.trim() : fallback
}

function numberValue(value: unknown, fallback: number, min: number, max: number): number {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  if (!Number.isFinite(number)) return fallback
  return Math.min(Math.max(Math.trunc(number), min), max)
}

function booleanValue(value: unknown, fallback: boolean): boolean {
  return typeof value === 'boolean' ? value : fallback
}

function actionValue(value: unknown): StreamFailureAction {
  return value === 'disable' || value === 'none' || value === 'cooldown' ? value : defaultSettings.streamFailureAction
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

.settings-hero {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 18px;
  padding: 18px 20px;
  background: linear-gradient(135deg, rgba(22, 119, 255, 0.10) 0%, rgba(14, 165, 233, 0.06) 100%);
  border: 1px solid #dbeafe;
  border-radius: 16px;
}

.hero-title {
  color: #0f172a;
  font-size: 18px;
  font-weight: 800;
  line-height: 28px;
}

.hero-subtitle {
  margin-top: 4px;
  color: #475569;
  font-size: 13px;
  line-height: 22px;
}

.hero-tag {
  margin-inline-end: 0;
  border-radius: 999px;
  font-weight: 700;
}

.setting-alert {
  border-radius: 12px;
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

  .settings-hero,
  .section-heading {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
