<template>
  <a-modal
    :open="open"
    width="960px"
    wrap-class-name="question-bank-modal-wrap"
    :footer="null"
    @update:open="emit('update:open', $event)"
  >
    <template #title>
      <div class="question-bank-modal-title">
        <span>{{ isManagementView ? '检测题库管理' : '检测题库' }}</span>
        <small>提交的题目经管理员审核通过后，才能在检测配置中选用（最多 3 题）。</small>
      </div>
    </template>

    <div class="question-bank-modal-content">
      <a-form ref="formRef" :model="form" class="question-bank-form" layout="vertical" @finish="submitForm">
        <section ref="questionEditorRef" class="question-editor">
          <header class="question-editor-head">
            <div>
              <h3>{{ editingItem ? '编辑题目' : '提交新题目' }}</h3>
              <p>标题不得与现有题目重复，题目内容与现有题目相似度过高会被拒绝。</p>
            </div>
            <a-tag v-if="editingItem" color="blue">正在编辑</a-tag>
          </header>

          <div class="question-form-grid">
            <a-form-item label="标题" name="title" :rules="[{ required: true, message: '请输入题目标题' }]">
              <a-input v-model:value="form.title" :maxlength="100" show-count placeholder="≤100 字符，提交后与现有题目查重" />
            </a-form-item>
            <a-form-item label="题目内容" name="questionText" :rules="[{ required: true, message: '请输入题目内容' }]">
              <a-textarea v-model:value="form.questionText" :maxlength="5000" show-count :rows="4" placeholder="发送给模型的完整题面，≤5000 字符" />
            </a-form-item>
            <a-form-item label="参考答案" name="referenceAnswer" :rules="[{ required: true, message: '请输入参考答案' }]">
              <a-textarea v-model:value="form.referenceAnswer" :maxlength="4000" show-count :rows="4" placeholder="模型作答的判定依据，全员可见，≤4000 字符" />
            </a-form-item>
            <a-form-item name="keyPoints">
              <template #label>
                <span class="question-label-with-help">
                  评分要点（选填）
                  <a-tooltip title="评分要点是判分时的关键检查点：检测评分会把要点连同参考答案一起用于判定，模型的回答需要覆盖这些要点才算答对。例如翻译题可填「包含简体中文译文」「包含日文译文」。最多 10 条、每条不超过 50 字；不填则按参考答案整体判定。">
                    <QuestionCircleOutlined class="question-label-help-icon" aria-label="评分要点说明" />
                  </a-tooltip>
                </span>
              </template>
              <a-select
                v-model:value="form.keyPoints"
                mode="tags"
                :open="false"
                :token-separators="[',', '，']"
                placeholder="每条写一个关键检查点，回车或逗号分隔"
                @change="handleKeyPointsChange"
              />
            </a-form-item>
          </div>

          <section class="question-test-section">
            <div class="question-test-head">
              <h4>题目可用性测试</h4>
              <p>选择账户与模型试答本题，模型答对（通过）后才能提交；修改题目内容后需重新测试。</p>
            </div>
            <div class="question-test-grid">
              <a-select
                v-model:value="testAccountId"
                class="question-test-account"
                show-search
                allow-clear
                :filter-option="false"
                :loading="testAccountOptionsLoading"
                :options="testAccountOptions"
                placeholder="选择用于测试的账户"
                @change="handleTestAccountChange"
                @dropdown-visible-change="handleTestAccountDropdownVisibleChange"
                @search="handleTestAccountSearch"
              />
              <a-select
                v-model:value="testModel"
                class="question-test-model"
                :loading="testModelOptionsLoading"
                :options="testModelOptions"
                :disabled="!testAccountId || testModelOptions.length === 0"
                :placeholder="testModelPlaceholder"
              />
              <a-button class="question-test-run" :loading="testing" @click="runQuestionTest">测试题目</a-button>
            </div>
            <p v-if="editingItem?.lastTest && !lastTestResult" class="question-test-history">
              上次通过测试：{{ editingItem.lastTest.accountName }} · {{ editingItem.lastTest.model }} · {{ questionBankDateTimeText(editingItem.lastTest.passedAt) }}；修改内容后需重新测试。
            </p>
            <p v-if="!lastTestResult" class="question-test-placeholder">{{ testPlaceholderText }}</p>
            <div v-else class="question-test-result">
              <div class="question-test-status">
                <a-tag :color="questionTestVerdictColor(lastTestResult.verdict)">{{ questionTestVerdictText(lastTestResult.verdict) }}</a-tag>
                <span class="question-test-status-meta">
                  {{ lastTestResult.accountName }} · {{ lastTestResult.model }} · 总耗时 <span class="question-test-mono">{{ formatModelCheckDuration(lastTestResult.latencyMs) }}</span>
                </span>
              </div>
              <div
                v-for="(note, index) in testResultNotes"
                :key="index"
                class="question-test-note"
                :class="`question-test-note-${note.tone}`"
              >{{ note.text }}</div>
              <ul v-if="testPointRows.length" class="question-test-points">
                <li v-for="(row, index) in testPointRows" :key="index" class="question-test-point">
                  <span v-if="row.met !== undefined" class="question-test-point-state" :class="row.met ? 'is-met' : 'is-unmet'">
                    <i class="question-test-point-dot" aria-hidden="true"></i>{{ row.met ? '已覆盖' : '未覆盖' }}
                  </span>
                  <span class="question-test-point-text">{{ row.point }}</span>
                  <span v-if="row.note" class="question-test-point-note">{{ row.note }}</span>
                </li>
              </ul>
              <div v-if="lastTestResult.answerExcerpt" class="question-test-answer">
                <p class="question-test-answer-text" :class="{ 'is-collapsed': !testAnswerExpanded }">{{ lastTestResult.answerExcerpt }}</p>
                <a-button type="link" size="small" class="question-test-answer-toggle" @click="testAnswerExpanded = !testAnswerExpanded">
                  {{ testAnswerExpanded ? '收起' : '展开查看' }}
                </a-button>
              </div>
            </div>
          </section>

          <footer class="question-form-actions">
            <span v-if="submitHint" class="question-test-submit-hint">{{ submitHint }}</span>
            <a-button v-if="editingItem" :disabled="submitting" @click="resetForm">取消编辑</a-button>
            <a-button type="primary" html-type="submit" :loading="submitting" :disabled="!testPassed">
              {{ editingItem ? '保存修改' : '提交题目' }}
            </a-button>
          </footer>
        </section>
      </a-form>

      <section class="question-list-section">
        <header class="question-list-head">
          <div>
            <h3>题库列表</h3>
            <p>共 {{ total }} 道题目{{ isManagementView ? '，可审核与删除任意题目' : '，展示已通过题目与本人提交' }}。</p>
          </div>
          <a-space wrap>
            <a-input-search
              v-model:value="keyword"
              class="question-keyword-input"
              allow-clear
              placeholder="按标题搜索"
              @search="handleKeywordSearch"
            />
            <a-select
              v-model:value="statusFilter"
              class="question-status-select"
              :options="statusFilterOptions"
              @change="handleStatusFilterChange"
            />
          </a-space>
        </header>

        <a-spin :spinning="loading">
          <a-empty v-if="!items.length" class="question-empty" :image="simpleEmptyImage" description="还没有符合条件的题目">
            <span class="question-empty-help">完成上方表单后点击“提交题目”，题目通过审核即可在检测配置中选用。</span>
          </a-empty>
          <div v-else class="question-list">
            <article v-for="item in items" :key="item.id" class="question-item">
              <div class="question-item-main">
                <div class="question-item-title-row">
                  <h4>{{ item.title }}</h4>
                  <a-tag v-if="item.isBuiltin" color="geekblue">内置</a-tag>
                  <a-tag :color="questionStatusColor(item.status)">{{ questionStatusText(item.status) }}</a-tag>
                </div>
                <p class="question-item-text">{{ item.questionText }}</p>
                <div class="question-item-meta">
                  <span v-if="isManagementView">提交人 {{ questionCreatorText(item) }}</span>
                  <span>提交于 {{ questionBankDateTimeText(item.createdAt) }}</span>
                  <span v-if="item.reviewedAt">审核于 {{ questionBankDateTimeText(item.reviewedAt) }}</span>
                  <span v-if="item.lastTest">已测试 通过 · {{ item.lastTest.accountName }} · {{ item.lastTest.model }}</span>
                  <span v-if="item.status === 'rejected' && item.rejectReason" class="question-item-reject">驳回理由：{{ item.rejectReason }}</span>
                </div>
              </div>
              <RowActions class="question-item-actions" :actions="rowActionsFor(item)" @action-click="handleRowAction($event, item)" />
            </article>
          </div>

          <a-pagination
            v-if="total > pageSize"
            class="question-pagination"
            :current="page"
            :page-size="pageSize"
            :total="total"
            show-less-items
            @change="handlePageChange"
          />
        </a-spin>
      </section>
    </div>

    <a-drawer
      v-model:open="detailOpen"
      class="question-detail-drawer"
      title="题目详情"
      width="520px"
      :body-style="{ padding: '16px' }"
    >
      <a-skeleton v-if="detailLoading" active :paragraph="{ rows: 5 }" />
      <a-empty v-else-if="!detailItem" description="尚未选择题目" />
      <div v-else class="question-detail">
        <div class="question-detail-head">
          <div>
            <div class="question-detail-title">{{ detailItem.title }}</div>
            <div class="question-detail-subtitle">
              <template v-if="isManagementView">提交人 {{ questionCreatorText(detailItem) }} · </template>提交于 {{ questionBankDateTimeText(detailItem.createdAt) }}
            </div>
          </div>
          <a-tag v-if="detailItem.isBuiltin" color="geekblue">内置</a-tag>
          <a-tag :color="questionStatusColor(detailItem.status)">{{ questionStatusText(detailItem.status) }}</a-tag>
        </div>
        <a-alert
          v-if="detailItem.status === 'rejected' && detailItem.rejectReason"
          class="question-detail-alert"
          type="error"
          show-icon
          :message="`驳回理由：${detailItem.rejectReason}`"
        />
        <div class="question-detail-block">
          <div class="question-detail-label">题目内容</div>
          <p class="question-detail-text">{{ detailItem.questionText }}</p>
        </div>
        <div class="question-detail-block">
          <div class="question-detail-label">参考答案</div>
          <p v-if="detailItem.referenceAnswer" class="question-detail-text">{{ detailItem.referenceAnswer }}</p>
          <p v-else class="question-detail-text question-detail-muted">提交者未填写。</p>
        </div>
        <div class="question-detail-block">
          <div class="question-detail-label">
            <span class="question-label-with-help">
              评分要点
              <a-tooltip title="判分时的关键检查点：检测评分会把要点连同参考答案一起用于判定，模型的回答需要覆盖这些要点才算答对。">
                <QuestionCircleOutlined class="question-label-help-icon" aria-label="评分要点说明" />
              </a-tooltip>
            </span>
          </div>
          <a-space v-if="detailItem.keyPoints.length" wrap>
            <a-tag v-for="(point, index) in detailItem.keyPoints" :key="index" color="blue">{{ point }}</a-tag>
          </a-space>
          <p v-else class="question-detail-text question-detail-muted">提交者未填写。</p>
        </div>
        <a-descriptions bordered size="small" :column="1" class="question-detail-descriptions">
          <a-descriptions-item v-if="isManagementView" label="提交人">{{ questionCreatorText(detailItem) }}</a-descriptions-item>
          <a-descriptions-item v-if="detailItem.lastTest" label="可用性测试">已测试 通过 · {{ detailItem.lastTest.accountName }} · {{ detailItem.lastTest.model }}</a-descriptions-item>
          <a-descriptions-item label="提交时间">{{ questionBankDateTimeText(detailItem.createdAt) }}</a-descriptions-item>
          <a-descriptions-item label="更新时间">{{ questionBankDateTimeText(detailItem.updatedAt) }}</a-descriptions-item>
          <a-descriptions-item label="审核时间">{{ detailItem.reviewedAt ? questionBankDateTimeText(detailItem.reviewedAt) : '-' }}</a-descriptions-item>
          <a-descriptions-item label="审核人">{{ isManagementView && detailItem.reviewedBy ? detailItem.reviewedBy : '-' }}</a-descriptions-item>
        </a-descriptions>
      </div>
    </a-drawer>

    <a-modal
      v-model:open="rejectOpen"
      title="驳回题目"
      :confirm-loading="reviewSubmitting"
      ok-text="确认驳回"
      cancel-text="取消"
      @ok="confirmReject"
    >
      <p class="question-reject-hint">审核时请检查题目是否存在提示注入等异常特征。</p>
      <a-form-item label="驳回理由" required class="question-reject-field">
        <a-textarea v-model:value="rejectReason" :maxlength="200" show-count :rows="3" placeholder="驳回后创建者可修改并重新提交" />
      </a-form-item>
    </a-modal>
  </a-modal>
</template>

<script setup lang="ts">
import { Empty } from 'ant-design-vue'
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { computed, nextTick, onBeforeUnmount, reactive, ref, toRef, watch } from 'vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { useScopedModelCheckAccountOptionsApi, useScopedModelChecksApi } from '@/composables/useScopedDomainApi'
import { authState } from '@/composables/useAuth'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { ModelCheckAccountOption, ModelCheckQuestionBankItem, ModelCheckQuestionBankTestResult, ModelCheckQuestionStatus } from '@/types/domain'
import {
  formatModelCheckDuration,
  questionBankDateTimeText,
  questionCreatorText,
  questionDraftKey,
  questionStatusColor,
  questionStatusText,
  questionTestVerdictColor,
  questionTestVerdictText
} from './modelCheckFormatters'
import { canSelectModelCheckAccount, modelCheckModelsForAccount, type ModelCheckAccountProfile } from './modelCheckProviderCapabilities'

const props = defineProps<{
  isManagementView: boolean
  open: boolean
  /** 管理面系统账户过滤范围，与检测页 run 面板的 scope 保持一致；自助面忽略 */
  modelCheckScopeParams?: { systemAccountId?: string }
}>()
const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
}>()

const scopedApi = useScopedModelChecksApi(toRef(props, 'isManagementView'))
const testAccountOptionsApi = useScopedModelCheckAccountOptionsApi(toRef(props, 'isManagementView'))
const simpleEmptyImage = Empty.PRESENTED_IMAGE_SIMPLE
const pageSize = 10

const form = reactive({
  title: '',
  questionText: '',
  referenceAnswer: '',
  keyPoints: [] as string[]
})
const formRef = ref<{ clearValidate: () => void }>()
const questionEditorRef = ref<HTMLElement>()
const editingItem = ref<ModelCheckQuestionBankItem>()
const submitting = ref(false)
const loading = ref(false)
const items = ref<ModelCheckQuestionBankItem[]>([])
const total = ref(0)
const page = ref(1)
const keyword = ref('')
const statusFilter = ref<'' | ModelCheckQuestionStatus>('')
let listRequestId = 0
let keywordTimer: ReturnType<typeof setTimeout> | undefined

const detailOpen = ref(false)
const detailLoading = ref(false)
const detailItem = ref<ModelCheckQuestionBankItem>()

const rejectOpen = ref(false)
const rejectReason = ref('')
const rejectTarget = ref<ModelCheckQuestionBankItem>()
const reviewSubmitting = ref(false)

// ---- 题目可用性测试 + 提交门禁状态机 ----
// 门禁契约：仅当「最近一次测试 verdict=pass 且当前表单内容指纹与测试时一致」才解锁提交。
const testAccountId = ref('')
const testModel = ref('')
const testing = ref(false)
const lastTestResult = ref<ModelCheckQuestionBankTestResult>()
// 测试通过时的内容指纹；与当前指纹不一致说明内容已改，门禁重新上锁
const lastTestDraftKey = ref('')
const testAccountOptions = ref<ModelCheckAccountOption[]>([])
const testAccountProfilesById = ref<Record<string, ModelCheckAccountProfile>>({})
const testAccountOptionsLoading = ref(false)
const testModelOptionsLoading = ref(false)
const testAnswerExpanded = ref(false)
let testAccountOptionsRequestId = 0
let testAccountOptionsTimer: ReturnType<typeof setTimeout> | undefined
let testAccountOptionsAbortController: AbortController | undefined
let testModelOptionsRequestId = 0

const currentTestDraftKey = computed(() => questionDraftKey(form.questionText, form.referenceAnswer, form.keyPoints))
const testPassed = computed(() => {
  const result = lastTestResult.value
  return result?.verdict === 'pass' && lastTestDraftKey.value === currentTestDraftKey.value
})
const submitHint = computed(() => {
  if (testPassed.value) return ''
  if (lastTestResult.value?.verdict === 'pass') return '内容已修改，请重新测试'
  return '测试通过后才能提交'
})

const testAccountSelectOptions = computed(() => testAccountOptions.value
  .filter((account) => canSelectModelCheckAccount(account))
  .map((account) => ({ label: account.name, value: account.id })))

const selectedTestAccountProfile = computed(() => testAccountProfilesById.value[testAccountId.value])

// 模型候选仅来自当前账户：profile 已带 modelCheckModels 直接用；尚未按需补拉时
// modelCheckModelsForAccount 回退能力规则目录，补拉完成后收敛为账户真实候选。
const testModelOptions = computed(() => modelCheckModelsForAccount(selectedTestAccountProfile.value)
  .map((model) => ({ label: model, value: model })))

const testModelPlaceholder = computed(() => {
  if (!testAccountId.value) return '先选择账户'
  return testModelOptions.value.length ? '选择测试模型' : '该账户暂无可用检测模型'
})

const testPlaceholderText = computed(() => {
  if (!form.questionText.trim() || !form.referenceAnswer.trim()) return '填写题目内容与参考答案后即可测试。'
  return '选择账户与模型，点击「测试题目」开始可用性测试。'
})

interface QuestionTestNote {
  tone: 'ok' | 'warn' | 'danger'
  text: string
}

// 分类提示：按 pass → request_failed → invalid → fail(题目失焦/要点未覆盖/其余) 的顺序归因，
// 文案始终承载语义，颜色（洗底）只作辅助。
const testResultNotes = computed<QuestionTestNote[]>(() => {
  const result = lastTestResult.value
  if (!result) return []
  if (result.verdict === 'pass') {
    return [{ tone: 'ok', text: '测试通过：模型答对了本题，可以提交。' }]
  }
  if (result.verdict === 'request_failed') {
    const detail = result.httpStatus ?? result.error?.trim() ?? result.verdictReason?.trim() ?? '未知错误'
    return [{ tone: 'warn', text: `上游请求未成功（${detail}），属账户或网络问题。可换账户或模型重试，无需修改题目。` }]
  }
  if (result.verdict === 'invalid') {
    const note = result.graderNote?.trim()
    return [{ tone: 'danger', text: `无法判定：参考答案或评分要点的写法让裁判无法对比。${note ? ` ${note}` : ''}`.trim() }]
  }
  if (result.questionAlignment === 'misaligned') {
    const reason = result.verdictReason?.trim()
    return [{ tone: 'danger', text: `题目问题：题面与参考答案似乎不是同一件事。${reason ? ` ${reason}` : ''}`.trim() }]
  }
  const uncovered = result.keyPointResults.filter((item) => !item.met)
  if (uncovered.length) {
    return uncovered.map((item) => {
      const note = item.note?.trim()
      return {
        tone: 'warn' as const,
        text: `模型未覆盖要点「${item.point}」${note ? `：${note}` : ''}。若该要点表述有歧义请修改；若题目无误，换更强模型重测。`
      }
    })
  }
  return [{ tone: 'warn', text: '题目配置自洽但当前模型答错。若使用的是较弱模型，换更强模型重测即可；本题通常无需修改。' }]
})

// 逐要点行：优先展示后端逐要点判定；后端未返回时回退表单要点（无判定，不标已/未覆盖）。
const testPointRows = computed<Array<{ point: string; met?: boolean; note?: string }>>(() => {
  const result = lastTestResult.value
  if (!result) return []
  if (result.keyPointResults.length) {
    return result.keyPointResults.map((item) => ({ point: item.point, met: item.met, note: item.note?.trim() || undefined }))
  }
  return form.keyPoints.map((point) => ({ point }))
})

function rememberTestAccountProfile(account: ModelCheckAccountOption) {
  const existing = testAccountProfilesById.value[account.id]
  const profile: ModelCheckAccountProfile = {
    id: account.id,
    name: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion
  }
  const models = account.modelCheckModels ?? existing?.modelCheckModels
  if (models !== undefined) profile.modelCheckModels = [...models]
  testAccountProfilesById.value = { ...testAccountProfilesById.value, [account.id]: profile }
}

async function loadTestAccountOptions(keyword = '') {
  const normalizedKeyword = keyword.trim()
  const requestId = ++testAccountOptionsRequestId
  testAccountOptionsAbortController?.abort()
  const controller = new AbortController()
  testAccountOptionsAbortController = controller
  testAccountOptionsLoading.value = true
  try {
    const accounts = await testAccountOptionsApi.options({
      purpose: 'run',
      systemAccountId: props.modelCheckScopeParams?.systemAccountId,
      keyword: normalizedKeyword || undefined,
      limit: 50,
      selectedIds: [testAccountId.value].filter(Boolean)
    }, { signal: controller.signal })
    if (requestId !== testAccountOptionsRequestId || controller.signal.aborted) return
    testAccountOptions.value = accounts
    for (const account of accounts) rememberTestAccountProfile(account)
  } catch (error) {
    if (requestId !== testAccountOptionsRequestId || controller.signal.aborted) return
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载测试账户失败'))
  } finally {
    if (requestId === testAccountOptionsRequestId) testAccountOptionsLoading.value = false
  }
}

// 选中账户后 modelCheckModels 缺失时按需补拉一次并缓存（照检测面板的补拉口径）。
async function ensureTestAccountModels(accountId: string) {
  if (!accountId || testAccountProfilesById.value[accountId]?.modelCheckModels !== undefined) return
  const requestId = ++testModelOptionsRequestId
  testModelOptionsLoading.value = true
  try {
    const accounts = await testAccountOptionsApi.options({
      purpose: 'run',
      systemAccountId: props.modelCheckScopeParams?.systemAccountId,
      accountId,
      limit: 1
    })
    if (requestId !== testModelOptionsRequestId || accountId !== testAccountId.value) return
    const account = accounts.find((item) => item.id === accountId)
    if (!account) return
    rememberTestAccountProfile(account)
  } catch (error) {
    if (requestId !== testModelOptionsRequestId || accountId !== testAccountId.value) return
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载账户检测模型失败'))
  } finally {
    if (requestId === testModelOptionsRequestId) testModelOptionsLoading.value = false
  }
}

function handleTestAccountChange(value: unknown) {
  // allow-clear 清空时 antd 给 undefined，归一为空串保持 ref 恒为 string
  const accountId = typeof value === 'string' ? value : ''
  testAccountId.value = accountId
  testModel.value = ''
  if (accountId) void ensureTestAccountModels(accountId)
}

function handleTestAccountDropdownVisibleChange(open: boolean) {
  if (!open) return
  clearTimeout(testAccountOptionsTimer)
  void loadTestAccountOptions()
}

function handleTestAccountSearch(keyword: string) {
  clearTimeout(testAccountOptionsTimer)
  testAccountOptionsTimer = setTimeout(() => void loadTestAccountOptions(keyword), 250)
}

async function runQuestionTest() {
  if (testing.value) return
  const questionText = form.questionText.trim()
  const referenceAnswer = form.referenceAnswer.trim()
  if (!testAccountId.value) {
    message.warning('请先选择测试账户')
    return
  }
  if (!testModel.value) {
    message.warning('请先选择测试模型')
    return
  }
  if (!questionText || !referenceAnswer) {
    message.warning('请先填写题目内容与参考答案')
    return
  }
  // 指纹在请求前捕获：测试期间改动内容，结果也不会解锁门禁
  const draftKey = questionDraftKey(form.questionText, form.referenceAnswer, form.keyPoints)
  testing.value = true
  try {
    const result = await scopedApi.questionBankTest({
      questionText,
      referenceAnswer,
      keyPoints: form.keyPoints.length ? [...form.keyPoints] : undefined,
      accountId: testAccountId.value,
      model: testModel.value
    }, props.modelCheckScopeParams)
    lastTestResult.value = result
    lastTestDraftKey.value = draftKey
    testAnswerExpanded.value = false
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '题目可用性测试失败'))
  } finally {
    testing.value = false
  }
}

function resetTestState() {
  lastTestResult.value = undefined
  lastTestDraftKey.value = ''
  testAnswerExpanded.value = false
}

onBeforeUnmount(() => {
  clearTimeout(testAccountOptionsTimer)
  testAccountOptionsRequestId += 1
  testAccountOptionsAbortController?.abort()
  testModelOptionsRequestId += 1
})

const statusFilterOptions = computed(() => {
  const options: Array<{ label: string; value: '' | ModelCheckQuestionStatus }> = [{ label: '全部状态', value: '' }]
  for (const item of [
    { label: '待审核', value: 'pending' as const },
    { label: '已通过', value: 'approved' as const },
    { label: '已驳回', value: 'rejected' as const }
  ]) options.push(item)
  return options
})

const canReview = computed(() => props.isManagementView)

watch(() => props.open, (open) => {
  if (!open) {
    clearTimeout(keywordTimer)
    listRequestId += 1
    loading.value = false
    resetForm()
    detailOpen.value = false
    rejectOpen.value = false
    return
  }
  page.value = 1
  keyword.value = ''
  statusFilter.value = props.isManagementView ? 'pending' : ''
  resetForm()
  void loadQuestions()
})

function rowActionsFor(item: ModelCheckQuestionBankItem): RowActionItem[] {
  const actions: RowActionItem[] = [{ key: 'detail', label: '详情', icon: 'detail' }]
  if (item.isBuiltin) return actions
  if (canEdit(item)) {
    actions.push({ key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' })
  }
  if (canDelete(item)) {
    actions.push({ key: 'delete', label: '删除', icon: 'delete', tone: 'danger', confirmTitle: `确认删除题目「${item.title}」？删除后不可恢复。` })
  }
  if (canReview.value && item.status === 'pending') {
    actions.push(
      { key: 'approve', label: '审核通过', icon: 'restore', tone: 'success', confirmTitle: `确认通过题目「${item.title}」？通过后可在检测配置中选用。审核时请检查题目是否存在提示注入等异常特征。` },
      { key: 'reject', label: '驳回', icon: 'stop', tone: 'warning' }
    )
  }
  if (canReview.value && item.status === 'approved') {
    actions.push({ key: 'delist', label: '下架', icon: 'stop', tone: 'warning', confirmTitle: `确认下架题目「${item.title}」？下架后不再出现在检测配置的可选题中，创建者修改后可重新提交审核。审核时请检查题目是否存在提示注入等异常特征。` })
  }
  return actions
}

function isOwn(item: ModelCheckQuestionBankItem): boolean {
  const currentUserId = authState.currentUser.value?.id
  return Boolean(currentUserId) && item.createdBy === currentUserId
}

function canEdit(item: ModelCheckQuestionBankItem): boolean {
  if (item.isBuiltin) return false
  return isOwn(item) && (item.status === 'pending' || item.status === 'rejected')
}

function canDelete(item: ModelCheckQuestionBankItem): boolean {
  if (item.isBuiltin) return false
  if (props.isManagementView) return true
  return isOwn(item) && (item.status === 'pending' || item.status === 'rejected')
}

function handleRowAction(action: string, item: ModelCheckQuestionBankItem) {
  if (action === 'detail') void openDetail(item)
  if (action === 'edit') startEdit(item)
  if (action === 'delete') void removeQuestion(item)
  if (action === 'approve') void approveQuestion(item)
  if (action === 'reject' || action === 'delist') startReject(item)
}

async function loadQuestions() {
  const requestId = ++listRequestId
  loading.value = true
  try {
    const result = await scopedApi.questionBankList({
      status: statusFilter.value || undefined,
      keyword: keyword.value.trim() || undefined,
      page: page.value,
      pageSize
    })
    if (requestId !== listRequestId) return
    items.value = result.items
    total.value = result.total
  } catch (error) {
    if (requestId !== listRequestId) return
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载题库列表失败'))
  } finally {
    if (requestId === listRequestId) loading.value = false
  }
}

function handleKeywordSearch() {
  clearTimeout(keywordTimer)
  page.value = 1
  void loadQuestions()
}

watch(keyword, () => {
  clearTimeout(keywordTimer)
  keywordTimer = setTimeout(() => {
    page.value = 1
    void loadQuestions()
  }, 300)
})

function handleStatusFilterChange() {
  page.value = 1
  void loadQuestions()
}

function handlePageChange(nextPage: number) {
  page.value = nextPage
  void loadQuestions()
}

async function submitForm() {
  // 门禁兜底：按钮已按 testPassed 禁用，此处防御键盘隐式提交等旁路
  if (!testPassed.value || !lastTestResult.value) {
    message.warning('请先完成可用性测试，测试通过后才能提交')
    return
  }
  const payload = {
    title: form.title.trim(),
    questionText: form.questionText.trim(),
    referenceAnswer: form.referenceAnswer.trim(),
    keyPoints: form.keyPoints.length ? [...form.keyPoints] : undefined,
    testId: lastTestResult.value.testId
  }
  if (!payload.title || !payload.questionText || !payload.referenceAnswer) {
    message.warning('请完整填写标题、题目内容与参考答案')
    return
  }
  submitting.value = true
  try {
    if (editingItem.value) {
      await scopedApi.questionBankUpdate(editingItem.value.id, payload)
      message.success('题目已保存，重新进入待审核')
    } else {
      await scopedApi.questionBankCreate(payload)
      message.success('题目已提交，等待管理员审核')
    }
    resetForm()
    page.value = 1
    await loadQuestions()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '提交题目失败'))
  } finally {
    submitting.value = false
  }
}

function startEdit(item: ModelCheckQuestionBankItem) {
  editingItem.value = item
  form.title = item.title
  form.questionText = item.questionText
  form.referenceAnswer = item.referenceAnswer
  form.keyPoints = [...item.keyPoints]
  // 编辑已有题目一律重新测试：历史 lastTest 仅作快照展示，不作为门禁依据
  resetTestState()
  void nextTick(() => questionEditorRef.value?.scrollIntoView({ block: 'start' }))
}

function resetForm() {
  editingItem.value = undefined
  form.title = ''
  form.questionText = ''
  form.referenceAnswer = ''
  form.keyPoints = []
  formRef.value?.clearValidate()
  // 重置测试状态；账户与模型选择保留，便于连续提交多道题目
  resetTestState()
}

function handleKeyPointsChange(value: unknown) {
  const list = Array.isArray(value) ? value.map((item) => String(item).trim()).filter(Boolean) : []
  const normalized = [...new Set(list.map((item) => item.slice(0, 50)))]
  if (normalized.length > 10) {
    message.warning('评分要点最多 10 条')
  }
  form.keyPoints = normalized.slice(0, 10)
}

async function openDetail(item: ModelCheckQuestionBankItem) {
  detailOpen.value = true
  detailLoading.value = true
  detailItem.value = undefined
  try {
    detailItem.value = await scopedApi.questionBankDetail(item.id)
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载题目详情失败'))
  } finally {
    detailLoading.value = false
  }
}

async function removeQuestion(item: ModelCheckQuestionBankItem) {
  try {
    await scopedApi.questionBankDelete(item.id)
    message.success('题目已删除')
    if (items.value.length === 1 && page.value > 1) page.value -= 1
    await loadQuestions()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '删除题目失败'))
  }
}

async function approveQuestion(item: ModelCheckQuestionBankItem) {
  reviewSubmitting.value = true
  try {
    await scopedApi.questionBankReview(item.id, { action: 'approve' })
    message.success('题目已通过审核')
    await loadQuestions()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '审核题目失败'))
  } finally {
    reviewSubmitting.value = false
  }
}

function startReject(item: ModelCheckQuestionBankItem) {
  rejectTarget.value = item
  rejectReason.value = ''
  rejectOpen.value = true
}

async function confirmReject() {
  const target = rejectTarget.value
  if (!target) return
  const reason = rejectReason.value.trim()
  if (!reason) {
    message.warning('请填写驳回理由')
    return
  }
  reviewSubmitting.value = true
  try {
    await scopedApi.questionBankReview(target.id, { action: 'reject', reason })
    message.success('已驳回该题目')
    rejectOpen.value = false
    rejectTarget.value = undefined
    await loadQuestions()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '审核题目失败'))
  } finally {
    reviewSubmitting.value = false
  }
}
</script>

<style scoped>
.question-bank-modal-title {
  display: grid;
  gap: 3px;
}

.question-bank-modal-title > span {
  color: var(--juhe-fg);
  font-size: 17px;
  font-weight: 600;
  line-height: 24px;
}

.question-bank-modal-title small {
  color: var(--juhe-muted);
  font-size: 12px;
  font-weight: 400;
  line-height: 18px;
}

.question-bank-modal-content {
  display: grid;
  gap: 0;
}

.question-editor-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  padding-bottom: 14px;
}

.question-editor h3,
.question-list-head h3,
.question-item h4 {
  margin: 0;
  color: var(--juhe-fg);
}

.question-editor h3,
.question-list-head h3 {
  font-size: 15px;
  font-weight: 600;
  line-height: 22px;
}

.question-editor-head p,
.question-list-head p {
  margin: 3px 0 0;
  color: var(--juhe-muted);
  font-size: 12px;
  line-height: 18px;
}

.question-form-grid {
  display: grid;
  gap: 4px;
}

.question-label-with-help {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.question-label-help-icon {
  color: var(--juhe-muted);
  cursor: help;
}

.question-bank-form :deep(.ant-form-item) {
  min-width: 0;
  margin-bottom: 14px;
}

.question-bank-form :deep(.ant-select),
.question-bank-form :deep(.ant-input) {
  width: 100%;
}

.question-form-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  flex-wrap: wrap;
  gap: 8px;
  padding: 12px 0 0;
  border-top: 1px solid var(--juhe-border);
}

.question-test-submit-hint {
  margin-right: auto;
  color: var(--juhe-muted);
  font-size: 12px;
  line-height: 20px;
}

.question-test-section {
  margin-top: 4px;
  padding-top: 16px;
  border-top: 1px solid var(--juhe-border);
}

.question-test-head h4 {
  margin: 0;
  color: var(--juhe-fg);
  font-size: 14px;
  font-weight: 600;
  line-height: 22px;
}

.question-test-head p {
  margin: 3px 0 0;
  color: var(--juhe-muted);
  font-size: 12px;
  line-height: 18px;
}

.question-test-grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) auto;
  gap: 8px;
  align-items: center;
  margin-top: 10px;
}

.question-test-grid :deep(.ant-select) {
  width: 100%;
}

.question-test-history,
.question-test-placeholder {
  margin: 10px 0 0;
  font-size: 12px;
  line-height: 18px;
}

.question-test-history {
  color: var(--juhe-faint);
}

.question-test-placeholder {
  color: var(--juhe-muted);
}

.question-test-result {
  display: grid;
  gap: 10px;
  margin-top: 10px;
}

.question-test-status {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.question-test-status-meta {
  color: var(--juhe-fg-soft);
  font-size: 12px;
  line-height: 20px;
}

.question-test-mono {
  font-family: var(--juhe-font-mono);
}

.question-test-note {
  padding: 8px 12px;
  border: 1px solid var(--juhe-border);
  border-radius: var(--juhe-radius-sm);
  color: var(--juhe-fg-soft);
  font-size: 12px;
  line-height: 20px;
  word-break: break-word;
}

.question-test-note-ok {
  background: var(--juhe-ok-soft);
  border-color: var(--juhe-ok-line);
}

.question-test-note-warn {
  background: var(--juhe-warn-soft);
  border-color: var(--juhe-warn-line);
}

.question-test-note-danger {
  background: var(--juhe-danger-soft);
  border-color: var(--juhe-danger-line);
}

.question-test-points {
  display: grid;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.question-test-point {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 2px 8px;
  font-size: 12px;
  line-height: 20px;
}

.question-test-point-state {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: var(--juhe-fg-soft);
  white-space: nowrap;
}

.question-test-point-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
}

.question-test-point-state.is-met .question-test-point-dot {
  background: var(--juhe-ok);
}

.question-test-point-state.is-unmet .question-test-point-dot {
  background: var(--juhe-danger);
}

.question-test-point-text {
  color: var(--juhe-fg-soft);
  word-break: break-word;
}

.question-test-point-note {
  color: var(--juhe-muted);
}

.question-test-answer {
  display: grid;
  gap: 2px;
}

.question-test-answer-text {
  margin: 0;
  color: var(--juhe-fg-soft);
  font-size: 13px;
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-word;
}

.question-test-answer-text.is-collapsed {
  display: -webkit-box;
  overflow: hidden;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 3;
}

.question-test-answer-toggle {
  justify-self: start;
  padding: 0;
}

.question-list-section {
  min-width: 0;
  margin-top: 22px;
  padding-top: 18px;
  border-top: 1px solid var(--juhe-border);
}

.question-list-head {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}

.question-keyword-input {
  width: 220px;
}

.question-status-select {
  width: 130px;
}

.question-list {
  border-top: 1px solid var(--juhe-border);
}

.question-item {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 14px;
  padding: 14px 0;
  border-bottom: 1px solid var(--juhe-border);
  transition: background-color .2s ease;
}

.question-item:hover {
  background: var(--juhe-surface-soft);
}

.question-item-main {
  min-width: 0;
  flex: 1 1 auto;
}

.question-item-title-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
}

.question-item h4 {
  min-width: 0;
  overflow: hidden;
  font-size: 14px;
  font-weight: 600;
  line-height: 22px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.question-item-text {
  display: -webkit-box;
  margin: 5px 0 0;
  overflow: hidden;
  color: var(--juhe-fg-soft);
  font-size: 12px;
  line-height: 20px;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  word-break: break-word;
}

.question-item-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  margin-top: 6px;
  color: var(--juhe-faint);
  font-size: 11px;
  line-height: 16px;
}

.question-item-reject {
  color: var(--juhe-danger);
}

.question-empty {
  margin: 0;
  padding: 28px 16px 20px;
  border-top: 1px solid var(--juhe-border);
}

.question-empty-help {
  display: block;
  color: var(--juhe-faint);
  font-size: 12px;
  line-height: 18px;
}

.question-pagination {
  margin-top: 14px;
  text-align: right;
}

.question-detail {
  display: grid;
  gap: 14px;
}

.question-detail-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.question-detail-title {
  color: var(--juhe-fg);
  font-size: 16px;
  font-weight: 700;
  line-height: 24px;
  word-break: break-word;
}

.question-detail-subtitle {
  margin-top: 4px;
  color: var(--juhe-muted);
  font-size: 12px;
}

.question-detail-alert {
  margin: 0;
}

.question-detail-block {
  display: grid;
  gap: 6px;
}

.question-detail-label {
  color: var(--juhe-fg-soft);
  font-size: 13px;
  font-weight: 600;
}

.question-detail-text {
  margin: 0;
  color: var(--juhe-fg-soft);
  font-size: 13px;
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-word;
}

.question-detail-muted {
  color: var(--juhe-faint);
}

.question-detail-descriptions {
  background: var(--juhe-surface);
}

.question-reject-hint {
  margin: 0;
  color: var(--juhe-muted);
  font-size: 12px;
  line-height: 18px;
}

.question-reject-field {
  margin-top: 16px;
  margin-bottom: 0;
}

:global(.question-bank-modal-wrap .ant-modal) {
  top: 32px;
  max-width: calc(100vw - 48px);
  padding-bottom: 32px;
}

:global(.question-bank-modal-wrap .ant-modal-content) {
  overflow: hidden;
  padding: 0;
  border: 1px solid var(--juhe-border);
  border-radius: 8px;
  box-shadow: 0 24px 64px rgba(34, 40, 43, .18);
}

:global(.question-bank-modal-wrap .ant-modal-header) {
  margin: 0;
  padding: 18px 22px 15px;
  border-bottom: 1px solid var(--juhe-border);
}

:global(.question-bank-modal-wrap .ant-modal-body) {
  max-height: calc(100dvh - 146px);
  overflow-y: auto;
  padding: 18px 22px 22px;
}

:global(.question-bank-modal-wrap .ant-modal-close) {
  top: 17px;
  inset-inline-end: 18px;
  color: var(--juhe-muted);
}

@media (max-width: 940px) {
  .question-item {
    flex-direction: column;
    align-items: stretch;
  }

  .question-item-actions {
    align-self: flex-end;
  }

  .question-keyword-input {
    width: 180px;
  }
}

@media (max-width: 640px) {
  .question-bank-modal-title small {
    max-width: calc(100vw - 100px);
  }

  .question-test-grid {
    grid-template-columns: 1fr;
  }

  :global(.question-bank-modal-wrap .ant-modal) {
    top: 12px;
    max-width: calc(100vw - 24px);
    padding-bottom: 12px;
  }

  :global(.question-bank-modal-wrap .ant-modal-header) {
    padding: 16px 18px 13px;
  }

  :global(.question-bank-modal-wrap .ant-modal-body) {
    max-height: calc(100dvh - 112px);
    padding: 14px 14px 18px;
  }
}
</style>
