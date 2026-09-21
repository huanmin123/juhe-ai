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
              <a-textarea v-model:value="form.questionText" :maxlength="2000" show-count :rows="4" placeholder="发送给模型的完整题面，≤2000 字符" />
            </a-form-item>
            <a-form-item label="参考答案" name="referenceAnswer" :rules="[{ required: true, message: '请输入参考答案' }]">
              <a-textarea v-model:value="form.referenceAnswer" :maxlength="4000" show-count :rows="4" placeholder="模型作答的判定依据，仅创建者与管理员可见，≤4000 字符" />
            </a-form-item>
            <a-form-item label="评分要点（选填）" name="keyPoints">
              <a-select
                v-model:value="form.keyPoints"
                mode="tags"
                :open="false"
                :token-separators="[',', '，']"
                placeholder="输入判定要点后回车或逗号分隔，1-10 条、每条 ≤50 字符"
                @change="handleKeyPointsChange"
              />
            </a-form-item>
          </div>

          <footer class="question-form-actions">
            <a-button v-if="editingItem" :disabled="submitting" @click="resetForm">取消编辑</a-button>
            <a-button type="primary" html-type="submit" :loading="submitting">
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
                  <a-tag :color="questionStatusColor(item.status)">{{ questionStatusText(item.status) }}</a-tag>
                </div>
                <p class="question-item-text">{{ item.questionText }}</p>
                <div class="question-item-meta">
                  <span>提交于 {{ formatDateTime(item.createdAt) }}</span>
                  <span v-if="item.reviewedAt">审核于 {{ formatDateTime(item.reviewedAt) }}</span>
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
            <div class="question-detail-subtitle">提交于 {{ formatDateTime(detailItem.createdAt) }}</div>
          </div>
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
          <p v-else class="question-detail-text question-detail-muted">仅创建者与管理员可见，当前无查看权限。</p>
        </div>
        <div class="question-detail-block">
          <div class="question-detail-label">评分要点</div>
          <a-space v-if="detailItem.keyPoints?.length" wrap>
            <a-tag v-for="(point, index) in detailItem.keyPoints" :key="index" color="blue">{{ point }}</a-tag>
          </a-space>
          <p v-else class="question-detail-text question-detail-muted">提交者未填写，或当前无查看权限。</p>
        </div>
        <a-descriptions bordered size="small" :column="1" class="question-detail-descriptions">
          <a-descriptions-item label="提交时间">{{ formatDateTime(detailItem.createdAt) }}</a-descriptions-item>
          <a-descriptions-item label="更新时间">{{ formatDateTime(detailItem.updatedAt) }}</a-descriptions-item>
          <a-descriptions-item label="审核时间">{{ detailItem.reviewedAt ? formatDateTime(detailItem.reviewedAt) : '-' }}</a-descriptions-item>
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
import { computed, nextTick, reactive, ref, toRef, watch } from 'vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { useScopedModelChecksApi } from '@/composables/useScopedDomainApi'
import { authState } from '@/composables/useAuth'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime } from '@/shared/formatters'
import type { ModelCheckQuestionBankItem, ModelCheckQuestionStatus } from '@/types/domain'
import { questionStatusColor, questionStatusText } from './modelCheckFormatters'

const props = defineProps<{
  isManagementView: boolean
  open: boolean
}>()
const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
}>()

const scopedApi = useScopedModelChecksApi(toRef(props, 'isManagementView'))
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
  return isOwn(item) && (item.status === 'pending' || item.status === 'rejected')
}

function canDelete(item: ModelCheckQuestionBankItem): boolean {
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
  const payload = {
    title: form.title.trim(),
    questionText: form.questionText.trim(),
    referenceAnswer: form.referenceAnswer.trim(),
    keyPoints: form.keyPoints.length ? [...form.keyPoints] : undefined
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
  form.referenceAnswer = item.referenceAnswer ?? ''
  form.keyPoints = [...(item.keyPoints ?? [])]
  void nextTick(() => questionEditorRef.value?.scrollIntoView({ block: 'start' }))
}

function resetForm() {
  editingItem.value = undefined
  form.title = ''
  form.questionText = ''
  form.referenceAnswer = ''
  form.keyPoints = []
  formRef.value?.clearValidate()
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
  color: #0f172a;
  font-size: 17px;
  font-weight: 600;
  line-height: 24px;
}

.question-bank-modal-title small {
  color: #64748b;
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
  color: #0f172a;
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
  color: #64748b;
  font-size: 12px;
  line-height: 18px;
}

.question-form-grid {
  display: grid;
  gap: 4px;
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
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 0 0;
  border-top: 1px solid #eef2f7;
}

.question-list-section {
  min-width: 0;
  margin-top: 22px;
  padding-top: 18px;
  border-top: 1px solid #e2e8f0;
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
  border-top: 1px solid #e2e8f0;
}

.question-item {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 14px;
  padding: 14px 0;
  border-bottom: 1px solid #eef2f7;
  transition: background-color .2s ease;
}

.question-item:hover {
  background: #fafcff;
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
  color: #475569;
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
  color: #94a3b8;
  font-size: 11px;
  line-height: 16px;
}

.question-item-reject {
  color: #dc2626;
}

.question-empty {
  margin: 0;
  padding: 28px 16px 20px;
  border-top: 1px solid #e2e8f0;
}

.question-empty-help {
  display: block;
  color: #94a3b8;
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
  color: #0f172a;
  font-size: 16px;
  font-weight: 700;
  line-height: 24px;
  word-break: break-word;
}

.question-detail-subtitle {
  margin-top: 4px;
  color: #64748b;
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
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}

.question-detail-text {
  margin: 0;
  color: #475569;
  font-size: 13px;
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-word;
}

.question-detail-muted {
  color: #94a3b8;
}

.question-detail-descriptions {
  background: #fff;
}

.question-reject-hint {
  margin: 0;
  color: #64748b;
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
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  box-shadow: 0 24px 64px rgba(15, 23, 42, .18);
}

:global(.question-bank-modal-wrap .ant-modal-header) {
  margin: 0;
  padding: 18px 22px 15px;
  border-bottom: 1px solid #e8eef6;
}

:global(.question-bank-modal-wrap .ant-modal-body) {
  max-height: calc(100dvh - 146px);
  overflow-y: auto;
  padding: 18px 22px 22px;
}

:global(.question-bank-modal-wrap .ant-modal-close) {
  top: 17px;
  inset-inline-end: 18px;
  color: #64748b;
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
