<template>
  <a-drawer
    :open="open"
    class="model-checks-detail-drawer"
    title="检测结果详情"
    width="720px"
    :body-style="{ padding: '16px' }"
    @update:open="emit('update:open', $event)"
  >
    <a-skeleton v-if="loading" active :paragraph="{ rows: 5 }" />
    <a-empty v-else-if="!run" description="尚未选择检测记录" />
    <div v-else class="run-detail">
      <div class="run-detail-head">
        <div>
          <div class="run-detail-title">{{ targetDisplayName(run) }}</div>
          <div class="run-detail-subtitle">
            检测目标：AI 账户
          </div>
        </div>
        <a-space wrap>
          <a-tag :color="statusColor(run.status)">{{ statusText(run.status) }}</a-tag>
          <a-tag :color="levelColor(run.level)">{{ levelText(run.level) }}</a-tag>
          <a-tag v-if="runTrustedComparison(run)" color="blue">可信对比</a-tag>
          <a-tag>{{ run.score }} / {{ run.maxScore }}</a-tag>
        </a-space>
      </div>

      <a-descriptions bordered size="small" :column="descriptionColumns" class="run-descriptions">
        <a-descriptions-item label="检测 ID">{{ run.id }}</a-descriptions-item>
        <a-descriptions-item label="账户名称">{{ targetDisplayName(run) }}</a-descriptions-item>
        <a-descriptions-item label="模型">{{ modelText(run.model) }}</a-descriptions-item>
        <a-descriptions-item label="创建时间">{{ formatDateTime(run.createdAt) }}</a-descriptions-item>
        <a-descriptions-item label="完成时间">{{ formatDateTime(run.finishedAt) }}</a-descriptions-item>
        <a-descriptions-item label="耗时">{{ formatDuration(run.durationMs) }}</a-descriptions-item>
        <a-descriptions-item label="证据完整度">{{ evidenceCompletenessText(run) }}</a-descriptions-item>
        <a-descriptions-item label="结论">{{ run.message || run.errorMessage || '-' }}</a-descriptions-item>
        <a-descriptions-item label="Trace ID">{{ run.traceId || '-' }}</a-descriptions-item>
      </a-descriptions>

      <a-descriptions v-if="trustReport" bordered size="small" :column="descriptionColumns" class="run-descriptions" title="可信度分项">
        <a-descriptions-item label="模型身份">{{ identityStatusText(trustReport.identityStatus) }}</a-descriptions-item>
        <a-descriptions-item label="模型映射">{{ mappingStatusText(trustReport.mappingStatus) }}</a-descriptions-item>
        <a-descriptions-item label="Token 诚信">{{ usageIntegrityStatusText(trustReport.usageIntegrityStatus) }}</a-descriptions-item>
        <a-descriptions-item label="协议一致性">{{ protocolStatusText(trustReport.protocolStatus) }}</a-descriptions-item>
        <a-descriptions-item label="证据充分度">{{ evidenceStatusText(trustReport.evidenceStatus) }}（{{ trustReport.evidenceCoverage }}%）</a-descriptions-item>
        <a-descriptions-item label="受控样本">{{ trustReport.observationCount ?? 0 }} 个 observation / {{ trustReport.roundCount ?? 0 }} 轮</a-descriptions-item>
        <a-descriptions-item label="独立来源桶">{{ trustReport.independentSourceCount ?? 0 }}</a-descriptions-item>
        <a-descriptions-item label="身份特征">{{ trustReport.identityObservationCount ?? 0 }} 个 observation / {{ trustReport.pairedProbeCount ?? 0 }} 组配对</a-descriptions-item>
        <a-descriptions-item label="身份偏离">{{ identityDistanceText(trustReport) }}</a-descriptions-item>
        <a-descriptions-item label="模型配对距离">{{ pairedDistanceText(trustReport) }}</a-descriptions-item>
        <a-descriptions-item label="群体基线">{{ baselineVersionText(trustReport) }}</a-descriptions-item>
        <a-descriptions-item label="Token 差分">{{ tokenDifferentialText(trustReport) }}</a-descriptions-item>
        <a-descriptions-item label="Tokenizer">{{ trustReport.tokenizerVersion || '尚无可用结果' }}</a-descriptions-item>
        <a-descriptions-item label="诊断依据">{{ reasonCodesText(trustReport.reasonCodes) }}</a-descriptions-item>
        <a-descriptions-item label="请求 / 上游 / 响应模型">
          {{ trustReport.requestedModel || '-' }} / {{ trustReport.mappedUpstreamModel || '-' }} / {{ trustReport.observedModel || '-' }}
        </a-descriptions-item>
      </a-descriptions>

      <div v-if="run.checks.length" class="check-list">
        <div v-for="check in run.checks" :key="check.id" class="check-item">
          <div class="check-item-head">
            <span>{{ checkTitle(check) }}</span>
            <a-space wrap>
              <a-tag :color="checkStatusColor(check.status)">{{ checkStatusText(check.status) }}</a-tag>
              <a-tag>{{ check.score }} / {{ check.maxScore }}</a-tag>
            </a-space>
          </div>
          <div v-if="checkMessage(check)" class="check-message">{{ checkMessage(check) }}</div>
          <pre v-if="hasCheckExtra(check)" class="json-block">{{ formatJson(checkExtra(check)) }}</pre>
        </div>
      </div>

      <pre class="json-block">{{ formatJson({ request: run.requestSummary, result: run.resultSummary }) }}</pre>
    </div>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { formatDateTime } from '@/shared/formatters'
import type { ModelCheckOption, ModelCheckRunDetail, ModelCheckRunSummary, ModelCheckTrustReport } from '@/types/domain'
import {
  checkExtra,
  checkMessage,
  checkStatusColor,
  checkStatusText,
  checkTitle,
  evidenceCompletenessText,
  formatModelCheckDuration as formatDuration,
  formatModelCheckJson as formatJson,
  hasCheckExtra,
  levelColor,
  levelText,
  modelCheckModelText,
  runTrustedComparison,
  statusColor,
  statusText
} from './modelCheckFormatters'

const props = defineProps<{
  descriptionColumns: number
  loading: boolean
  open: boolean
  run?: ModelCheckRunDetail
  supportedModels: ModelCheckOption[]
  targetDisplayName: (run: Pick<ModelCheckRunSummary, 'targetName' | 'targetId'>) => string
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
}>()

const trustReport = computed(() => {
  const value = props.run?.resultSummary?.trustReport
  return value && typeof value === 'object' ? value as ModelCheckTrustReport : undefined
})

const identityStatusText = (value: ModelCheckTrustReport['identityStatus']) => ({
  consistent: '当前受控探针一致', suspected_downgrade: '疑似降级', suspected_same_source: '疑似同源', population_outlier: '群体离群', insufficient_evidence: '证据不足'
}[value])
const mappingStatusText = (value: ModelCheckTrustReport['mappingStatus']) => ({
  direct: '直接请求', configured_mapping: '已配置模型映射', undeclared_mismatch: '响应模型与请求不一致', unknown: '未知'
}[value])
const usageIntegrityStatusText = (value: ModelCheckTrustReport['usageIntegrityStatus']) => ({
  consistent: '输入 Token 差分一致', warning: '输入 Token 需关注', suspected_padding: '疑似输入 Token 灌水', unsupported: '输入 Token 不支持检测', insufficient_evidence: '证据不足'
}[value])
const protocolStatusText = (value: ModelCheckTrustReport['protocolStatus']) => ({
  consistent: '一致', warning: '部分异常', failed: '不一致', insufficient_evidence: '证据不足'
}[value])
const evidenceStatusText = (value: ModelCheckTrustReport['evidenceStatus']) => ({
  stable: '稳定基线', candidate: '候选基线', bootstrap: '初始基线', insufficient: '证据不足'
}[value])
const tokenDifferentialText = (report: ModelCheckTrustReport) => report.slope === undefined
  ? '尚无预聚合结果'
  : `斜率 ${report.slope.toFixed(4)}，截距 ${(report.intercept ?? 0).toFixed(2)}`
const identityDistanceText = (report: ModelCheckTrustReport) => report.identityDistance === undefined
  ? '尚无 leave-one-upstream-out 结果'
  : `稳健偏离 ${report.identityDistance.toFixed(3)}`
const pairedDistanceText = (report: ModelCheckTrustReport) => report.pairedDistance === undefined
  ? '尚无可比配对结果'
  : `当前 ${report.pairedDistance.toFixed(4)}，群体中位数 ${(report.pairedBaselineMedian ?? 0).toFixed(4)}，MAD ${(report.pairedBaselineMad ?? 0).toFixed(4)}`
const baselineVersionText = (report: ModelCheckTrustReport) => report.baselineVersion === undefined
  ? '尚未形成版本化基线'
  : `v${report.baselineVersion} / ${{ active: '生效中', drift_protected: '群体漂移保护', retired: '已退役' }[report.baselineVersionStatus ?? 'active']} / ${report.featureVersion || '未知特征版本'}`
const reasonCodesText = (codes: string[]) => codes.length
  ? codes.map((code) => ({
      proportional_padding: '差分斜率疑似比例灌水',
      slope_warning: '差分斜率偏离待复核',
      bucket_rounding: '上游用量疑似分桶取整',
      reported_usage_missing: '上游未返回完整输入 Token',
      reported_usage_incompatible: '上游 usage 口径与总输入不兼容',
      configured_model_mapping: '已配置模型映射',
      undeclared_response_model_mismatch: '响应模型与请求不一致',
      protocol_check_failed: '协议探针未通过',
      tokenizer_calibration_unavailable: '尚无 Tokenizer 校准结果',
      population_baseline_unavailable: '群体基线尚未形成',
      paired_models_collapsed: '配对模型行为距离异常收缩，疑似同源',
      closer_to_lower_model_baseline: '行为特征更接近较低版本模型基线',
      identity_population_outlier: '身份特征偏离 leave-one-upstream-out 群体基线',
      population_drift_protected: '群体发生共同漂移，已暂停强身份结论'
    }[code] ?? `未知原因码：${code}`)).join('；')
  : '未发现已知异常原因'

function modelText(value: string) {
  return modelCheckModelText(value, props.supportedModels)
}
</script>

<style scoped>
.model-checks-detail-drawer :deep(.ant-drawer-content-wrapper) {
  max-width: 100vw;
}

.run-detail {
  display: grid;
  gap: 14px;
}

.run-detail-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
}

.run-detail-title {
  color: #0f172a;
  font-size: 16px;
  font-weight: 700;
}

.run-detail-subtitle {
  margin-top: 4px;
  color: #64748b;
  font-size: 13px;
}

.run-descriptions {
  background: #fff;
}

.check-list {
  display: grid;
  gap: 10px;
}

.check-item {
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fbfdff;
}

.check-item-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  color: #0f172a;
  font-weight: 700;
}

.check-message {
  margin-top: 6px;
  color: #475569;
  font-size: 13px;
  line-height: 1.6;
}

.json-block {
  max-height: 320px;
  margin: 10px 0 0;
  padding: 12px;
  overflow: auto;
  color: #dbeafe;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 18px;
  white-space: pre-wrap;
  word-break: break-word;
  background: #0f172a;
  border-radius: 8px;
}

@media (max-width: 900px) {
  .run-detail-head,
  .check-item-head {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
