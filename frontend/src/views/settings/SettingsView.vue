<template>
  <a-card class="page-card settings-page-card">
    <div class="settings-shell">
      <div class="settings-page-head">
        <div>
          <h1 class="settings-page-title">系统设置</h1>
          <p class="settings-page-desc">网关与账户调度的系统级默认策略；不覆盖账号里的显式配置。</p>
        </div>
        <span class="settings-summary">
          共 {{ settingsTotalItemCount }} 项
          <template v-if="totalCustomCount > 0"> · <b>{{ totalCustomCount }} 项自定义</b> · 其余默认</template>
          <template v-else> · 全部默认</template>
        </span>
      </div>

      <section
        v-for="group in settingGroups"
        :key="group.key"
        class="settings-group"
        :class="{ open: expandedGroups.has(group.key) }"
      >
        <header class="settings-group-bar" @click="toggleGroup(group)">
          <span class="settings-group-chevron">▶</span>
          <h2 class="settings-group-title">{{ group.title }}</h2>
          <span class="settings-group-meta">{{ group.hint }}</span>
          <span class="settings-group-status" :class="groupStatusClass(group)">{{ groupStatusText(group) }}</span>
        </header>

        <div v-if="expandedGroups.has(group.key)" class="settings-group-body">
          <a-alert
            v-if="groupErrorMessage(group)"
            type="error"
            show-icon
            :message="`${group.title}加载失败`"
            :description="groupErrorMessage(group)"
          >
            <template #action>
              <a-button size="small" @click="retryGroup(group)">重新加载</a-button>
            </template>
          </a-alert>
          <a-skeleton v-else-if="!groupReady(group)" active :paragraph="{ rows: 3 }" />

          <!-- 展示态：标签 ······ 值（同语义归并行），点「编辑此组」切换表单 -->
          <template v-else-if="!editingGroups.has(group.key)">
            <div v-for="(subgroup, index) in groupViewRows(group)" :key="subgroup.title || index" class="settings-view-block">
              <div v-if="subgroup.title" class="settings-view-subgroup">{{ subgroup.title }}</div>
              <div v-for="row in subgroup.rows" :key="row.key" class="settings-view-row">
                <span class="settings-view-key">{{ row.label }}</span>
                <span class="settings-view-dots" />
                <span class="settings-view-value" :class="{ custom: row.custom }">
                  {{ row.value }}<em v-if="row.defaultText">（{{ row.defaultText }}）</em>
                </span>
              </div>
            </div>
            <div class="settings-group-actions">
              <a-popconfirm
                v-for="action in group.actions ?? []"
                :key="action.key"
                :title="action.confirm"
                @confirm="runGroupAction(group, action)"
              >
                <a-button size="small" :loading="groupActionPending[`${group.key}:${action.key}`]" @click.stop>{{ action.label }}</a-button>
              </a-popconfirm>
              <a-button size="small" @click.stop="beginGroupEditing(group)">编辑此组</a-button>
            </div>
          </template>

          <!-- 编辑态：行式表单（label 左 · 输入右 · 单位后缀），按组保存 -->
          <template v-else>
            <template v-for="entry in group.fields" :key="'divider' in entry ? entry.divider : entry.key">
              <div v-if="'divider' in entry" class="settings-edit-subgroup">{{ entry.divider }}</div>
              <div v-else class="settings-edit-row">
                <label class="settings-edit-label">
                  <span class="settings-edit-label-text">
                    {{ entry.label }}
                    <a-tooltip v-if="entry.tip" :title="entry.tip">
                      <QuestionCircleOutlined class="settings-help-icon" />
                    </a-tooltip>
                  </span>
                </label>
                <a-input-number
                  v-if="entry.kind === 'number'"
                  class="settings-edit-input"
                  :value="Number(getFormValue(globalForm, systemForm, entry.key))"
                  :min="entry.min"
                  :max="entry.max"
                  :precision="entry.precision ?? 0"
                  @update:value="(value: string | number | null) => setFormValue(globalForm, systemForm, entry.key, value ?? 0)"
                >
                  <template v-if="entry.unit" #addon-after>{{ entry.unit }}</template>
                </a-input-number>
                <span v-else-if="entry.kind === 'brandIcon'" class="settings-brand-field">
                  <img class="settings-brand-preview" :src="globalForm.appIcon" alt="品牌图标预览" />
                  <a-input
                    class="settings-edit-input"
                    :value="String(getFormValue(globalForm, systemForm, entry.key))"
                    placeholder="/__aisys__/brand-icon.svg"
                    @update:value="(value: string | number) => setFormValue(globalForm, systemForm, entry.key, value)"
                  />
                  <a-upload accept="image/svg+xml,image/png,image/jpeg,image/webp" :before-upload="handleIconUpload" :show-upload-list="false">
                    <a-button size="small">上传图标</a-button>
                  </a-upload>
                  <a-button size="small" type="link" @click="restoreDefaultIcon">恢复默认</a-button>
                </span>
                <a-input
                  v-else
                  class="settings-edit-input"
                  :value="String(getFormValue(globalForm, systemForm, entry.key))"
                  :placeholder="entry.placeholder"
                  @update:value="(value: string | number) => setFormValue(globalForm, systemForm, entry.key, value)"
                />
              </div>
            </template>
            <div class="settings-group-actions">
              <span class="settings-edit-hint">仅保存本组修改 · 其他组不受影响</span>
              <a-button size="small" @click="resetGroupDefaults(group)">恢复默认值</a-button>
              <a-button size="small" @click="cancelGroupEditing(group)">取消</a-button>
              <a-button size="small" type="primary" :loading="savingGroups[group.key]" @click="handleSaveGroup(group)">保存本组</a-button>
            </div>
          </template>
        </div>
      </section>
    </div>
  </a-card>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { computed, onActivated, onMounted, onBeforeUnmount, onDeactivated, reactive, ref, watch } from 'vue'

import { api } from '@/api/client'
import type { ManagementSettingsSectionKey } from '@/api/domains/settings'
import type { GlobalSettings, SystemSettings } from '@/types/domain'
import { authState } from '@/composables/useAuth'
import { applyAppBrand } from '@/composables/useAppBrand'
import { extractApiErrorMessage } from '@/shared/apiError'
import {
  createDefaultSystemForm,
  defaultGlobalSettings,
  normalizeGlobalSettings,
  normalizeSystemSettings,
  serializeUpstreamClientVersionOverrides,
  type GlobalForm,
  type SystemForm
} from './settingsForm'
import {
  fieldSectionIndex,
  getFormValue,
  groupCustomCount,
  groupFieldKeys,
  setFormValue,
  settingDefaultValue,
  settingGroups,
  settingsSectionFields,
  settingsTotalItemCount,
  type SettingGroupAction,
  type SettingGroupDef,
  type SettingsDisplayAccess
} from './settingsDisplay'
import { buildSettingsSectionRequestSignature, createSettingsSectionRequestGate } from './settingsSectionRequestGate'

const globalForm = reactive<GlobalForm>({ ...defaultGlobalSettings })
const systemForm = reactive<SystemForm>(createDefaultSystemForm())
const sectionReady = reactive<Record<ManagementSettingsSectionKey, boolean>>({
  brand: false, 'gateway-core': false, 'user-request-limit': false, 'account-health': false, 'api-rate-limit': false,
  'cooldown-retest': false, 'data-retention': false, 'log-retention': false
})
const sectionLoading = reactive<Record<ManagementSettingsSectionKey, boolean>>({ ...sectionReady })
const sectionErrors = reactive<Record<ManagementSettingsSectionKey, string | undefined>>({
  brand: undefined, 'gateway-core': undefined, 'user-request-limit': undefined, 'account-health': undefined, 'api-rate-limit': undefined,
  'cooldown-retest': undefined, 'data-retention': undefined, 'log-retention': undefined
})
const sectionBaselines = reactive<Record<string, Record<string, unknown>>>({})
const sectionRequestGate = createSettingsSectionRequestGate()
const sectionSaveRequestGate = createSettingsSectionRequestGate()
let pageActive = true

const expandedGroups = ref(new Set<string>())
const editingGroups = ref(new Set<string>())
const savingGroups = reactive<Record<string, boolean>>({})

function sectionValues(sectionKey: ManagementSettingsSectionKey): Record<string, unknown> {
  const source = sectionKey === 'brand' ? globalForm : systemForm
  return Object.fromEntries(settingsSectionFields[sectionKey].map((key) => {
    if (key === 'upstreamClientVersionOverrides') return [key, serializeUpstreamClientVersionOverrides((source as SystemForm).upstreamClientVersionOverrides)]
    return [key, (source as unknown as Record<string, unknown>)[key]]
  }))
}

function applySystemSectionValues(sectionKey: ManagementSettingsSectionKey, values: Record<string, unknown>): void {
  const normalized = normalizeSystemSettings({ ...createDefaultSystemForm(), ...values } as unknown as SystemSettings)
  for (const key of settingsSectionFields[sectionKey]) {
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

/** 折叠胶囊与展示行需要全部 section 的 baseline，页面打开即并行加载 8 个轻量 GET。 */
function loadAllSections(): Promise<unknown> {
  return Promise.all((Object.keys(settingsSectionFields) as ManagementSettingsSectionKey[]).map((key) => loadSection(key)))
}

// ---------------------------------------------------------------------------
// 展示访问器：baseline 读取 + 自定义判定（服务端值 vs 出厂默认）
// ---------------------------------------------------------------------------

function baselineOf(key: string): string | number | undefined {
  const sectionKey = fieldSectionIndex[key]
  if (!sectionKey) return undefined
  const baseline = sectionBaselines[sectionKey]
  if (!baseline) return undefined
  if (key.startsWith('upstreamClientVersionOverrides.')) {
    const raw = baseline.upstreamClientVersionOverrides
    if (typeof raw !== 'object' || raw === null) return ''
    const value = (raw as Record<string, unknown>)[key.slice('upstreamClientVersionOverrides.'.length)]
    return typeof value === 'string' ? value : ''
  }
  const value = baseline[key]
  if (typeof value === 'string' || typeof value === 'number') return value
  return undefined
}

const displayAccess: SettingsDisplayAccess = {
  baseline: (key) => baselineOf(key),
  isCustom: (key) => {
    const baselineValue = baselineOf(key)
    if (baselineValue === undefined) return false
    const fallback = settingDefaultValue(key)
    if (typeof fallback === 'string' || typeof baselineValue === 'string') {
      return String(baselineValue) !== String(fallback)
    }
    return baselineValue !== fallback
  }
}

// ---------------------------------------------------------------------------
// 组交互态
// ---------------------------------------------------------------------------

function toggleGroup(group: SettingGroupDef): void {
  const next = new Set(expandedGroups.value)
  if (next.has(group.key)) next.delete(group.key)
  else next.add(group.key)
  expandedGroups.value = next
}

function beginGroupEditing(group: SettingGroupDef): void {
  const next = new Set(editingGroups.value)
  next.add(group.key)
  editingGroups.value = next
}

function groupReady(group: SettingGroupDef): boolean {
  return group.sectionKeys.every((key) => sectionReady[key])
}

function groupErrorMessage(group: SettingGroupDef): string | undefined {
  for (const key of group.sectionKeys) {
    if (sectionErrors[key]) return sectionErrors[key]
  }
  return undefined
}

function retryGroup(group: SettingGroupDef): void {
  for (const key of group.sectionKeys) {
    if (sectionErrors[key]) void loadSection(key, true)
  }
}

function groupViewRows(group: SettingGroupDef) {
  return group.viewRows(displayAccess)
}

const groupDirtyCounts = computed<Record<string, number>>(() => {
  const result: Record<string, number> = {}
  for (const group of settingGroups) {
    result[group.key] = groupFieldKeys(group).filter((key) => {
      const formValue = getFormValue(globalForm, systemForm, key)
      const baselineValue = baselineOf(key)
      return !settingValueEquals(formValue, baselineValue)
    }).length
  }
  return result
})

const groupCustomCounts = computed<Record<string, number>>(() => {
  const result: Record<string, number> = {}
  for (const group of settingGroups) result[group.key] = groupCustomCount(group, displayAccess)
  return result
})

const totalCustomCount = computed(() => Object.values(groupCustomCounts.value).reduce((sum, count) => sum + count, 0))

function groupStatusText(group: SettingGroupDef): string {
  const dirty = groupDirtyCounts.value[group.key] ?? 0
  if (dirty > 0) return `${dirty} 项已修改`
  const custom = groupCustomCounts.value[group.key] ?? 0
  return custom > 0 ? `${custom} 项自定义` : '默认'
}

function groupStatusClass(group: SettingGroupDef): string {
  const dirty = groupDirtyCounts.value[group.key] ?? 0
  if (dirty > 0) return 'dirty'
  const custom = groupCustomCounts.value[group.key] ?? 0
  return custom > 0 ? 'custom' : 'default'
}

function cancelGroupEditing(group: SettingGroupDef): void {
  // 精确回滚组内字段到服务端 baseline（未加载时回退出厂默认），不影响其他组的在途编辑
  for (const key of groupFieldKeys(group)) {
    const baselineValue = baselineOf(key)
    setFormValue(globalForm, systemForm, key, baselineValue !== undefined ? baselineValue : settingDefaultValue(key))
  }
  const next = new Set(editingGroups.value)
  next.delete(group.key)
  editingGroups.value = next
}

function resetGroupDefaults(group: SettingGroupDef): void {
  for (const key of groupFieldKeys(group)) {
    setFormValue(globalForm, systemForm, key, settingDefaultValue(key))
  }
}

// ---------------------------------------------------------------------------
// 组级操作（展示态 actions 按钮，如审计日志手动清理）
// ---------------------------------------------------------------------------

const groupActionPending = reactive<Record<string, boolean>>({})

async function runGroupAction(group: SettingGroupDef, action: SettingGroupAction): Promise<void> {
  const pendingKey = `${group.key}:${action.key}`
  if (groupActionPending[pendingKey]) return
  groupActionPending[pendingKey] = true
  try {
    if (action.key === 'audit-cleanup') await cleanupAuditLogs()
  } finally {
    groupActionPending[pendingKey] = false
  }
}

/** 审计日志手动清理：按当前生效保留策略立即执行一轮，toast 展示主要清理统计。 */
async function cleanupAuditLogs(): Promise<void> {
  try {
    const result = await api.settings.cleanupAuditLogs()
    message.success(`清理完成：删除 ${result.deletedLogs} 条日志、${result.deletedPayloadBlobs} 个正文文件`)
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '清理审计日志失败'))
  }
}

// ---------------------------------------------------------------------------
// 保存：按组提交（组内字段的差异按所属 section 分桶，逐 section PATCH）
// ---------------------------------------------------------------------------

async function handleSaveGroup(group: SettingGroupDef): Promise<void> {
  if (group.key === 'brand') {
    await saveGlobalSettings()
    return
  }
  savingGroups[group.key] = true
  let activeRequest: { sectionKey: ManagementSettingsSectionKey; generation: number; signature: string } | undefined
  try {
    for (const sectionKey of group.sectionKeys) {
      if (!sectionReady[sectionKey]) continue
      const payload = changedPayload(sectionKey)
      if (!Object.keys(payload).length) continue
      const signature = currentSectionRequestSignature(sectionKey)
      activeRequest = sectionSaveRequestGate.begin(sectionKey, signature)
      const submittedSnapshot = sectionValues(sectionKey)
      const next = await api.settings.updateSection(sectionKey, payload as Record<string, string | number>)
      if (!sectionSaveRequestGate.isCurrent(activeRequest, currentSectionRequestSignature(sectionKey))) return
      const current = sectionValues(sectionKey)
      const responseValues = { ...next.values }
      for (const key of settingsSectionFields[sectionKey]) {
        if (current[key] !== submittedSnapshot[key]) responseValues[key] = current[key] as string | number
      }
      applySystemSectionValues(sectionKey, responseValues)
      sectionBaselines[sectionKey] = { ...next.values }
    }
    message.success(`${group.title}已保存`)
    const nextEditing = new Set(editingGroups.value)
    nextEditing.delete(group.key)
    editingGroups.value = nextEditing
  } catch (error) {
    // activeRequest 未建立时的失败是本地表单校验（如版本覆盖非法 semver），同样要提示
    if (!activeRequest || sectionSaveRequestGate.isCurrent(activeRequest, currentSectionRequestSignature(activeRequest.sectionKey))) {
      console.error(error)
      message.error(extractApiErrorMessage(error, `保存${group.title}失败`))
    }
  } finally {
    if (!activeRequest || sectionSaveRequestGate.isCurrent(activeRequest, currentSectionRequestSignature(activeRequest.sectionKey))) savingGroups[group.key] = false
  }
}

async function saveGlobalSettings() {
  const signature = currentSectionRequestSignature('brand')
  const requestToken = sectionSaveRequestGate.begin('brand', signature)
  savingGroups.brand = true
  try {
    const payload = changedPayload('brand')
    if (!Object.keys(payload).length) {
      const nextEditing = new Set(editingGroups.value)
      nextEditing.delete('brand')
      editingGroups.value = nextEditing
      return
    }
    const submittedSnapshot = sectionValues('brand')
    const next = await api.settings.updateSection('brand', payload as Record<string, string | number>)
    if (!sectionSaveRequestGate.isCurrent(requestToken, currentSectionRequestSignature('brand'))) return
    const current = sectionValues('brand')
    const responseValues = { ...next.values }
    for (const key of settingsSectionFields.brand) {
      if (current[key] !== submittedSnapshot[key]) responseValues[key] = current[key] as string
    }
    Object.assign(globalForm, normalizeGlobalSettings(responseValues as unknown as GlobalSettings))
    sectionBaselines.brand = { ...next.values }
    applyAppBrand(globalForm)
    message.success('品牌与展示已保存')
    const nextEditing = new Set(editingGroups.value)
    nextEditing.delete('brand')
    editingGroups.value = nextEditing
  } catch (error) {
    if (sectionSaveRequestGate.isCurrent(requestToken, currentSectionRequestSignature('brand'))) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '保存品牌与展示失败'))
    }
  } finally {
    if (sectionSaveRequestGate.isCurrent(requestToken, currentSectionRequestSignature('brand'))) savingGroups.brand = false
  }
}

function changedPayload(sectionKey: ManagementSettingsSectionKey): Record<string, unknown> {
  const current = sectionValues(sectionKey)
  const baseline = sectionBaselines[sectionKey] ?? {}
  return Object.fromEntries(Object.entries(current).filter(([key, value]) => {
    if (key === 'upstreamClientVersionOverrides') {
      // 服务端无覆盖时 GET 不返回该键；空覆盖（{}）与缺失语义等价（全部用内置），不算脏
      const formHas = Object.keys(value as Record<string, string>).length > 0
      const baselineRaw = baseline[key]
      const baselineHas = typeof baselineRaw === 'object' && baselineRaw !== null && Object.keys(baselineRaw).length > 0
      return formHas !== baselineHas || (formHas && !settingValueEquals(value, baselineRaw))
    }
    return !settingValueEquals(value, baseline[key])
  }))
}

function settingValueEquals(value: unknown, baseline: unknown): boolean {
  if (value === baseline) return true
  if (typeof value === 'object' && value !== null && typeof baseline === 'object' && baseline !== null) {
    return JSON.stringify(value, Object.keys(value).sort()) === JSON.stringify(baseline, Object.keys(baseline).sort())
  }
  return false
}

function resetSectionsForViewerChange(): void {
  Object.assign(globalForm, defaultGlobalSettings)
  Object.assign(systemForm, createDefaultSystemForm())
  for (const key of Object.keys(sectionReady) as ManagementSettingsSectionKey[]) {
    sectionReady[key] = false
    sectionLoading[key] = false
    sectionErrors[key] = undefined
    delete sectionBaselines[key]
  }
  editingGroups.value = new Set()
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
      message.success('图标已读取，保存本组后生效')
    }
  }
  reader.onerror = () => {
    message.error('读取图标失败')
  }
  reader.readAsDataURL(file)
  return false
}

onMounted(() => {
  void loadAllSections()
})

watch(() => authState.revision.value, () => {
  sectionRequestGate.invalidate()
  sectionSaveRequestGate.invalidate()
  for (const key of Object.keys(savingGroups)) savingGroups[key] = false
  for (const key of Object.keys(sectionLoading) as ManagementSettingsSectionKey[]) sectionLoading[key] = false
  resetSectionsForViewerChange()
})

onDeactivated(() => {
  pageActive = false
  sectionRequestGate.deactivate()
  sectionSaveRequestGate.deactivate()
  for (const key of Object.keys(savingGroups)) savingGroups[key] = false
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
})
</script>

<style scoped>
.settings-page-card {
  margin-top: 4px;
}

.settings-shell {
  max-width: 880px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.settings-page-head {
  display: flex;
  align-items: flex-end;
  gap: 14px;
  flex-wrap: wrap;
}

.settings-page-title {
  margin: 0;
  font-size: 22px;
  font-weight: 600;
  color: var(--juhe-fg, var(--juhe-fg));
}

.settings-page-desc {
  margin: 4px 0 0;
  font-size: 13px;
  color: var(--juhe-muted, var(--juhe-muted));
}

.settings-summary {
  margin-left: auto;
  font-size: 13px;
  color: var(--juhe-muted, var(--juhe-muted));
  white-space: nowrap;
}

.settings-summary b {
  color: var(--juhe-warn, var(--juhe-warn));
  font-weight: 600;
}

/* 分组折叠卡 */
.settings-group {
  background: var(--juhe-surface);
  border: 1px solid var(--juhe-border, rgba(34, 40, 43, 0.1));
  border-radius: var(--juhe-radius, 12px);
  box-shadow: 0 10px 30px rgba(34, 40, 43, 0.05);
  overflow: hidden;
}

.settings-group-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 14px 20px;
  cursor: pointer;
  user-select: none;
}

.settings-group-bar:hover {
  background: var(--juhe-surface-soft, var(--juhe-surface-soft));
}

.settings-group-chevron {
  color: var(--juhe-faint, var(--juhe-faint));
  font-size: 11px;
  transition: transform 0.18s;
}

.settings-group.open .settings-group-chevron {
  transform: rotate(90deg);
}

.settings-group-title {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
}

.settings-group-meta {
  color: var(--juhe-faint, var(--juhe-faint));
  font-size: 12.5px;
}

.settings-group-status {
  margin-left: auto;
  font-size: 12px;
  padding: 1px 10px;
  border-radius: 999px;
  white-space: nowrap;
}

.settings-group-status.default {
  color: var(--juhe-muted, var(--juhe-muted));
  background: rgba(139, 145, 144, 0.1);
  border: 1px solid rgba(139, 145, 144, 0.22);
}

.settings-group-status.custom,
.settings-group-status.dirty {
  color: var(--juhe-warn, var(--juhe-warn));
  background: rgba(169, 133, 72, 0.1);
  border: 1px solid rgba(169, 133, 72, 0.3);
}

.settings-group-body {
  border-top: 1px solid var(--juhe-border, rgba(34, 40, 43, 0.1));
  padding: 6px 20px 16px;
}

/* 展示行 */
.settings-view-block {
  margin-top: 10px;
}

.settings-view-subgroup {
  font-size: 12px;
  color: var(--juhe-muted, var(--juhe-muted));
  letter-spacing: 1.5px;
  margin: 12px 0 6px;
}

.settings-view-row {
  display: flex;
  align-items: baseline;
  gap: 12px;
  padding: 8px 0;
  border-bottom: 1px dashed rgba(34, 40, 43, 0.06);
}

.settings-view-row:last-child {
  border-bottom: none;
}

.settings-view-key {
  color: var(--juhe-fg-soft, var(--juhe-fg-soft));
  font-size: 13px;
  white-space: nowrap;
}

.settings-view-dots {
  flex: 1;
  border-bottom: 1px dotted rgba(34, 40, 43, 0.16);
  transform: translateY(-4px);
  min-width: 24px;
}

.settings-view-value {
  font-size: 13px;
  font-weight: 500;
  color: var(--juhe-fg, var(--juhe-fg));
  white-space: normal;
  text-align: right;
}

.settings-view-value em {
  font-style: normal;
  color: var(--juhe-faint, var(--juhe-faint));
  font-weight: 400;
}

.settings-view-value.custom {
  color: var(--juhe-warn, var(--juhe-warn));
}

/* 编辑行 */
.settings-edit-subgroup {
  font-size: 12px;
  color: var(--juhe-muted, var(--juhe-muted));
  letter-spacing: 1.5px;
  margin: 14px 0 4px;
}

.settings-edit-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 0;
  border-bottom: 1px dashed rgba(34, 40, 43, 0.06);
}

.settings-edit-label {
  flex: 1;
  min-width: 0;
}

.settings-edit-label-text {
  font-size: 13px;
  color: var(--juhe-fg-soft, var(--juhe-fg-soft));
  display: inline-flex;
  align-items: center;
  gap: 5px;
}

.settings-help-icon {
  color: var(--juhe-faint, var(--juhe-faint));
  cursor: help;
  font-size: 13px;
}

.settings-help-icon:hover {
  color: var(--juhe-accent, var(--juhe-accent));
}

.settings-edit-input {
  flex-shrink: 0;
  width: 170px;
}

.settings-brand-field {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}

.settings-brand-preview {
  width: 26px;
  height: 26px;
  border-radius: 6px;
  object-fit: contain;
  border: 1px solid var(--juhe-border, rgba(34, 40, 43, 0.1));
}

.settings-group-actions {
  display: flex;
  justify-content: flex-end;
  align-items: center;
  gap: 8px;
  padding-top: 12px;
}

.settings-edit-hint {
  margin-right: auto;
  color: var(--juhe-faint, var(--juhe-faint));
  font-size: 12px;
}

@media (max-width: 700px) {
  .settings-group-meta {
    display: none;
  }

  .settings-view-dots {
    display: none;
  }

  .settings-view-row {
    justify-content: space-between;
  }

  .settings-edit-row {
    flex-direction: column;
    align-items: stretch;
    gap: 6px;
  }

  .settings-edit-input {
    width: 100%;
    flex-shrink: 1;
  }

  .settings-brand-field {
    flex-wrap: wrap;
  }
}
</style>
