<template>
  <a-card class="page-card settings-page-card">
    <div class="settings-shell">
      <a-form layout="vertical" class="settings-form">
        <section class="settings-section global-section">
          <div class="section-heading">
            <div>
              <h3 class="section-title">
                <span>全局展示配置</span>
                <a-tooltip title="只管理系统名称和系统图标路径；登录页文案与样式按设计固定。仅管理角色可修改，普通用户不会看到这些字段。">
                  <QuestionCircleOutlined class="help-icon" />
                </a-tooltip>
              </h3>
            </div>
            <div class="global-preview-stack">
              <div class="brand-preview">
                <img class="brand-preview-icon" :src="globalForm.appIcon" :alt="`${globalForm.appName} 图标`" />
                <span>{{ globalForm.appName }}</span>
              </div>
            </div>
          </div>

          <div class="settings-grid">
            <div class="setting-item">
              <a-form-item label="系统名称" tooltip="保存后显示到左侧菜单标题、浏览器 tab，并用于登录页“系统名称 + 管理平台”标题。">
                <a-input v-model:value="globalForm.appName" placeholder="请输入系统名称" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="系统图标路径" tooltip="默认使用 /__aisys__/brand-icon.svg；也可上传 256KB 以内图片并以 Data URL 保存。">
                <a-input v-model:value="globalForm.appIcon" placeholder="/__aisys__/brand-icon.svg" />
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
              <h3 class="section-title">
                <span>网关请求体限制</span>
                <a-tooltip title="控制文本请求进入上游前可承载的最大上下文体积，保存后会随运行时缓存刷新生效。图像生成请求和网关入口硬保护仍保留 64MB 上限。">
                  <QuestionCircleOutlined class="help-icon" />
                </a-tooltip>
              </h3>
            </div>
          </div>

          <div class="settings-grid">
            <div class="setting-item">
              <a-form-item label="文本请求体上限（MB）" tooltip="可设置 1 到 64；调大可承载更长上下文，也会增加单请求内存压力。">
                <a-input-number v-model:value="systemForm.gatewayTextRawBodyLimitMegabytes" :min="1" :max="64" style="width: 100%" />
              </a-form-item>
            </div>
          </div>
        </section>

        <section class="settings-section">
          <div class="section-heading">
            <div>
              <h3 class="section-title">
                <span>正常账号健康检测</span>
                <a-tooltip title="只检测长期没有真实成功请求的正常账号；真实请求成功会顺延下次检测，后台分批探测到期账号。">
                  <QuestionCircleOutlined class="help-icon" />
                </a-tooltip>
              </h3>
            </div>
          </div>

          <div class="settings-grid">
            <div class="setting-item">
              <a-form-item label="检测间隔（小时）" tooltip="默认 12 小时；账号近期已有真实成功请求时不再额外探测。">
                <a-input-number v-model:value="systemForm.accountHealthCheckIntervalHours" :min="1" :max="168" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="错峰窗口（分钟）" tooltip="默认 120 分钟；按账号 ID 稳定错峰，避免大量账号同时探测。">
                <a-input-number v-model:value="systemForm.accountHealthCheckJitterMinutes" :min="0" :max="1440" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="单轮账号数" tooltip="默认 20；运维 worker 每轮只拉取到期账号，避免一次性全量扫描和集中打上游。">
                <a-input-number v-model:value="systemForm.accountHealthCheckBatchSize" :min="1" :max="100" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="连续失败阈值" tooltip="默认 3 次；达到阈值后才允许进入临时不可调用处理，降低网络抖动误杀。">
                <a-input-number v-model:value="systemForm.accountHealthCheckFailureThreshold" :min="1" :max="10" style="width: 100%" />
              </a-form-item>
            </div>
          </div>
        </section>

        <section class="settings-section">
          <div class="section-heading">
            <div>
              <h3 class="section-title">
                <span>后台接口限流</span>
                <a-tooltip title="保护 /__aisys__/api 后台接口，避免同一来源或同一登录用户在短时间内压垮 DB service；健康检查不受影响。">
                  <QuestionCircleOutlined class="help-icon" />
                </a-tooltip>
              </h3>
            </div>
            <a-switch v-model:checked="systemForm.systemApiRateLimitEnabled" checked-children="启用" un-checked-children="关闭" />
          </div>

          <div class="settings-grid">
            <div class="setting-item">
              <a-form-item label="IP 读请求每分钟" tooltip="默认 600；适用于 GET、HEAD 和 OPTIONS。">
                <a-input-number v-model:value="systemForm.systemApiRateLimitIpReadPerMinute" :disabled="!systemForm.systemApiRateLimitEnabled" :min="0" :max="1000000" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="IP 读请求 10 秒突发" tooltip="默认 120；用于拦截短时间刷新列表和探测接口。">
                <a-input-number v-model:value="systemForm.systemApiRateLimitIpReadBurstPer10Seconds" :disabled="!systemForm.systemApiRateLimitEnabled" :min="0" :max="1000000" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="IP 写请求每分钟" tooltip="默认 180；适用于 POST、PATCH、PUT 和 DELETE。">
                <a-input-number v-model:value="systemForm.systemApiRateLimitIpWritePerMinute" :disabled="!systemForm.systemApiRateLimitEnabled" :min="0" :max="1000000" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="IP 写请求 10 秒突发" tooltip="默认 40；优先挡住批量提交和暴力探测。">
                <a-input-number v-model:value="systemForm.systemApiRateLimitIpWriteBurstPer10Seconds" :disabled="!systemForm.systemApiRateLimitEnabled" :min="0" :max="1000000" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="登录用户读请求每分钟" tooltip="默认 300；同一登录账号的后台读请求保护。">
                <a-input-number v-model:value="systemForm.systemApiRateLimitUserReadPerMinute" :disabled="!systemForm.systemApiRateLimitEnabled" :min="0" :max="1000000" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="登录用户写请求每分钟" tooltip="默认 120；对保存、删除、批量操作等写请求再加一层限制。">
                <a-input-number v-model:value="systemForm.systemApiRateLimitUserWritePerMinute" :disabled="!systemForm.systemApiRateLimitEnabled" :min="0" :max="1000000" style="width: 100%" />
              </a-form-item>
            </div>
          </div>
        </section>

        <section class="settings-section">
          <div class="section-heading">
            <div>
              <h3 class="section-title">
                <span>账号测试</span>
                <a-tooltip title="控制后台账号测试任务的系统级并发防护；单个用户批量测试仍按每批最多 10 个账号提交。">
                  <QuestionCircleOutlined class="help-icon" />
                </a-tooltip>
              </h3>
            </div>
          </div>

          <div class="settings-grid">
            <div class="setting-item">
              <a-form-item label="后台并发上限" tooltip="默认 100；用于防止异常批量或恶意请求把账号测试任务全部同时打到上游。">
                <a-input-number v-model:value="systemForm.accountTestTaskConcurrency" :min="1" :max="1000" style="width: 100%" />
              </a-form-item>
            </div>
          </div>
        </section>

        <section class="settings-section">
          <div class="section-heading">
            <div>
              <h3 class="section-title">
                <span>账户调度默认值</span>
                <a-tooltip title="这些配置是系统级运行策略，保存后会影响网关调度和后台任务。">
                  <QuestionCircleOutlined class="help-icon" />
                </a-tooltip>
              </h3>
            </div>
          </div>
          <div class="settings-grid">
            <div class="setting-item">
              <a-form-item label="临时不可调用最大暂停时间（分钟）" tooltip="默认 2 分钟；账号进入临时不可调用后先走快速恢复通道：3 秒起步，失败后翻倍；单次等待不会超过这个最大暂停时间。">
                <a-input-number v-model:value="systemForm.defaultTemporaryUnschedulableMinutes" :min="1" :max="1440" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="临时状态重试间隔（秒）" tooltip="普通上游失败切号前，同账号原地重试之间等待多久。">
                <a-input-number v-model:value="systemForm.temporaryUnschedulableRetryIntervalSeconds" :min="0" :max="3600" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="临时状态重试次数" tooltip="普通上游失败会先按该次数原地重试当前账号；仍失败才切换账号并记录临时不可调用。">
                <a-input-number v-model:value="systemForm.temporaryUnschedulableRetryAttempts" :min="0" :max="10" style="width: 100%" />
              </a-form-item>
            </div>
          </div>
        </section>

        <section class="settings-section">
          <div class="section-heading">
            <div>
              <h3 class="section-title">
                <span>冷却账户复测</span>
                <a-tooltip title="仅复测临时不可调用和限流中的账户；先快速恢复，再退化到慢速恢复，长期不可用后继续低频复测。">
                  <QuestionCircleOutlined class="help-icon" />
                </a-tooltip>
              </h3>
            </div>
          </div>

          <div class="settings-grid">
            <div class="setting-item">
              <a-form-item label="长期不可用观察阈值（小时）" tooltip="默认 12 小时；从进入临时不可调用或限流中开始计时，超过后不转异常，而是显示为长期不可用。">
                <a-input-number v-model:value="systemForm.cooldownAccountRetestMaxBackoffHours" :min="1" :max="720" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="长期不可用复测间隔（小时）" tooltip="默认 1 小时；账号进入长期不可用后按该间隔继续自动复测，复测成功会恢复正常。">
                <a-input-number v-model:value="systemForm.cooldownAccountRetestLongTermIntervalHours" :min="1" :max="720" style="width: 100%" />
              </a-form-item>
            </div>
          </div>
        </section>

        <section class="settings-section">
          <div class="section-heading">
            <div>
              <h3 class="section-title">
                <span>首包等待与流式中断</span>
                <a-tooltip title="首包等待适用于非流式和流式请求；流式输出停顿和失败计数只作用于 SSE 响应。">
                  <QuestionCircleOutlined class="help-icon" />
                </a-tooltip>
              </h3>
            </div>
            <a-switch v-model:checked="systemForm.streamCircuitBreakerEnabled" checked-children="启用" un-checked-children="关闭" />
          </div>

          <div class="settings-grid">
            <div class="setting-item">
              <a-form-item label="首包等待上限（秒）" tooltip="发起上游请求后，超过这个时间仍未收到上游首个响应或非流式首个字节，就中断当前账号并尝试后续账号。">
                <a-input-number v-model:value="systemForm.streamRequestTimeoutSeconds" :min="10" :max="3600" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="输出停顿上限（秒）" tooltip="只作用于当前这次流式响应：收到首段上游内容后，超过该时间没有任何上游新数据，就发送失败事件并结束本次响应；任意上游 chunk 都会刷新原始数据计时，未形成完整 SSE 事件只记录诊断。">
                <a-input-number v-model:value="systemForm.streamIdleTimeoutSeconds" :min="1" :max="3600" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="客户端总等待时长（秒）" tooltip="限制同一次客户端连接在服务端隐藏切号和重试期间的总等待时间；超过后停止继续隐藏重试并返回失败，避免客户端长期收不到内容后断开。">
                <a-input-number v-model:value="systemForm.streamClientTotalWaitTimeoutSeconds" :min="10" :max="3600" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="单条流最大存活时间（秒）" tooltip="限制单条 SSE 从进入网关到强制收口的最长时间；即使上游持续发送心跳，到达该时间也会直接中断连接，让客户端重试。默认 1800 秒。">
                <a-input-number v-model:value="systemForm.streamMaxLifetimeSeconds" :min="60" :max="86400" style="width: 100%" />
              </a-form-item>
            </div>
          </div>
        </section>

        <section class="settings-section">
          <div class="section-heading">
            <div>
              <h3 class="section-title">
                <span>数据保留与清理</span>
                <a-tooltip title="控制 usage、日志索引和统计缓存的保留清理吞吐，适配高流量部署。">
                  <QuestionCircleOutlined class="help-icon" />
                </a-tooltip>
              </h3>
            </div>
          </div>

          <a-alert class="setting-alert section-alert" type="warning" show-icon>
            <template #message>线上日增几十万记录时，清理应按小批多轮执行；常态保持 1000 行级别单批，积压追赶再临时上调。</template>
          </a-alert>

          <div class="settings-grid">
            <div class="setting-item">
              <a-form-item label="使用记录保留天数" tooltip="默认 30 天，最大 180 天；清理前会等待统计游标处理完成，避免破坏聚合。">
                <a-input-number v-model:value="systemForm.usageRecordRetentionDays" :min="1" :max="180" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="运行日志索引保留天数" tooltip="默认 14 天，最大 90 天；只影响运行日志索引和文件游标清理，不删除原始日志文件。">
                <a-input-number v-model:value="systemForm.runtimeLogIndexRetentionDays" :min="1" :max="90" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="公开接口日志保留天数" tooltip="默认 30 天，最大 365 天；用于公开接口日志表的后台清理。">
                <a-input-number v-model:value="systemForm.publicApiLogRetentionDays" :min="1" :max="365" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="清理间隔（分钟）" tooltip="默认 10 分钟；修改后重启后台 worker 生效。">
                <a-input-number v-model:value="systemForm.dataRetentionCleanupIntervalMinutes" :min="5" :max="1440" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="单批删除行数" tooltip="默认 1000；单批越大，对数据库写入和后台聚合的瞬时压力越明显。">
                <a-input-number v-model:value="systemForm.dataRetentionCleanupBatchSize" :min="100" :max="5000" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="单轮最大批数" tooltip="默认 20；默认每类每轮最多 2 万行，靠周期持续追平。">
                <a-input-number v-model:value="systemForm.dataRetentionCleanupMaxBatchesPerRun" :min="1" :max="100" style="width: 100%" />
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
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { onMounted, reactive, ref } from 'vue'

import { api } from '@/api/client'
import { applyAppBrand } from '@/composables/useAppBrand'
import { extractApiErrorMessage } from '@/shared/apiError'
import {
  defaultGlobalSettings,
  defaultSystemSettings,
  buildGlobalSettingsPayload,
  buildSystemSettingsPayload,
  normalizeGlobalSettings,
  normalizeSystemSettings,
  type GlobalForm,
  type SystemForm
} from './settingsForm'

const savingGlobal = ref(false)
const savingSystem = ref(false)
const globalForm = reactive<GlobalForm>({ ...defaultGlobalSettings })
const systemForm = reactive<SystemForm>({ ...defaultSystemSettings })

async function loadSettings() {
  try {
    const [systemSettings, globalSettings] = await Promise.all([
      api.settings.get(),
      api.settings.global()
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
    const next = await api.settings.updateGlobal(buildGlobalSettingsPayload(globalForm))
    Object.assign(globalForm, normalizeGlobalSettings(next))
    applyAppBrand(next)
    message.success('全局配置已保存')
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存全局配置失败'))
  } finally {
    savingGlobal.value = false
  }
}

async function saveSystemSettings() {
  savingSystem.value = true
  try {
    const next = await api.settings.update(buildSystemSettingsPayload(systemForm))
    Object.assign(systemForm, normalizeSystemSettings(next))
    message.success('系统设置已保存')
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存系统设置失败'))
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

onMounted(() => {
  void loadSettings()
})
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

.section-title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.help-icon {
  color: #94a3b8;
  cursor: help;
  font-size: 14px;
}

.help-icon:hover {
  color: #1677ff;
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

.settings-actions {
  display: flex;
  justify-content: flex-end;
  margin-top: 18px;
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
