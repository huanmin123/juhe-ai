<template>
  <div class="feature-rules-page">
    <a-card class="page-card">
      <div class="feature-rules-toolbar">
        <a-alert
          class="feature-rules-alert"
          type="info"
          show-icon
          message="特征规则由后端代码内置维护，只读展示用于排障；需要调整时应修改代码并补回归验证。"
        />
        <a-button :loading="loading" @click="loadRules">
          <template #icon>
            <ReloadOutlined />
          </template>
          刷新
        </a-button>
      </div>

      <a-tabs v-model:active-key="activeTab" class="feature-rules-tabs">
        <a-tab-pane key="stream" :tab="`流式事件特征（${streamRules.length}）`">
          <a-skeleton v-if="loading" active :paragraph="{ rows: 5 }" />
          <a-empty v-else-if="!streamRuleDisplayItems.length" description="当前没有内置流式事件特征规则" />
          <a-collapse v-else class="feature-rule-collapse" ghost>
            <a-collapse-panel v-for="rule in streamRuleDisplayItems" :key="rule.id">
              <template #header>
                <div class="feature-rule-header">
                  <div class="feature-rule-heading">
                    <div class="feature-rule-title">{{ rule.name }}</div>
                    <div class="feature-rule-summary">{{ rule.description || '暂无描述' }}</div>
                  </div>
                  <a-space wrap>
                    <a-tag :color="rule.enabled ? 'green' : 'default'">{{ rule.enabled ? '启用' : '停用' }}</a-tag>
                    <a-tag color="purple">{{ rule.actionText }}</a-tag>
                    <a-tag>{{ rule.endpoint }}</a-tag>
                  </a-space>
                </div>
              </template>

              <div class="feature-rule-body">
                <a-descriptions bordered size="small" :column="2" class="feature-rule-descriptions">
                  <a-descriptions-item label="规则 ID" :span="2">{{ rule.id }}</a-descriptions-item>
                  <a-descriptions-item label="来源">{{ rule.source }}</a-descriptions-item>
                  <a-descriptions-item label="供应商">{{ rule.provider }}</a-descriptions-item>
                  <a-descriptions-item label="触发阶段">{{ rule.phaseText }}</a-descriptions-item>
                  <a-descriptions-item label="处理动作">{{ rule.actionText }}</a-descriptions-item>
                  <a-descriptions-item label="账号策略">{{ rule.accountPolicyText }}</a-descriptions-item>
                  <a-descriptions-item label="为什么这样做" :span="2">{{ rule.rationale || '暂无记录' }}</a-descriptions-item>
                </a-descriptions>
                <pre class="feature-rule-json">{{ formatRuleJson(rule.rule) }}</pre>
              </div>
            </a-collapse-panel>
          </a-collapse>
        </a-tab-pane>

        <a-tab-pane key="upstream-error" :tab="`上游错误响应特征（${upstreamErrorRules.length}）`">
          <a-skeleton v-if="loading" active :paragraph="{ rows: 5 }" />
          <a-empty v-else-if="!upstreamErrorRuleDisplayItems.length" description="当前没有内置上游错误响应特征规则" />
          <a-collapse v-else class="feature-rule-collapse" ghost>
            <a-collapse-panel v-for="rule in upstreamErrorRuleDisplayItems" :key="rule.id">
              <template #header>
                <div class="feature-rule-header">
                  <div class="feature-rule-heading">
                    <div class="feature-rule-title">{{ rule.name }}</div>
                    <div class="feature-rule-summary">{{ rule.description || '暂无描述' }}</div>
                  </div>
                  <a-space wrap>
                    <a-tag :color="rule.enabled ? 'green' : 'default'">{{ rule.enabled ? '启用' : '停用' }}</a-tag>
                    <a-tag color="purple">{{ rule.actionText }}</a-tag>
                    <a-tag>{{ rule.endpoint }}</a-tag>
                  </a-space>
                </div>
              </template>

              <div class="feature-rule-body">
                <a-descriptions bordered size="small" :column="2" class="feature-rule-descriptions">
                  <a-descriptions-item label="规则 ID" :span="2">{{ rule.id }}</a-descriptions-item>
                  <a-descriptions-item label="来源">{{ rule.source }}</a-descriptions-item>
                  <a-descriptions-item label="供应商">{{ rule.provider }}</a-descriptions-item>
                  <a-descriptions-item label="处理动作">{{ rule.actionText }}</a-descriptions-item>
                  <a-descriptions-item label="账号策略">{{ rule.accountPolicyText }}</a-descriptions-item>
                  <a-descriptions-item label="为什么这样做" :span="2">{{ rule.rationale || '暂无记录' }}</a-descriptions-item>
                </a-descriptions>
                <pre class="feature-rule-json">{{ formatRuleJson(rule.rule) }}</pre>
              </div>
            </a-collapse-panel>
          </a-collapse>
        </a-tab-pane>
      </a-tabs>
    </a-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import type { StreamInterceptRuleCatalogItem, UpstreamErrorFeatureRuleCatalogItem } from '@/types/domain'

interface FeatureRuleDisplayItem {
  id: string
  enabled: boolean
  name: string
  description?: string
  source: string
  provider: string
  endpoint: string
  actionText: string
  phaseText?: string
  accountPolicyText: string
  rationale?: string
  rule: Record<string, unknown>
}

const activeTab = ref('stream')
const loading = ref(false)
const streamRules = ref<StreamInterceptRuleCatalogItem[]>([])
const upstreamErrorRules = ref<UpstreamErrorFeatureRuleCatalogItem[]>([])

const streamRuleDisplayItems = computed<FeatureRuleDisplayItem[]>(() =>
  streamRules.value.map((rule) => ({
    id: rule.id,
    enabled: rule.enabled,
    name: rule.name,
    description: rule.description,
    source: sourceText(rule.source),
    provider: providerText(rule.provider),
    endpoint: rule.endpoint,
    actionText: streamActionText(rule.action),
    phaseText: streamTriggerPhaseText(rule.triggerPhase),
    accountPolicyText: accountPolicyText(rule.accountPolicy),
    rationale: rule.rationale,
    rule: rule.rule
  }))
)

const upstreamErrorRuleDisplayItems = computed<FeatureRuleDisplayItem[]>(() =>
  upstreamErrorRules.value.map((rule) => ({
    id: rule.id,
    enabled: rule.enabled,
    name: rule.name,
    description: rule.description,
    source: sourceText(rule.source),
    provider: providerText(rule.provider),
    endpoint: rule.endpoint,
    actionText: upstreamErrorActionText(rule.action),
    accountPolicyText: accountPolicyText(rule.accountPolicy),
    rationale: rule.rationale,
    rule: rule.rule
  }))
)

async function loadRules() {
  loading.value = true
  try {
    const [nextStreamRules, nextUpstreamErrorRules] = await Promise.all([
      api.featureRules.streamInterceptRules(),
      api.featureRules.upstreamErrorFeatureRules()
    ])
    streamRules.value = nextStreamRules
    upstreamErrorRules.value = nextUpstreamErrorRules
  } catch (error) {
    console.error(error)
    message.error('加载特征规则失败')
  } finally {
    loading.value = false
  }
}

function sourceText(source?: string): string {
  if (source === 'audit_log') return '审计日志'
  if (source === 'source_code') return '源码排查'
  if (source === 'manual_verification') return '人工验证'
  return source || '未记录'
}

function providerText(provider: string): string {
  if (provider === 'openai') return 'OpenAI'
  if (provider === 'all') return '全部供应商'
  return provider
}

function streamTriggerPhaseText(phase: string): string {
  if (phase === 'before_output') return '输出前'
  if (phase === 'after_output') return '输出后'
  if (phase === 'all') return '全部阶段'
  return phase
}

function streamActionText(action: string): string {
  if (action === 'client_retry') return '客户端重试'
  if (action === 'server_replay') return '服务端重放'
  if (action === 'custom_rewrite') return '自定义改写'
  return action
}

function upstreamErrorActionText(action: string): string {
  if (action === 'passthrough_request_error') return '原样返回请求级错误'
  return action
}

function accountPolicyText(policy: string): string {
  if (policy === 'temporary_unavailable') return '写入临时不可调用'
  if (policy === 'none') return '不改变账号状态'
  return policy
}

function formatRuleJson(rule: Record<string, unknown>): string {
  return JSON.stringify(rule, null, 2)
}

onMounted(() => {
  void loadRules()
})
</script>

<style scoped>
.feature-rules-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.feature-rules-toolbar {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 18px;
}

.feature-rules-alert {
  flex: 1 1 auto;
  border-radius: 12px;
}

.feature-rules-tabs {
  min-width: 0;
}

.feature-rule-collapse {
  background: #f8fafc;
  border: 1px solid #edf1f7;
  border-radius: 12px;
}

.feature-rule-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  width: 100%;
}

.feature-rule-heading {
  min-width: 0;
}

.feature-rule-title {
  color: #0f172a;
  font-size: 14px;
  font-weight: 800;
}

.feature-rule-summary {
  margin-top: 4px;
  color: #64748b;
  font-size: 12px;
  line-height: 18px;
}

.feature-rule-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.feature-rule-descriptions {
  background: #fff;
}

.feature-rule-json {
  max-height: 360px;
  margin: 0;
  padding: 14px;
  overflow: auto;
  color: #dbeafe;
  font-size: 12px;
  line-height: 18px;
  white-space: pre-wrap;
  word-break: break-word;
  background: #0f172a;
  border-radius: 10px;
}

@media (max-width: 900px) {
  .feature-rules-toolbar,
  .feature-rule-header {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
