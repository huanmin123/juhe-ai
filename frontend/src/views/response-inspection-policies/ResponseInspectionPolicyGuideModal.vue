<template>
  <a-modal v-model:open="open" :title="title" width="900px" :footer="null">
    <div class="policy-guide">
      <p v-if="intro" class="guide-note guide-intro">{{ intro }}</p>

      <section class="guide-section">
        <h4>去哪里查依据</h4>
        <a-table :columns="guideSourceColumns" :data-source="responseInspectionPolicyGuideSources" :pagination="false" row-key="key" size="small" />
      </section>

      <section class="guide-section">
        <h4>字段怎么填</h4>
        <a-table :columns="guideFieldColumns" :data-source="responseInspectionPolicyGuideFields" :pagination="false" row-key="key" size="small" />
        <p class="guide-note">{{ matchNote }}</p>
      </section>

      <section class="guide-section">
        <h4>处置怎么选</h4>
        <a-table :columns="guideActionColumns" :data-source="responseInspectionPolicyGuideActions" :pagination="false" row-key="key" size="small" />
      </section>

      <section class="guide-section">
        <h4>常见响应结构</h4>
        <pre class="guide-code">{{ responseInspectionPolicyGuideExample }}</pre>
      </section>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import {
  responseInspectionPolicyGuideActions,
  responseInspectionPolicyGuideExample,
  responseInspectionPolicyGuideFields,
  responseInspectionPolicyGuideSources
} from './responseInspectionPolicyGuide'

const open = defineModel<boolean>('open', { required: true })

withDefaults(defineProps<{
  title: string
  intro?: string
  matchNote?: string
}>(), {
  intro: '',
  matchNote: '支持多值的匹配字段只用英文逗号或中文逗号分隔；同一个字段里的多个值是“任一命中”，不同字段之间是“同时命中”。输出文本排除为负向条件，任一命中即让本规则不命中。'
})

const guideSourceColumns = [
  { title: '来源', key: 'name', dataIndex: 'name', width: 120 },
  { title: '查看位置', key: 'where', dataIndex: 'where' },
  { title: '说明', key: 'note', dataIndex: 'note' }
]

const guideFieldColumns = [
  { title: '字段', key: 'field', dataIndex: 'field', width: 120 },
  { title: '取值来源', key: 'source', dataIndex: 'source' },
  { title: '例子', key: 'example', dataIndex: 'example', width: 180 },
  { title: '说明', key: 'note', dataIndex: 'note' }
]

const guideActionColumns = [
  { title: '处置', key: 'action', dataIndex: 'action', width: 150 },
  { title: '适用场景', key: 'when', dataIndex: 'when' },
  { title: '说明', key: 'note', dataIndex: 'note' }
]
</script>

<style scoped>
.policy-guide {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.policy-guide :deep(.ant-table-wrapper) {
  overflow-x: auto;
}

.guide-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.guide-section h4 {
  margin: 0;
  color: #0f172a;
  font-size: 14px;
}

.guide-note {
  color: #64748b;
  font-size: 12px;
  line-height: 20px;
}

.guide-intro {
  margin: 0;
}

.guide-code {
  overflow-x: auto;
  margin: 0;
  border: 1px solid #e8edf5;
  border-radius: 8px;
  background: #f8fafc;
  padding: 12px;
  color: #334155;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 20px;
}
</style>
