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

          <a-skeleton v-if="!sectionReady.brand" active :paragraph="{ rows: 1 }" />
          <div v-else class="settings-grid">
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
              <a-button type="primary" :loading="savingGlobal" :disabled="!sectionReady.brand" @click="saveGlobalSettings">保存全局配置</a-button>
              <a-button :disabled="savingGlobal || !sectionReady.brand" @click="resetGlobalDefaults">恢复默认配置</a-button>
            </a-space>
          </div>
        </section>
      </a-form>

      <a-form layout="vertical" class="settings-form">
        <section class="settings-section" :ref="(element) => setLazySectionElement(element, 'gateway-core')">
          <div class="section-heading">
            <div>
              <h3 class="section-title">
                <span>网关请求与电路</span>
                <a-tooltip title="控制文本请求体上限和账户传输电路的独立确认阈值，保存后随运行时缓存刷新生效。">
                  <QuestionCircleOutlined class="help-icon" />
                </a-tooltip>
              </h3>
            </div>
          </div>

          <a-skeleton v-if="!sectionReady['gateway-core']" active :paragraph="{ rows: 1 }" />
          <div v-else class="settings-grid">
            <div class="setting-item">
              <a-form-item label="文本请求体上限（MB）" tooltip="可设置 1 到 64；调大可承载更长上下文，也会增加单请求内存压力。">
                <a-input-number v-model:value="systemForm.gatewayTextRawBodyLimitMegabytes" :min="1" :max="64" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="电路独立确认失败次数" tooltip="首次 transport 失败进入待确认；默认还需 2 个不同请求的独立失败证据才熔断，完整 HTTP 响应不计失败。">
                <a-input-number v-model:value="systemForm.accountCircuitConfirmationFailuresRequired" :min="1" :max="5" style="width: 100%" />
              </a-form-item>
            </div>
          </div>
        </section>

        <section class="settings-section request-limit-section" :ref="(element) => setLazySectionElement(element, 'user-request-limit')">
          <div class="section-heading">
            <div>
              <h3 class="section-title">
                <span>用户限制</span>
                <a-tooltip title="设置系统账户的全局请求和 AI 账户数量限制；各用户可以在系统账户编辑中单独覆盖。">
                  <QuestionCircleOutlined class="help-icon" />
                </a-tooltip>
              </h3>
              <p class="section-description">全局默认值，可在系统账户编辑中为单个用户继承、设为无限或单独覆盖。</p>
            </div>
          </div>

          <a-alert
            v-if="sectionErrors['user-request-limit']"
            type="error"
            show-icon
            message="用户限制加载失败"
            :description="sectionErrors['user-request-limit']"
          >
            <template #action>
              <a-button size="small" @click="retrySection('user-request-limit')">重新加载</a-button>
            </template>
          </a-alert>
          <a-skeleton v-else-if="!sectionReady['user-request-limit']" active :paragraph="{ rows: 2 }" />
          <template v-else>
            <a-alert
              class="request-limit-alert"
              type="info"
              show-icon
              message="性能优先"
              description="网关请求不会等待 Redis 或数据库。多节点之间按秒级后台同步，因此高并发时允许短暂超额，但不会拖慢正常请求。"
            />
            <div class="settings-grid">
              <div class="setting-item">
                <a-form-item label="每分钟请求数" tooltip="0 表示无限；达到上限后，本分钟内的新请求立即返回 429。">
                  <a-input-number v-model:value="systemForm.gatewayUserRequestLimitPerMinute" :min="0" :max="1000000000" :precision="0" :step="1" style="width: 100%" />
                </a-form-item>
              </div>
              <div class="setting-item">
                <a-form-item label="每日请求数" tooltip="0 表示无限；按系统使用统计时区的自然日计算。">
                  <a-input-number v-model:value="systemForm.gatewayUserRequestLimitPerDay" :min="0" :max="1000000000" :precision="0" :step="1" style="width: 100%" />
                </a-form-item>
              </div>
              <div class="setting-item">
                <a-form-item label="每周请求数" tooltip="0 表示无限；按系统使用统计时区、周一作为一周起点。">
                  <a-input-number v-model:value="systemForm.gatewayUserRequestLimitPerWeek" :min="0" :max="1000000000" :precision="0" :step="1" style="width: 100%" />
                </a-form-item>
              </div>
              <div class="setting-item">
                <a-form-item label="每月请求数" tooltip="0 表示无限；按系统使用统计时区的自然月计算。">
                  <a-input-number v-model:value="systemForm.gatewayUserRequestLimitPerMonth" :min="0" :max="1000000000" :precision="0" :step="1" style="width: 100%" />
                </a-form-item>
              </div>
              <div class="setting-item">
                <a-form-item label="AI 账户数量限制" tooltip="默认 100；0 表示无限。限制每个用户可创建的自有 AI 账户数量，删除账户后释放名额。">
                  <a-input-number v-model:value="systemForm.userAiAccountLimit" :min="0" :max="1000000" :precision="0" :step="1" style="width: 100%" />
                </a-form-item>
              </div>
            </div>
          </template>
        </section>

        <section class="settings-section" :ref="(element) => setLazySectionElement(element, 'account-health')">
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

          <a-skeleton v-if="!sectionReady['account-health']" active :paragraph="{ rows: 2 }" />
          <div v-else class="settings-grid">
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
              <a-form-item label="连续失败阈值" tooltip="默认 3 次；达到阈值后才允许进入临时不可调用处理，降低网络抖动误杀。">
                <a-input-number v-model:value="systemForm.accountHealthCheckFailureThreshold" :min="1" :max="10" style="width: 100%" />
              </a-form-item>
            </div>
          </div>
        </section>

        <section class="settings-section" :ref="(element) => setLazySectionElement(element, 'api-rate-limit')">
          <div class="section-heading">
            <div>
              <h3 class="section-title">
                <span>后台接口限流</span>
                <a-tooltip title="保护 /__aisys__/api 后台接口，避免同一来源或同一登录用户在短时间内压垮 DB service；健康检查不受影响，限流固定启用。">
                  <QuestionCircleOutlined class="help-icon" />
                </a-tooltip>
              </h3>
            </div>
          </div>

          <a-skeleton v-if="!sectionReady['api-rate-limit']" active :paragraph="{ rows: 3 }" />
          <div v-else class="settings-grid">
            <div class="setting-item">
              <a-form-item label="IP 读请求每分钟" tooltip="默认 600；适用于 GET、HEAD 和 OPTIONS。">
                <a-input-number v-model:value="systemForm.systemApiRateLimitIpReadPerMinute" :min="0" :max="1000000" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="IP 读请求 10 秒突发" tooltip="默认 120；用于拦截短时间刷新列表和探测接口。">
                <a-input-number v-model:value="systemForm.systemApiRateLimitIpReadBurstPer10Seconds" :min="0" :max="1000000" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="IP 写请求每分钟" tooltip="默认 180；适用于 POST、PATCH、PUT 和 DELETE。">
                <a-input-number v-model:value="systemForm.systemApiRateLimitIpWritePerMinute" :min="0" :max="1000000" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="IP 写请求 10 秒突发" tooltip="默认 40；优先挡住批量提交和暴力探测。">
                <a-input-number v-model:value="systemForm.systemApiRateLimitIpWriteBurstPer10Seconds" :min="0" :max="1000000" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="登录用户读请求每分钟" tooltip="默认 300；同一登录账号的后台读请求保护。">
                <a-input-number v-model:value="systemForm.systemApiRateLimitUserReadPerMinute" :min="0" :max="1000000" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="登录用户写请求每分钟" tooltip="默认 120；对保存、删除、批量操作等写请求再加一层限制。">
                <a-input-number v-model:value="systemForm.systemApiRateLimitUserWritePerMinute" :min="0" :max="1000000" style="width: 100%" />
              </a-form-item>
            </div>
          </div>
        </section>

        <section class="settings-section" :ref="(element) => setLazySectionElement(element, 'gateway-core')">
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
          <a-skeleton v-if="!sectionReady['gateway-core']" active :paragraph="{ rows: 2 }" />
          <div v-else class="settings-grid">
            <div class="setting-item">
              <a-form-item label="临时不可调用最大暂停时间（分钟）" tooltip="默认 2 分钟；账号进入临时不可调用后先走快速恢复通道：3 秒起步，失败后翻倍；单次等待不会超过这个最大暂停时间。">
                <a-input-number v-model:value="systemForm.defaultTemporaryUnschedulableMinutes" :min="1" :max="1440" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="安全原地重试间隔（秒）" tooltip="仅对可安全重放的文本请求，在主请求已发出且响应头到达前发生传输异常时使用；完整 HTTP、正文中断、配置首字截止和副作用请求不占次数。">
                <a-input-number v-model:value="systemForm.temporaryUnschedulableRetryIntervalSeconds" :min="0" :max="3600" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="安全原地重试次数" tooltip="整次请求共享的同账户重试上限；兄弟 Key 会先尝试且不占次数。不按上游状态码或正文判断，也不写账户或 Key 状态。">
                <a-input-number v-model:value="systemForm.temporaryUnschedulableRetryAttempts" :min="0" :max="10" style="width: 100%" />
              </a-form-item>
            </div>
          </div>
        </section>

        <section class="settings-section" :ref="(element) => setLazySectionElement(element, 'cooldown-retest')">
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

          <a-skeleton v-if="!sectionReady['cooldown-retest']" active :paragraph="{ rows: 1 }" />
          <div v-else class="settings-grid">
            <div class="setting-item">
              <a-form-item label="长期不可用观察阈值（小时）" tooltip="默认 12 小时；从进入临时不可调用或限流中开始计时，超过后不转异常，而是显示为长期不可用。">
                <a-input-number v-model:value="systemForm.cooldownAccountRetestMaxBackoffHours" :min="1" :max="720" style="width: 100%" />
              </a-form-item>
            </div>
          </div>
        </section>

        <section class="settings-section" :ref="(element) => setLazySectionElement(element, 'gateway-core')">
          <div class="section-heading">
            <div>
              <h3 class="section-title">
                <span>请求等待与流式中断</span>
                <a-tooltip title="文本和图像请求使用独立的当前账号尝试超时；只有暂时没有可派发账号时才累计无账号等待时间。">
                  <QuestionCircleOutlined class="help-icon" />
                </a-tooltip>
              </h3>
            </div>
          </div>

          <a-skeleton v-if="!sectionReady['gateway-core']" active :paragraph="{ rows: 3 }" />
          <div v-else class="settings-grid">
            <div class="setting-item">
              <a-form-item label="文本首响应等待（秒）" tooltip="只作用于文本 lane：当前账号超过该时间仍未返回响应头或非流式首字节时，进入未提交接管。">
                <a-input-number v-model:value="systemForm.textFirstResponseTimeoutSeconds" :min="10" :max="3600" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="文本流式停顿（秒）" tooltip="只作用于文本 lane：收到首段内容后，超过该时间没有任何上游新数据时收口当前尝试。">
                <a-input-number v-model:value="systemForm.textStreamIdleTimeoutSeconds" :min="1" :max="3600" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="文本未提交尝试寿命（秒）" tooltip="只作用于文本 lane：当前账号尚未产生模型语义输出时的单次尝试最大存活时间；语义输出后不再使用该绝对寿命。">
                <a-input-number v-model:value="systemForm.textUncommittedAttemptMaxLifetimeSeconds" :min="60" :max="86400" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="图像首响应等待（秒）" tooltip="只作用于 image lane 的单次 attempt；超时终止当前候选，若下游尚未提交则按统一候选机制继续切 Key、账户或分组。快速模式的文本首 token 截止不作用于图片。">
                <a-input-number v-model:value="systemForm.imageFirstResponseTimeoutSeconds" :min="10" :max="3600" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="图像流式停顿（秒）" tooltip="只作用于 image lane：收到首段内容后，超过该时间没有任何上游新数据时收口当前尝试。">
                <a-input-number v-model:value="systemForm.imageStreamIdleTimeoutSeconds" :min="1" :max="3600" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="图像未提交尝试寿命（秒）" tooltip="只作用于 image lane：当前账号尚未产生模型语义输出时的单次尝试最大存活时间。">
                <a-input-number v-model:value="systemForm.imageUncommittedAttemptMaxLifetimeSeconds" :min="60" :max="86400" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="图像整请求总时限（秒）" tooltip="一次通用网关图片请求从接收到候选切换决策的总墙钟，默认 3600 秒。已在执行且仍处于图片专用 attempt 时限内的请求不会被机械中断；失败后只有总墙钟仍有余量才继续后备候选。">
                <a-input-number v-model:value="systemForm.imageRequestWallTimeoutSeconds" :min="60" :max="86400" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="AI 对话生图总超时（秒）" tooltip="一次 generate_image 工具调用的整体时限，包含网关选号、账户切换、上游生成、结果读取和资产保存；默认 900 秒。">
                <a-input-number v-model:value="systemForm.chatImageGenerationTotalTimeoutSeconds" :min="60" :max="86400" style="width: 100%" />
              </a-form-item>
            </div>
            <div class="setting-item">
              <a-form-item label="无可用账号等待（秒）" tooltip="只在没有可立即派发账号时累计；当前账号仍在执行或存在可派发候选时不会因为该时间到达而停止服务端接管。">
                <a-input-number v-model:value="systemForm.noAvailableAccountWaitTimeoutSeconds" :min="10" :max="3600" style="width: 100%" />
              </a-form-item>
            </div>
          </div>
        </section>

        <section class="settings-section" :ref="(element) => setLazySectionElement(element, 'data-retention')">
          <div class="section-heading">
            <div>
              <h3 class="section-title">
                <span>数据保留与清理</span>
                <a-tooltip title="配置 usage、日志索引和统计缓存的保留期；清理间隔与批量吞吐由后台内部常量控制。">
                  <QuestionCircleOutlined class="help-icon" />
                </a-tooltip>
              </h3>
            </div>
          </div>

          <a-skeleton v-if="!sectionReady['data-retention']" active :paragraph="{ rows: 2 }" />
          <div v-else class="settings-grid">
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
import { nextTick, onActivated, onMounted, onBeforeUnmount, onDeactivated, reactive, ref, watch } from 'vue'

import { api } from '@/api/client'
import type { ManagementSettingsSectionKey } from '@/api/domains/settings'
import type { GlobalSettings, SystemSettings } from '@/types/domain'
import { authState } from '@/composables/useAuth'
import { applyAppBrand } from '@/composables/useAppBrand'
import { extractApiErrorMessage } from '@/shared/apiError'
import {
  defaultGlobalSettings,
  defaultSystemSettings,
  normalizeGlobalSettings,
  normalizeSystemSettings,
  type GlobalForm,
  type SystemForm
} from './settingsForm'
import { buildSettingsSectionRequestSignature, createSettingsSectionRequestGate } from './settingsSectionRequestGate'

const savingGlobal = ref(false)
const savingSystem = ref(false)
const globalForm = reactive<GlobalForm>({ ...defaultGlobalSettings })
const systemForm = reactive<SystemForm>({ ...defaultSystemSettings })
const sectionReady = reactive<Record<ManagementSettingsSectionKey, boolean>>({
  brand: false, 'gateway-core': false, 'user-request-limit': false, 'account-health': false, 'api-rate-limit': false,
  'cooldown-retest': false, 'data-retention': false
})
const sectionLoading = reactive<Record<ManagementSettingsSectionKey, boolean>>({ ...sectionReady })
const sectionErrors = reactive<Record<ManagementSettingsSectionKey, string | undefined>>({
  brand: undefined, 'gateway-core': undefined, 'user-request-limit': undefined, 'account-health': undefined, 'api-rate-limit': undefined,
  'cooldown-retest': undefined, 'data-retention': undefined
})
const sectionBaselines = reactive<Record<string, Record<string, unknown>>>({})
const sectionFields: Record<ManagementSettingsSectionKey, readonly string[]> = {
  brand: ['appName', 'appIcon'],
  'gateway-core': ['gatewayTextRawBodyLimitMegabytes', 'accountCircuitConfirmationFailuresRequired', 'defaultTemporaryUnschedulableMinutes', 'temporaryUnschedulableRetryIntervalSeconds', 'temporaryUnschedulableRetryAttempts', 'textFirstResponseTimeoutSeconds', 'textStreamIdleTimeoutSeconds', 'textUncommittedAttemptMaxLifetimeSeconds', 'imageFirstResponseTimeoutSeconds', 'imageStreamIdleTimeoutSeconds', 'imageUncommittedAttemptMaxLifetimeSeconds', 'imageRequestWallTimeoutSeconds', 'chatImageGenerationTotalTimeoutSeconds', 'noAvailableAccountWaitTimeoutSeconds'],
  'user-request-limit': ['gatewayUserRequestLimitPerMinute', 'gatewayUserRequestLimitPerDay', 'gatewayUserRequestLimitPerWeek', 'gatewayUserRequestLimitPerMonth', 'userAiAccountLimit'],
  'account-health': ['accountHealthCheckIntervalHours', 'accountHealthCheckJitterMinutes', 'accountHealthCheckFailureThreshold'],
  'api-rate-limit': ['systemApiRateLimitIpReadPerMinute', 'systemApiRateLimitIpReadBurstPer10Seconds', 'systemApiRateLimitIpWritePerMinute', 'systemApiRateLimitIpWriteBurstPer10Seconds', 'systemApiRateLimitUserReadPerMinute', 'systemApiRateLimitUserWritePerMinute'],
  'cooldown-retest': ['cooldownAccountRetestMaxBackoffHours'],
  'data-retention': ['usageRecordRetentionDays', 'runtimeLogIndexRetentionDays', 'publicApiLogRetentionDays']
}
const sectionElements = new Map<Element, ManagementSettingsSectionKey>()
let sectionObserver: IntersectionObserver | undefined
const sectionRequestGate = createSettingsSectionRequestGate()
const sectionSaveRequestGate = createSettingsSectionRequestGate()
let pageActive = true
let viewerKey = currentViewerKey()

function sectionValues(sectionKey: ManagementSettingsSectionKey): Record<string, unknown> {
  const source = sectionKey === 'brand' ? globalForm : systemForm
  return Object.fromEntries(sectionFields[sectionKey].map((key) => [key, (source as unknown as Record<string, unknown>)[key]]))
}

function applySystemSectionValues(sectionKey: ManagementSettingsSectionKey, values: Record<string, unknown>): void {
  const normalized = normalizeSystemSettings({ ...defaultSystemSettings, ...values } as SystemSettings)
  for (const key of sectionFields[sectionKey]) {
    (systemForm as unknown as Record<string, unknown>)[key] = (normalized as unknown as Record<string, unknown>)[key]
  }
}

async function loadSection(sectionKey: ManagementSettingsSectionKey, force = false): Promise<void> {
  if (!pageActive) return
  if (sectionLoading[sectionKey] || (sectionReady[sectionKey] && !force)) return
  const signature = currentSectionRequestSignature(sectionKey)
  const requestToken = sectionRequestGate.begin(sectionKey, signature)
  sectionLoading[sectionKey] = true
  sectionErrors[sectionKey] = undefined
  try {
    const result = await api.settings.section(sectionKey)
    if (!sectionRequestGate.isCurrent(requestToken, currentSectionRequestSignature(sectionKey))) return
    const dirty = sectionReady[sectionKey] ? changedPayload(sectionKey) : {}
    const current = sectionValues(sectionKey)
    const responseValues = { ...result.values }
    for (const key of Object.keys(dirty)) responseValues[key] = current[key] as string | number
    if (sectionKey === 'brand') Object.assign(globalForm, normalizeGlobalSettings(responseValues as unknown as GlobalSettings))
    else applySystemSectionValues(sectionKey, responseValues)
    sectionBaselines[sectionKey] = { ...result.values }
    sectionReady[sectionKey] = true
    if (sectionKey === 'brand') applyAppBrand(globalForm)
  } catch (error) {
    if (sectionRequestGate.isCurrent(requestToken, currentSectionRequestSignature(sectionKey))) {
      sectionErrors[sectionKey] = extractApiErrorMessage(error, `加载 ${sectionKey} 设置失败`)
    }
  } finally {
    if (sectionRequestGate.isCurrent(requestToken, currentSectionRequestSignature(sectionKey))) sectionLoading[sectionKey] = false
  }
}

function currentSectionRequestSignature(sectionKey: ManagementSettingsSectionKey): string {
  const viewer = authState.currentUser.value
  return buildSettingsSectionRequestSignature({
    sectionKey,
    authRevision: authState.revision.value,
    viewerId: viewer?.id,
    viewerRole: viewer?.role
  })
}

function currentViewerKey(): string {
  const viewer = authState.currentUser.value
  return JSON.stringify([viewer?.id ?? 'anonymous', viewer?.role ?? 'anonymous'])
}

async function loadSettings() {
  await Promise.all([loadSection('brand'), loadSection('gateway-core')])
}

async function saveGlobalSettings() {
  const signature = currentSectionRequestSignature('brand')
  const requestToken = sectionSaveRequestGate.begin('brand', signature)
  savingGlobal.value = true
  try {
    const payload = changedPayload('brand')
    if (!Object.keys(payload).length) return
    const submittedSnapshot = sectionValues('brand')
    const next = await api.settings.updateSection('brand', payload)
    if (!sectionSaveRequestGate.isCurrent(requestToken, currentSectionRequestSignature('brand'))) return
    const current = sectionValues('brand')
    const responseValues = { ...next.values }
    for (const key of sectionFields.brand) {
      if (current[key] !== submittedSnapshot[key]) responseValues[key] = current[key] as string
    }
    Object.assign(globalForm, normalizeGlobalSettings(responseValues as unknown as GlobalSettings))
    sectionBaselines.brand = { ...next.values }
    applyAppBrand(globalForm)
    message.success('全局配置已保存')
  } catch (error) {
    if (sectionSaveRequestGate.isCurrent(requestToken, currentSectionRequestSignature('brand'))) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '保存全局配置失败'))
    }
  } finally {
    if (sectionSaveRequestGate.isCurrent(requestToken, currentSectionRequestSignature('brand'))) savingGlobal.value = false
  }
}

async function saveSystemSettings() {
  let activeRequest: { sectionKey: ManagementSettingsSectionKey; generation: number; signature: string } | undefined
  savingSystem.value = true
  try {
    for (const sectionKey of Object.keys(sectionFields).filter((key) => key !== 'brand') as ManagementSettingsSectionKey[]) {
      if (!sectionReady[sectionKey]) continue
      const payload = changedPayload(sectionKey)
      if (!Object.keys(payload).length) continue
      const signature = currentSectionRequestSignature(sectionKey)
      activeRequest = sectionSaveRequestGate.begin(sectionKey, signature)
      const submittedSnapshot = sectionValues(sectionKey)
      const next = await api.settings.updateSection(sectionKey, payload)
      if (!sectionSaveRequestGate.isCurrent(activeRequest, currentSectionRequestSignature(sectionKey))) return
      const current = sectionValues(sectionKey)
      const responseValues = { ...next.values }
      for (const key of sectionFields[sectionKey]) {
        if (current[key] !== submittedSnapshot[key]) responseValues[key] = current[key] as string | number
      }
      applySystemSectionValues(sectionKey, responseValues)
      sectionBaselines[sectionKey] = { ...next.values }
    }
    message.success('系统设置已保存')
  } catch (error) {
    if (activeRequest && sectionSaveRequestGate.isCurrent(activeRequest, currentSectionRequestSignature(activeRequest.sectionKey))) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '保存系统设置失败'))
    }
  } finally {
    if (!activeRequest || sectionSaveRequestGate.isCurrent(activeRequest, currentSectionRequestSignature(activeRequest.sectionKey))) savingSystem.value = false
  }
}

function resetGlobalDefaults() {
  Object.assign(globalForm, defaultGlobalSettings)
}

function resetSystemDefaults() {
  for (const sectionKey of Object.keys(sectionFields).filter((key) => key !== 'brand') as ManagementSettingsSectionKey[]) {
    if (!sectionReady[sectionKey]) continue
    for (const key of sectionFields[sectionKey]) (systemForm as unknown as Record<string, unknown>)[key] = (defaultSystemSettings as unknown as Record<string, unknown>)[key]
  }
}

function changedPayload(sectionKey: ManagementSettingsSectionKey): Record<string, string | number> {
  const current = sectionValues(sectionKey)
  const baseline = sectionBaselines[sectionKey] ?? {}
  return Object.fromEntries(Object.entries(current).filter(([key, value]) => value !== baseline[key])) as Record<string, string | number>
}

function setLazySectionElement(element: unknown, sectionKey: ManagementSettingsSectionKey): void {
  if (!(element instanceof Element)) return
  sectionElements.set(element, sectionKey)
  sectionObserver?.observe(element)
}

function retrySection(sectionKey: ManagementSettingsSectionKey): void { void loadSection(sectionKey, true) }

function resetSectionsForViewerChange(): void {
  Object.assign(globalForm, defaultGlobalSettings)
  Object.assign(systemForm, defaultSystemSettings)
  for (const key of Object.keys(sectionReady) as ManagementSettingsSectionKey[]) {
    sectionReady[key] = false
    sectionLoading[key] = false
    sectionErrors[key] = undefined
    delete sectionBaselines[key]
  }
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
  sectionObserver = typeof IntersectionObserver === 'undefined' ? undefined : new IntersectionObserver((entries) => {
    for (const entry of entries) if (entry.isIntersecting) {
      const key = sectionElements.get(entry.target)
      if (key) void loadSection(key)
    }
  }, { rootMargin: '240px' })
  void loadSettings()
  if (!sectionObserver) {
    for (const key of Object.keys(sectionFields) as ManagementSettingsSectionKey[]) void loadSection(key)
  }
  void nextTick(() => sectionElements.forEach((_key, element) => sectionObserver?.observe(element)))
})

watch(() => authState.revision.value, () => {
  sectionRequestGate.invalidate()
  sectionSaveRequestGate.invalidate()
  savingGlobal.value = false
  savingSystem.value = false
  for (const key of Object.keys(sectionLoading) as ManagementSettingsSectionKey[]) sectionLoading[key] = false
  viewerKey = currentViewerKey()
  resetSectionsForViewerChange()
})

onDeactivated(() => {
  pageActive = false
  sectionRequestGate.deactivate()
  sectionSaveRequestGate.deactivate()
  savingGlobal.value = false
  savingSystem.value = false
  for (const key of Object.keys(sectionLoading) as ManagementSettingsSectionKey[]) sectionLoading[key] = false
})

onActivated(() => {
  pageActive = true
  sectionRequestGate.activate()
  sectionSaveRequestGate.activate()
})

onBeforeUnmount(() => {
  pageActive = false
  sectionRequestGate.deactivate()
  sectionSaveRequestGate.deactivate()
  sectionObserver?.disconnect()
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

.section-description {
  margin: 6px 0 0;
  color: #64748b;
  font-size: 13px;
  line-height: 1.6;
}

.request-limit-section {
  border-color: #dbe7f5;
}

.request-limit-alert {
  margin-bottom: 16px;
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
