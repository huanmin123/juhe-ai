<template>
  <a-card class="page-card responsive-page-card mcp-runtime-page">
    <a-tabs v-model:active-key="activeTab" class="mcp-runtime-tabs" @change="handleTabChange">
      <a-tab-pane key="servers" tab="MCP Server">
        <ResponsiveListToolbar
          v-model:keyword="serverFilters.keyword"
          search-placeholder="搜索 label / URL / 说明"
          filter-title="筛选 MCP Server"
          :active-filter-count="serverActiveFilterCount"
          :advanced-filter-count="serverAdvancedFilterCount"
          :refresh-loading="serversLoading"
          @search="applyServerFilters"
          @reset="resetServerFilters"
          @refresh="loadServers"
        >
          <template #inline-filters>
            <a-select v-model:value="serverFilters.enabled" class="responsive-list-inline-filter toolbar-select" :options="serverEnabledOptions" @change="applyServerFilters" />
            <SystemPrincipalSelect
              v-if="isManagementView"
              v-model:value="serverFilters.systemAccountId"
              class="responsive-list-inline-filter toolbar-select mcp-principal-filter"
              :accounts="systemAccountOptions"
              :include-all="true"
              :loading="systemAccountOptionsLoading"
              :selected-ids="[serverFilters.systemAccountId]"
              all-value="all"
              placeholder="系统账户"
              @change="applyServerFilters"
            />
          </template>
          <template #advanced-filters>
            <a-form layout="vertical" class="mcp-filter-form">
              <a-form-item v-if="isManagementView" label="系统账户">
                <SystemPrincipalSelect
                  v-model:value="serverFilters.systemAccountId"
                  :accounts="systemAccountOptions"
                  :include-all="true"
                  :loading="systemAccountOptionsLoading"
                  :selected-ids="[serverFilters.systemAccountId]"
                  all-value="all"
                  placeholder="系统账户"
                />
              </a-form-item>
              <a-form-item label="启用状态">
                <a-select v-model:value="serverFilters.enabled" :options="serverEnabledOptions" />
              </a-form-item>
            </a-form>
          </template>
          <template #actions>
            <a-button type="primary" @click="openServerCreate">
              <template #icon><plus-outlined /></template>
              新建 Server
            </a-button>
          </template>
        </ResponsiveListToolbar>

        <ResponsiveDataList
          table-class="page-table mcp-server-table"
          :columns="serverColumns"
          :data-source="servers"
          row-key="id"
          :loading="serversLoading"
          :loading-more="serversMobileLoadingMore"
          :mobile-has-more="serversMobileHasMore"
          :pagination="serversTablePagination"
          :scroll-x="1420"
          mobile-pagination
          pull-refresh-enabled
          :refreshing="serversLoading"
          @change="handleServersTableChange"
          @mobile-load-more="loadMoreMobileServers"
          @mobile-refresh="refreshMobileServers"
        >
          <template #emptyText>
            <a-empty class="page-empty-card" description="暂无 MCP Server allowlist 配置。" />
          </template>
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'label'">
              <div class="mcp-primary-cell">
                <span class="mono-cell">{{ record.label }}</span>
                <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
              </div>
            </template>
            <template v-else-if="column.key === 'serverUrl'">
              <span class="mono-cell mcp-url-cell">{{ record.serverUrl }}</span>
            </template>
            <template v-else-if="column.key === 'policy'">
              <a-space :size="6" wrap>
                <a-tag :color="approvalPolicyColor(record.defaultApprovalPolicy)">{{ approvalPolicyText(record.defaultApprovalPolicy) }}</a-tag>
                <a-tag :color="record.allowRequestAuthorization ? 'orange' : 'default'">
                  {{ record.allowRequestAuthorization ? '允许请求授权' : '禁止请求授权' }}
                </a-tag>
              </a-space>
            </template>
            <template v-else-if="column.key === 'allowedTools'">
              <span v-if="!record.allowedTools.length" class="muted-cell">全部远程工具</span>
              <a-space v-else :size="4" wrap>
                <a-tag v-for="tool in record.allowedTools.slice(0, 3)" :key="tool">{{ tool }}</a-tag>
                <a-tag v-if="record.allowedTools.length > 3">+{{ record.allowedTools.length - 3 }}</a-tag>
              </a-space>
            </template>
            <template v-else-if="column.key === 'limits'">
              <span class="muted-cell">{{ limitSummary(record) }}</span>
            </template>
            <template v-else-if="column.key === 'updatedAt'">
              <span>{{ formatDateTime(record.updatedAt) }}</span>
            </template>
            <template v-else-if="column.key === 'actions'">
              <RowActions
                :actions="serverRowActions(record)"
                @action-click="handleServerAction($event, record)"
              />
            </template>
          </template>
          <template #card="{ record }">
            <article class="mobile-list-card">
              <div class="mobile-list-card-head">
                <div class="mobile-list-card-title mono-cell">{{ record.label }}</div>
                <div class="mobile-list-card-tags">
                  <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
                  <a-tag :color="approvalPolicyColor(record.defaultApprovalPolicy)">{{ approvalPolicyText(record.defaultApprovalPolicy) }}</a-tag>
                </div>
              </div>
              <div class="mobile-list-meta-grid">
                <div class="mobile-list-meta-item mobile-list-meta-wide">
                  <span>URL</span>
                  <strong class="mono-cell">{{ record.serverUrl }}</strong>
                </div>
                <div class="mobile-list-meta-item">
                  <span>工具</span>
                  <strong>{{ record.allowedTools.length ? `${record.allowedTools.length} 个限制` : '全部' }}</strong>
                </div>
                <div class="mobile-list-meta-item">
                  <span>请求授权</span>
                  <strong>{{ record.allowRequestAuthorization ? '允许' : '禁止' }}</strong>
                </div>
                <div class="mobile-list-meta-item mobile-list-meta-wide">
                  <span>限制</span>
                  <strong>{{ limitSummary(record) }}</strong>
                </div>
              </div>
              <div class="mobile-list-card-actions">
                <RowActions variant="button" :actions="serverRowActions(record)" @action-click="handleServerAction($event, record)" />
              </div>
            </article>
          </template>
        </ResponsiveDataList>
      </a-tab-pane>

      <a-tab-pane key="approvals" tab="审批请求">
        <ResponsiveListToolbar
          v-model:keyword="approvalFilters.traceId"
          search-placeholder="搜索 traceId"
          filter-title="筛选审批请求"
          :active-filter-count="approvalActiveFilterCount"
          :advanced-filter-count="approvalAdvancedFilterCount"
          :refresh-loading="approvalsLoading"
          @search="applyApprovalFilters"
          @reset="resetApprovalFilters"
          @refresh="loadApprovals"
        >
          <template #inline-filters>
            <a-select v-model:value="approvalFilters.status" class="responsive-list-inline-filter toolbar-select" :options="approvalStatusOptions" @change="applyApprovalFilters" />
            <a-input v-model:value="approvalFilters.serverLabel" class="responsive-list-inline-filter toolbar-select" allow-clear placeholder="Server label" @press-enter="applyApprovalFilters" />
            <SystemPrincipalSelect
              v-if="isManagementView"
              v-model:value="approvalFilters.systemAccountId"
              class="responsive-list-inline-filter toolbar-select mcp-principal-filter"
              :accounts="systemAccountOptions"
              :include-all="true"
              :loading="systemAccountOptionsLoading"
              :selected-ids="[approvalFilters.systemAccountId]"
              all-value="all"
              placeholder="系统账户"
              @change="applyApprovalFilters"
            />
          </template>
          <template #advanced-filters>
            <a-form layout="vertical" class="mcp-filter-form">
              <a-form-item v-if="isManagementView" label="系统账户">
                <SystemPrincipalSelect
                  v-model:value="approvalFilters.systemAccountId"
                  :accounts="systemAccountOptions"
                  :include-all="true"
                  :loading="systemAccountOptionsLoading"
                  :selected-ids="[approvalFilters.systemAccountId]"
                  all-value="all"
                  placeholder="系统账户"
                />
              </a-form-item>
              <a-form-item label="状态">
                <a-select v-model:value="approvalFilters.status" :options="approvalStatusOptions" />
              </a-form-item>
              <a-form-item label="Server label">
                <a-input v-model:value="approvalFilters.serverLabel" allow-clear />
              </a-form-item>
              <a-form-item label="工具名">
                <a-input v-model:value="approvalFilters.toolName" allow-clear />
              </a-form-item>
              <a-form-item label="API Key ID">
                <a-input v-model:value="approvalFilters.apiKeyId" allow-clear />
              </a-form-item>
              <a-form-item label="分组 ID">
                <a-input v-model:value="approvalFilters.groupId" allow-clear />
              </a-form-item>
            </a-form>
          </template>
        </ResponsiveListToolbar>

        <ResponsiveDataList
          table-class="page-table mcp-approval-table"
          :columns="approvalColumns"
          :data-source="approvals"
          row-key="id"
          :loading="approvalsLoading"
          :loading-more="approvalsMobileLoadingMore"
          :mobile-has-more="approvalsMobileHasMore"
          :pagination="approvalsTablePagination"
          :scroll-x="1370"
          mobile-pagination
          pull-refresh-enabled
          :refreshing="approvalsLoading"
          @change="handleApprovalsTableChange"
          @mobile-load-more="loadMoreMobileApprovals"
          @mobile-refresh="refreshMobileApprovals"
        >
          <template #emptyText>
            <a-empty class="page-empty-card" description="暂无 MCP 审批请求。" />
          </template>
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'status'">
              <a-tag :color="approvalStatusColor(record.status)">{{ approvalStatusText(record.status) }}</a-tag>
            </template>
            <template v-else-if="column.key === 'target'">
              <div class="mcp-target-cell">
                <span class="mono-cell">{{ record.serverLabel }}</span>
                <span>{{ record.toolName }}</span>
              </div>
            </template>
            <template v-else-if="column.key === 'traceId'">
              <span :class="record.traceId ? 'mono-cell' : 'muted-cell'">{{ record.traceId || '-' }}</span>
            </template>
            <template v-else-if="column.key === 'argumentsDigest'">
              <span class="mono-cell">{{ shortDigest(record.argumentsDigest) }}</span>
            </template>
            <template v-else-if="column.key === 'expiresAt'">
              <span>{{ formatDateTime(record.expiresAt) }}</span>
            </template>
            <template v-else-if="column.key === 'createdAt'">
              <span>{{ formatDateTime(record.createdAt) }}</span>
            </template>
            <template v-else-if="column.key === 'actions'">
              <RowActions
                :actions="approvalRowActions(record)"
                @action-click="handleApprovalAction($event, record)"
              />
            </template>
          </template>
          <template #card="{ record }">
            <article class="mobile-list-card">
              <div class="mobile-list-card-head">
                <div class="mobile-list-card-title">{{ record.toolName }}</div>
                <div class="mobile-list-card-tags">
                  <a-tag :color="approvalStatusColor(record.status)">{{ approvalStatusText(record.status) }}</a-tag>
                </div>
              </div>
              <div class="mobile-list-meta-grid">
                <div class="mobile-list-meta-item">
                  <span>Server</span>
                  <strong class="mono-cell">{{ record.serverLabel }}</strong>
                </div>
                <div class="mobile-list-meta-item">
                  <span>Digest</span>
                  <strong class="mono-cell">{{ shortDigest(record.argumentsDigest) }}</strong>
                </div>
                <div class="mobile-list-meta-item mobile-list-meta-wide">
                  <span>Trace</span>
                  <strong :class="record.traceId ? 'mono-cell' : 'muted-cell'">{{ record.traceId || '-' }}</strong>
                </div>
                <div class="mobile-list-meta-item">
                  <span>创建</span>
                  <strong>{{ formatDateTime(record.createdAt) }}</strong>
                </div>
                <div class="mobile-list-meta-item">
                  <span>过期</span>
                  <strong>{{ formatDateTime(record.expiresAt) }}</strong>
                </div>
              </div>
              <div class="mobile-list-card-actions">
                <RowActions variant="button" :actions="approvalRowActions(record)" @action-click="handleApprovalAction($event, record)" />
              </div>
            </article>
          </template>
        </ResponsiveDataList>
      </a-tab-pane>

      <a-tab-pane key="executions" tab="执行记录">
        <ResponsiveListToolbar
          v-model:keyword="executionFilters.traceId"
          search-placeholder="搜索 traceId"
          filter-title="筛选执行记录"
          :active-filter-count="executionActiveFilterCount"
          :advanced-filter-count="executionAdvancedFilterCount"
          :refresh-loading="executionsLoading"
          @search="applyExecutionFilters"
          @reset="resetExecutionFilters"
          @refresh="loadExecutions"
        >
          <template #inline-filters>
            <a-select v-model:value="executionFilters.status" class="responsive-list-inline-filter toolbar-select" :options="executionStatusOptions" @change="applyExecutionFilters" />
            <a-input v-model:value="executionFilters.serverLabel" class="responsive-list-inline-filter toolbar-select" allow-clear placeholder="Server label" @press-enter="applyExecutionFilters" />
            <SystemPrincipalSelect
              v-if="isManagementView"
              v-model:value="executionFilters.systemAccountId"
              class="responsive-list-inline-filter toolbar-select mcp-principal-filter"
              :accounts="systemAccountOptions"
              :include-all="true"
              :loading="systemAccountOptionsLoading"
              :selected-ids="[executionFilters.systemAccountId]"
              all-value="all"
              placeholder="系统账户"
              @change="applyExecutionFilters"
            />
          </template>
          <template #advanced-filters>
            <a-form layout="vertical" class="mcp-filter-form">
              <a-form-item v-if="isManagementView" label="系统账户">
                <SystemPrincipalSelect
                  v-model:value="executionFilters.systemAccountId"
                  :accounts="systemAccountOptions"
                  :include-all="true"
                  :loading="systemAccountOptionsLoading"
                  :selected-ids="[executionFilters.systemAccountId]"
                  all-value="all"
                  placeholder="系统账户"
                />
              </a-form-item>
              <a-form-item label="状态">
                <a-select v-model:value="executionFilters.status" :options="executionStatusOptions" />
              </a-form-item>
              <a-form-item label="Server label">
                <a-input v-model:value="executionFilters.serverLabel" allow-clear />
              </a-form-item>
              <a-form-item label="工具名">
                <a-input v-model:value="executionFilters.toolName" allow-clear />
              </a-form-item>
              <a-form-item label="审批请求 ID">
                <a-input v-model:value="executionFilters.approvalRequestId" allow-clear />
              </a-form-item>
              <a-form-item label="API Key ID">
                <a-input v-model:value="executionFilters.apiKeyId" allow-clear />
              </a-form-item>
              <a-form-item label="分组 ID">
                <a-input v-model:value="executionFilters.groupId" allow-clear />
              </a-form-item>
            </a-form>
          </template>
        </ResponsiveListToolbar>

        <ResponsiveDataList
          table-class="page-table mcp-execution-table"
          :columns="executionColumns"
          :data-source="executions"
          row-key="id"
          :loading="executionsLoading"
          :loading-more="executionsMobileLoadingMore"
          :mobile-has-more="executionsMobileHasMore"
          :pagination="executionsTablePagination"
          :scroll-x="1420"
          mobile-pagination
          pull-refresh-enabled
          :refreshing="executionsLoading"
          @change="handleExecutionsTableChange"
          @mobile-load-more="loadMoreMobileExecutions"
          @mobile-refresh="refreshMobileExecutions"
        >
          <template #emptyText>
            <a-empty class="page-empty-card" description="暂无 MCP 执行记录。" />
          </template>
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'status'">
              <a-space :size="6" wrap>
                <a-tag :color="executionStatusColor(record.status)">{{ executionStatusText(record.status) }}</a-tag>
                <a-tag v-if="record.outputTruncated" color="orange">已截断</a-tag>
              </a-space>
            </template>
            <template v-else-if="column.key === 'target'">
              <div class="mcp-target-cell">
                <span class="mono-cell">{{ record.serverLabel }}</span>
                <span>{{ record.toolName }}</span>
              </div>
            </template>
            <template v-else-if="column.key === 'traceId'">
              <span :class="record.traceId ? 'mono-cell' : 'muted-cell'">{{ record.traceId || '-' }}</span>
            </template>
            <template v-else-if="column.key === 'output'">
              <span>{{ formatBytes(record.outputBytes) }}</span>
              <span v-if="record.outputDigest" class="mono-cell mcp-digest-inline">{{ shortDigest(record.outputDigest) }}</span>
            </template>
            <template v-else-if="column.key === 'durationMs'">
              <span>{{ formatDuration(record.durationMs) }}</span>
            </template>
            <template v-else-if="column.key === 'createdAt'">
              <span>{{ formatDateTime(record.createdAt) }}</span>
            </template>
            <template v-else-if="column.key === 'actions'">
              <RowActions :actions="detailOnlyActions" @action-click="openExecutionDetail(record)" />
            </template>
          </template>
          <template #card="{ record }">
            <article class="mobile-list-card">
              <div class="mobile-list-card-head">
                <div class="mobile-list-card-title">{{ record.toolName }}</div>
                <div class="mobile-list-card-tags">
                  <a-tag :color="executionStatusColor(record.status)">{{ executionStatusText(record.status) }}</a-tag>
                  <a-tag v-if="record.outputTruncated" color="orange">已截断</a-tag>
                </div>
              </div>
              <div class="mobile-list-meta-grid">
                <div class="mobile-list-meta-item">
                  <span>Server</span>
                  <strong class="mono-cell">{{ record.serverLabel }}</strong>
                </div>
                <div class="mobile-list-meta-item">
                  <span>耗时</span>
                  <strong>{{ formatDuration(record.durationMs) }}</strong>
                </div>
                <div class="mobile-list-meta-item">
                  <span>输出</span>
                  <strong>{{ formatBytes(record.outputBytes) }}</strong>
                </div>
                <div class="mobile-list-meta-item mobile-list-meta-wide">
                  <span>Trace</span>
                  <strong :class="record.traceId ? 'mono-cell' : 'muted-cell'">{{ record.traceId || '-' }}</strong>
                </div>
                <div class="mobile-list-meta-item mobile-list-meta-wide">
                  <span>时间</span>
                  <strong>{{ formatDateTime(record.createdAt) }}</strong>
                </div>
              </div>
              <div class="mobile-list-card-actions">
                <RowActions variant="button" :actions="detailOnlyActions" @action-click="openExecutionDetail(record)" />
              </div>
            </article>
          </template>
        </ResponsiveDataList>
      </a-tab-pane>
    </a-tabs>

    <a-modal
      v-model:open="serverModalOpen"
      :title="serverEditingId ? '编辑 MCP Server' : '新建 MCP Server'"
      width="760px"
      :confirm-loading="serverSaving"
      @ok="saveServer"
      @cancel="resetServerForm"
    >
      <a-form layout="vertical" class="modal-form" autocomplete="off">
        <a-form-item label="Label" required>
          <a-input v-model:value="serverForm.label" placeholder="例如 internal-tools" />
        </a-form-item>
        <a-form-item label="Server URL" required>
          <a-input v-model:value="serverForm.serverUrl" placeholder="https://mcp.example.com/mcp" />
        </a-form-item>
        <a-form-item label="说明">
          <a-textarea v-model:value="serverForm.description" :rows="2" placeholder="可选，记录用途和授权边界" />
        </a-form-item>
        <a-row :gutter="16">
          <a-col :span="12">
            <a-form-item label="状态">
              <a-switch v-model:checked="serverForm.enabled" checked-children="启用" un-checked-children="停用" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="默认审批策略">
              <a-select v-model:value="serverForm.defaultApprovalPolicy" :options="approvalPolicyOptions" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-form-item label="允许工具">
          <a-select
            v-model:value="serverForm.allowedTools"
            mode="tags"
            :token-separators="[',', '，', '\\n']"
            placeholder="留空表示允许远程 tools/list 返回的全部工具"
          />
        </a-form-item>
        <a-row :gutter="16">
          <a-col :span="12">
            <a-form-item label="允许请求内 Authorization">
              <a-switch v-model:checked="serverForm.allowRequestAuthorization" checked-children="允许" un-checked-children="禁止" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="授权引用">
              <a-input v-model:value="serverForm.authorizationRef" placeholder="可选，仅保存引用名" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-row :gutter="16">
          <a-col :span="8">
            <a-form-item label="超时 ms">
              <a-input-number v-model:value="serverForm.timeoutMs" :min="1000" :max="120000" style="width: 100%" />
            </a-form-item>
          </a-col>
          <a-col :span="8">
            <a-form-item label="重试次数">
              <a-input-number v-model:value="serverForm.maxRetries" :min="0" :max="3" style="width: 100%" />
            </a-form-item>
          </a-col>
          <a-col :span="8">
            <a-form-item label="重试间隔 ms">
              <a-input-number v-model:value="serverForm.retryDelayMs" :min="0" :max="5000" style="width: 100%" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-row :gutter="16">
          <a-col :span="12">
            <a-form-item label="请求体上限 bytes">
              <a-input-number v-model:value="serverForm.maxBodyBytes" :min="16384" :max="4194304" style="width: 100%" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="输出上限 bytes">
              <a-input-number v-model:value="serverForm.maxOutputBytes" :min="4096" :max="1048576" style="width: 100%" />
            </a-form-item>
          </a-col>
        </a-row>
      </a-form>
    </a-modal>

    <a-drawer
      v-model:open="detailDrawerOpen"
      :title="detailDrawerTitle"
      width="min(720px, 96vw)"
      :body-style="{ padding: '18px 20px' }"
    >
      <a-spin :spinning="detailLoading">
        <a-descriptions v-if="activeDetail" bordered size="small" :column="1">
          <a-descriptions-item v-for="item in detailItems" :key="item.label" :label="item.label">
            <span :class="item.mono ? 'mono-cell' : ''">{{ item.value }}</span>
          </a-descriptions-item>
        </a-descriptions>
        <a-empty v-else class="page-empty-card" description="暂无详情" />
      </a-spin>
    </a-drawer>
  </a-card>
</template>

<script setup lang="ts">
import { PlusOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import type { TableColumnsType } from 'ant-design-vue'
import dayjs from 'dayjs'
import { computed, onMounted, reactive, ref } from 'vue'

import type {
  OpenAICompatibleMcpApprovalRejectPayload,
  OpenAICompatibleMcpApprovalRequestListParams,
  OpenAICompatibleMcpExecutionRecordListParams,
  OpenAICompatibleMcpServerListParams,
  OpenAICompatibleMcpServerPayload
} from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { useScopedOpenAICompatibleMcpRuntimeApi } from '@/composables/useScopedDomainApi'
import { api } from '@/api/client'
import { extractApiErrorMessage } from '@/shared/apiError'
import type {
  OpenAICompatibleMcpApprovalPolicy,
  OpenAICompatibleMcpApprovalRequestSummary,
  OpenAICompatibleMcpApprovalStatus,
  OpenAICompatibleMcpExecutionRecordSummary,
  OpenAICompatibleMcpExecutionStatus,
  OpenAICompatibleMcpServerDiagnosticSummary,
  OpenAICompatibleMcpServerSummary,
  OpenAICompatibleMcpToolCacheSummary,
  SystemAccountPrincipalSummary
} from '@/types/domain'
import { allSystemAccountsValue, selectedSystemAccountId } from '@/utils/systemAccountFilter'

type McpRuntimeTab = 'servers' | 'approvals' | 'executions'
type DetailMode = 'approval' | 'execution' | 'server'

interface ServerFilters {
  keyword: string
  enabled: 'all' | 'true' | 'false'
  systemAccountId: string
}

interface ApprovalFilters {
  traceId: string
  status: OpenAICompatibleMcpApprovalStatus | 'all'
  systemAccountId: string
  apiKeyId: string
  groupId: string
  serverLabel: string
  toolName: string
}

interface ExecutionFilters {
  traceId: string
  status: OpenAICompatibleMcpExecutionStatus | 'all'
  systemAccountId: string
  apiKeyId: string
  groupId: string
  approvalRequestId: string
  serverLabel: string
  toolName: string
}

interface ServerFormState {
  label: string
  serverUrl: string
  description: string
  enabled: boolean
  allowedTools: string[]
  defaultApprovalPolicy: OpenAICompatibleMcpApprovalPolicy
  timeoutMs?: number
  maxRetries?: number
  retryDelayMs?: number
  maxBodyBytes?: number
  maxOutputBytes?: number
  allowRequestAuthorization: boolean
  authorizationRef: string
}

interface DetailItem {
  label: string
  value: string
  mono?: boolean
}

const pageSize = 20
const { isManagementView } = useScopedMenuView()
const mcpRuntimeApi = useScopedOpenAICompatibleMcpRuntimeApi(isManagementView)
const activeTab = ref<McpRuntimeTab>('servers')
const systemAccountOptions = ref<SystemAccountPrincipalSummary[]>([])
const systemAccountOptionsLoading = ref(false)

const serverFilters = reactive<ServerFilters>(defaultServerFilters())
const approvalFilters = reactive<ApprovalFilters>(defaultApprovalFilters())
const executionFilters = reactive<ExecutionFilters>(defaultExecutionFilters())
const serverModalOpen = ref(false)
const serverSaving = ref(false)
const serverEditingId = ref<string>()
const serverForm = reactive<ServerFormState>(defaultServerForm())
const detailDrawerOpen = ref(false)
const detailLoading = ref(false)
const detailMode = ref<DetailMode>('server')
const activeDetail = ref<OpenAICompatibleMcpServerSummary | OpenAICompatibleMcpApprovalRequestSummary | OpenAICompatibleMcpExecutionRecordSummary>()
const serverToolCache = ref<OpenAICompatibleMcpToolCacheSummary[]>([])
const serverLatestDiagnostic = ref<OpenAICompatibleMcpServerDiagnosticSummary | null>(null)

const serverEnabledOptions = [
  { label: '全部状态', value: 'all' },
  { label: '启用', value: 'true' },
  { label: '停用', value: 'false' }
]
const approvalPolicyOptions = [
  { label: '总是审批', value: 'always' },
  { label: '默认免批', value: 'never' }
]
const approvalStatusOptions = [
  { label: '全部状态', value: 'all' },
  { label: '待审批', value: 'pending' },
  { label: '已批准', value: 'approved' },
  { label: '已拒绝', value: 'rejected' },
  { label: '已过期', value: 'expired' },
  { label: '已消费', value: 'consumed' }
]
const executionStatusOptions = [
  { label: '全部状态', value: 'all' },
  { label: '成功', value: 'succeeded' },
  { label: '失败', value: 'failed' }
]
const detailOnlyActions: RowActionItem[] = [{ key: 'detail', label: '详情', icon: 'detail', tone: 'info' }]

const serverColumns = computed<TableColumnsType<OpenAICompatibleMcpServerSummary>>(() => [
  { title: 'Label', key: 'label', dataIndex: 'label', width: 190, fixed: 'left' },
  { title: 'Server URL', key: 'serverUrl', dataIndex: 'serverUrl', width: 300 },
  { title: '策略', key: 'policy', width: 210 },
  { title: '允许工具', key: 'allowedTools', width: 220 },
  { title: '限制', key: 'limits', width: 240 },
  { title: '更新时间', key: 'updatedAt', dataIndex: 'updatedAt', width: 170 },
  { title: '操作', key: 'actions', width: 116, fixed: 'right' }
])
const approvalColumns = computed<TableColumnsType<OpenAICompatibleMcpApprovalRequestSummary>>(() => [
  { title: '状态', key: 'status', dataIndex: 'status', width: 110 },
  { title: '目标', key: 'target', width: 240 },
  { title: 'Trace ID', key: 'traceId', dataIndex: 'traceId', width: 240 },
  { title: '参数摘要', key: 'argumentsDigest', dataIndex: 'argumentsDigest', width: 160 },
  { title: '过期时间', key: 'expiresAt', dataIndex: 'expiresAt', width: 170 },
  { title: '创建时间', key: 'createdAt', dataIndex: 'createdAt', width: 170 },
  { title: '操作', key: 'actions', width: 128, fixed: 'right' }
])
const executionColumns = computed<TableColumnsType<OpenAICompatibleMcpExecutionRecordSummary>>(() => [
  { title: '状态', key: 'status', dataIndex: 'status', width: 130 },
  { title: '目标', key: 'target', width: 240 },
  { title: 'Trace ID', key: 'traceId', dataIndex: 'traceId', width: 240 },
  { title: '输出', key: 'output', width: 210 },
  { title: '耗时', key: 'durationMs', dataIndex: 'durationMs', width: 110 },
  { title: '创建时间', key: 'createdAt', dataIndex: 'createdAt', width: 170 },
  { title: '操作', key: 'actions', width: 86, fixed: 'right' }
])

const {
  items: servers,
  loading: serversLoading,
  mobileHasMore: serversMobileHasMore,
  mobileLoadingMore: serversMobileLoadingMore,
  tablePagination: serversTablePagination,
  handleTableChange: handleServersTableChange,
  loadData: loadServers,
  loadMoreMobile: loadMoreMobileServers,
  refreshMobile: refreshMobileServers,
  resetPagination: resetServersPagination,
  updateItems: updateServerItems,
  removeItems: removeServerItems
} = useResponsivePagedList<OpenAICompatibleMcpServerSummary>({
  pageSize,
  showTotal: showTotalText('个 MCP Server'),
  fetchPage: (_options, pageState) => mcpRuntimeApi.servers.list(serverListParams(pageState.current, pageState.pageSize)),
  onError: (error) => {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载 MCP Server 失败'))
  }
})

const {
  items: approvals,
  loading: approvalsLoading,
  mobileHasMore: approvalsMobileHasMore,
  mobileLoadingMore: approvalsMobileLoadingMore,
  tablePagination: approvalsTablePagination,
  handleTableChange: handleApprovalsTableChange,
  loadData: loadApprovals,
  loadMoreMobile: loadMoreMobileApprovals,
  refreshMobile: refreshMobileApprovals,
  resetPagination: resetApprovalsPagination,
  updateItems: updateApprovalItems
} = useResponsivePagedList<OpenAICompatibleMcpApprovalRequestSummary>({
  pageSize,
  showTotal: showTotalText('条审批请求'),
  fetchPage: (_options, pageState) => mcpRuntimeApi.approvals.list(approvalListParams(pageState.current, pageState.pageSize)),
  onError: (error) => {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载 MCP 审批请求失败'))
  }
})

const {
  items: executions,
  loading: executionsLoading,
  mobileHasMore: executionsMobileHasMore,
  mobileLoadingMore: executionsMobileLoadingMore,
  tablePagination: executionsTablePagination,
  handleTableChange: handleExecutionsTableChange,
  loadData: loadExecutions,
  loadMoreMobile: loadMoreMobileExecutions,
  refreshMobile: refreshMobileExecutions,
  resetPagination: resetExecutionsPagination
} = useResponsivePagedList<OpenAICompatibleMcpExecutionRecordSummary>({
  pageSize,
  showTotal: showTotalText('条执行记录'),
  fetchPage: (_options, pageState) => mcpRuntimeApi.executions.list(executionListParams(pageState.current, pageState.pageSize)),
  onError: (error) => {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载 MCP 执行记录失败'))
  }
})

const serverActiveFilterCount = computed(() => countActive([
  serverFilters.keyword,
  serverFilters.enabled !== 'all' ? serverFilters.enabled : '',
  isManagementView.value && serverFilters.systemAccountId !== allSystemAccountsValue ? serverFilters.systemAccountId : ''
]))
const serverAdvancedFilterCount = computed(() => countActive([
  serverFilters.enabled !== 'all' ? serverFilters.enabled : '',
  isManagementView.value && serverFilters.systemAccountId !== allSystemAccountsValue ? serverFilters.systemAccountId : ''
]))
const approvalActiveFilterCount = computed(() => countActive([
  approvalFilters.traceId,
  approvalFilters.status !== 'all' ? approvalFilters.status : '',
  approvalFilters.serverLabel,
  approvalFilters.toolName,
  approvalFilters.apiKeyId,
  approvalFilters.groupId,
  isManagementView.value && approvalFilters.systemAccountId !== allSystemAccountsValue ? approvalFilters.systemAccountId : ''
]))
const approvalAdvancedFilterCount = computed(() => approvalActiveFilterCount.value - (approvalFilters.traceId.trim() ? 1 : 0))
const executionActiveFilterCount = computed(() => countActive([
  executionFilters.traceId,
  executionFilters.status !== 'all' ? executionFilters.status : '',
  executionFilters.serverLabel,
  executionFilters.toolName,
  executionFilters.approvalRequestId,
  executionFilters.apiKeyId,
  executionFilters.groupId,
  isManagementView.value && executionFilters.systemAccountId !== allSystemAccountsValue ? executionFilters.systemAccountId : ''
]))
const executionAdvancedFilterCount = computed(() => executionActiveFilterCount.value - (executionFilters.traceId.trim() ? 1 : 0))
const detailDrawerTitle = computed(() => {
  if (detailMode.value === 'server') return 'MCP Server 详情'
  if (detailMode.value === 'approval') return 'MCP 审批详情'
  return 'MCP 执行详情'
})
const detailItems = computed<DetailItem[]>(() => {
  const detail = activeDetail.value
  if (!detail) return []
  if (detailMode.value === 'server') return serverDetailItems(detail as OpenAICompatibleMcpServerSummary)
  if (detailMode.value === 'approval') return approvalDetailItems(detail as OpenAICompatibleMcpApprovalRequestSummary)
  return executionDetailItems(detail as OpenAICompatibleMcpExecutionRecordSummary)
})

function defaultServerFilters(): ServerFilters {
  return { keyword: '', enabled: 'all', systemAccountId: allSystemAccountsValue }
}

function defaultApprovalFilters(): ApprovalFilters {
  return {
    traceId: '',
    status: 'pending',
    systemAccountId: allSystemAccountsValue,
    apiKeyId: '',
    groupId: '',
    serverLabel: '',
    toolName: ''
  }
}

function defaultExecutionFilters(): ExecutionFilters {
  return {
    traceId: '',
    status: 'all',
    systemAccountId: allSystemAccountsValue,
    apiKeyId: '',
    groupId: '',
    approvalRequestId: '',
    serverLabel: '',
    toolName: ''
  }
}

function defaultServerForm(): ServerFormState {
  return {
    label: '',
    serverUrl: '',
    description: '',
    enabled: true,
    allowedTools: [],
    defaultApprovalPolicy: 'always',
    timeoutMs: 30000,
    maxRetries: 1,
    retryDelayMs: 250,
    maxBodyBytes: 1024 * 1024,
    maxOutputBytes: 64 * 1024,
    allowRequestAuthorization: false,
    authorizationRef: ''
  }
}

function serverListParams(page: number, pageSize: number): OpenAICompatibleMcpServerListParams {
  return {
    page,
    pageSize,
    keyword: trimmed(serverFilters.keyword),
    enabled: serverFilters.enabled,
    systemAccountId: selectedSystemAccountId(serverFilters.systemAccountId, isManagementView.value)
  }
}

function approvalListParams(page: number, pageSize: number): OpenAICompatibleMcpApprovalRequestListParams {
  return {
    page,
    pageSize,
    traceId: trimmed(approvalFilters.traceId),
    status: approvalFilters.status,
    apiKeyId: trimmed(approvalFilters.apiKeyId),
    groupId: trimmed(approvalFilters.groupId),
    serverLabel: trimmed(approvalFilters.serverLabel),
    toolName: trimmed(approvalFilters.toolName),
    systemAccountId: selectedSystemAccountId(approvalFilters.systemAccountId, isManagementView.value)
  }
}

function executionListParams(page: number, pageSize: number): OpenAICompatibleMcpExecutionRecordListParams {
  return {
    page,
    pageSize,
    traceId: trimmed(executionFilters.traceId),
    status: executionFilters.status,
    apiKeyId: trimmed(executionFilters.apiKeyId),
    groupId: trimmed(executionFilters.groupId),
    approvalRequestId: trimmed(executionFilters.approvalRequestId),
    serverLabel: trimmed(executionFilters.serverLabel),
    toolName: trimmed(executionFilters.toolName),
    systemAccountId: selectedSystemAccountId(executionFilters.systemAccountId, isManagementView.value)
  }
}

function showTotalText(unit: string) {
  return (total: number, range?: [number, number], context?: { hasMore: boolean }) => context?.hasMore
    ? `已加载到第 ${range?.[1] ?? total - 1} ${unit}，还有更多`
    : `共 ${total} ${unit}`
}

function handleTabChange(key: string): void {
  activeTab.value = key as McpRuntimeTab
  if (activeTab.value === 'servers' && !servers.value.length) void loadServers()
  if (activeTab.value === 'approvals' && !approvals.value.length) void loadApprovals()
  if (activeTab.value === 'executions' && !executions.value.length) void loadExecutions()
}

function applyServerFilters(): void {
  serverFilters.keyword = serverFilters.keyword.trim()
  resetServersPagination()
  void loadServers()
}

function resetServerFilters(): void {
  Object.assign(serverFilters, defaultServerFilters())
  resetServersPagination()
  void loadServers()
}

function applyApprovalFilters(): void {
  normalizeApprovalFilters()
  resetApprovalsPagination()
  void loadApprovals()
}

function resetApprovalFilters(): void {
  Object.assign(approvalFilters, defaultApprovalFilters())
  resetApprovalsPagination()
  void loadApprovals()
}

function applyExecutionFilters(): void {
  normalizeExecutionFilters()
  resetExecutionsPagination()
  void loadExecutions()
}

function resetExecutionFilters(): void {
  Object.assign(executionFilters, defaultExecutionFilters())
  resetExecutionsPagination()
  void loadExecutions()
}

function normalizeApprovalFilters(): void {
  approvalFilters.traceId = approvalFilters.traceId.trim()
  approvalFilters.apiKeyId = approvalFilters.apiKeyId.trim()
  approvalFilters.groupId = approvalFilters.groupId.trim()
  approvalFilters.serverLabel = approvalFilters.serverLabel.trim()
  approvalFilters.toolName = approvalFilters.toolName.trim()
}

function normalizeExecutionFilters(): void {
  executionFilters.traceId = executionFilters.traceId.trim()
  executionFilters.apiKeyId = executionFilters.apiKeyId.trim()
  executionFilters.groupId = executionFilters.groupId.trim()
  executionFilters.approvalRequestId = executionFilters.approvalRequestId.trim()
  executionFilters.serverLabel = executionFilters.serverLabel.trim()
  executionFilters.toolName = executionFilters.toolName.trim()
}

function openServerCreate(): void {
  serverEditingId.value = undefined
  Object.assign(serverForm, defaultServerForm())
  serverModalOpen.value = true
}

function openServerEdit(server: OpenAICompatibleMcpServerSummary): void {
  serverEditingId.value = server.id
  Object.assign(serverForm, {
    label: server.label,
    serverUrl: server.serverUrl,
    description: server.description ?? '',
    enabled: server.enabled,
    allowedTools: [...server.allowedTools],
    defaultApprovalPolicy: server.defaultApprovalPolicy,
    timeoutMs: server.timeoutMs,
    maxRetries: server.maxRetries,
    retryDelayMs: server.retryDelayMs,
    maxBodyBytes: server.maxBodyBytes,
    maxOutputBytes: server.maxOutputBytes,
    allowRequestAuthorization: server.allowRequestAuthorization,
    authorizationRef: server.authorizationRef ?? ''
  })
  serverModalOpen.value = true
}

function resetServerForm(): void {
  serverEditingId.value = undefined
  Object.assign(serverForm, defaultServerForm())
}

async function saveServer(): Promise<void> {
  const payload = serverPayload()
  if (!payload.label || !payload.serverUrl) {
    message.warning('请填写 Label 和 Server URL')
    return
  }
  serverSaving.value = true
  try {
    const scopeParams = serverMutationScopeParams()
    const saved = serverEditingId.value
      ? await mcpRuntimeApi.servers.update(serverEditingId.value, payload, scopeParams)
      : await mcpRuntimeApi.servers.create(payload, scopeParams)
    message.success(serverEditingId.value ? 'MCP Server 已更新' : 'MCP Server 已创建')
    updateServerItems((item) => item.id === saved.id, () => saved)
    serverModalOpen.value = false
    resetServerForm()
    await loadServers({ quiet: true })
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存 MCP Server 失败'))
  } finally {
    serverSaving.value = false
  }
}

async function removeServer(server: OpenAICompatibleMcpServerSummary): Promise<void> {
  try {
    await mcpRuntimeApi.servers.delete(server.id, serverMutationScopeParams(server.systemAccountId))
    removeServerItems((item) => item.id === server.id)
    message.success('MCP Server 已删除')
    void loadServers({ quiet: true })
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '删除 MCP Server 失败'))
  }
}

async function diagnoseServer(server: OpenAICompatibleMcpServerSummary): Promise<void> {
  try {
    const result = await mcpRuntimeApi.servers.diagnose(server.id, {}, serverMutationScopeParams(server.systemAccountId))
    serverLatestDiagnostic.value = result.diagnostic
    serverToolCache.value = result.tools
    detailMode.value = 'server'
    activeDetail.value = server
    detailDrawerOpen.value = true
    message.success(result.diagnostic.status === 'succeeded' ? 'MCP Server 诊断完成' : 'MCP Server 诊断已记录失败摘要')
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, 'MCP Server 诊断失败'))
  }
}

async function toggleServer(server: OpenAICompatibleMcpServerSummary): Promise<void> {
  try {
    const updated = await mcpRuntimeApi.servers.update(
      server.id,
      { enabled: !server.enabled },
      serverMutationScopeParams(server.systemAccountId)
    )
    updateServerItems((item) => item.id === updated.id, () => updated)
    message.success(updated.enabled ? 'MCP Server 已启用' : 'MCP Server 已停用')
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '更新 MCP Server 状态失败'))
  }
}

async function approveApproval(record: OpenAICompatibleMcpApprovalRequestSummary): Promise<void> {
  try {
    const updated = await mcpRuntimeApi.approvals.approve(record.id, approvalMutationScopeParams(record.systemAccountId))
    updateApprovalItems((item) => item.id === updated.id, () => updated)
    message.success('MCP 审批已批准')
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '批准 MCP 审批失败'))
  }
}

async function rejectApproval(record: OpenAICompatibleMcpApprovalRequestSummary): Promise<void> {
  try {
    const payload: OpenAICompatibleMcpApprovalRejectPayload = { rejectReason: '后台人工拒绝' }
    const updated = await mcpRuntimeApi.approvals.reject(record.id, payload, approvalMutationScopeParams(record.systemAccountId))
    updateApprovalItems((item) => item.id === updated.id, () => updated)
    message.success('MCP 审批已拒绝')
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '拒绝 MCP 审批失败'))
  }
}

async function openServerDetail(record: OpenAICompatibleMcpServerSummary): Promise<void> {
  detailMode.value = 'server'
  serverToolCache.value = []
  serverLatestDiagnostic.value = null
  await openDetail(record, async () => {
    const [detail, toolsResult] = await Promise.all([
      mcpRuntimeApi.servers.detail(record.id, serverMutationScopeParams(record.systemAccountId)),
      mcpRuntimeApi.servers.tools(record.id, serverMutationScopeParams(record.systemAccountId))
    ])
    serverToolCache.value = toolsResult.tools
    serverLatestDiagnostic.value = toolsResult.latestDiagnostic
    return detail
  })
}

async function openApprovalDetail(record: OpenAICompatibleMcpApprovalRequestSummary): Promise<void> {
  detailMode.value = 'approval'
  await openDetail(record, () => mcpRuntimeApi.approvals.detail(record.id, approvalMutationScopeParams(record.systemAccountId)))
}

async function openExecutionDetail(record: OpenAICompatibleMcpExecutionRecordSummary): Promise<void> {
  detailMode.value = 'execution'
  await openDetail(record, () => mcpRuntimeApi.executions.detail(record.id, executionMutationScopeParams(record.systemAccountId)))
}

async function openDetail<T extends OpenAICompatibleMcpServerSummary | OpenAICompatibleMcpApprovalRequestSummary | OpenAICompatibleMcpExecutionRecordSummary>(
  fallback: T,
  load: () => Promise<T>
): Promise<void> {
  activeDetail.value = fallback
  detailDrawerOpen.value = true
  detailLoading.value = true
  try {
    activeDetail.value = await load()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载 MCP 详情失败'))
  } finally {
    detailLoading.value = false
  }
}

function handleServerAction(key: string, record: OpenAICompatibleMcpServerSummary): void {
  if (key === 'detail') {
    void openServerDetail(record)
    return
  }
  if (key === 'edit') {
    openServerEdit(record)
    return
  }
  if (key === 'toggle') {
    void toggleServer(record)
    return
  }
  if (key === 'diagnose') {
    void diagnoseServer(record)
    return
  }
  if (key === 'delete') {
    void removeServer(record)
  }
}

function handleApprovalAction(key: string, record: OpenAICompatibleMcpApprovalRequestSummary): void {
  if (key === 'detail') {
    void openApprovalDetail(record)
    return
  }
  if (key === 'approve') {
    void approveApproval(record)
    return
  }
  if (key === 'reject') {
    void rejectApproval(record)
  }
}

function serverRowActions(record: OpenAICompatibleMcpServerSummary): RowActionItem[] {
  return [
    { key: 'detail', label: '详情', icon: 'detail', tone: 'info' },
    { key: 'diagnose', label: '诊断', icon: 'test', tone: 'primary', disabled: !record.enabled },
    { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' },
    {
      key: 'toggle',
      label: record.enabled ? '停用' : '启用',
      icon: record.enabled ? 'disable' : 'enable',
      tone: record.enabled ? 'warning' : 'success',
      confirmTitle: record.enabled ? '确认停用这个 MCP Server？' : undefined,
      confirmOkText: record.enabled ? '停用' : undefined
    },
    {
      key: 'delete',
      label: '删除',
      icon: 'delete',
      tone: 'danger',
      danger: true,
      confirmTitle: '确认删除这个 MCP Server？',
      confirmOkText: '删除'
    }
  ]
}

function approvalRowActions(record: OpenAICompatibleMcpApprovalRequestSummary): RowActionItem[] {
  const pending = record.status === 'pending'
  return [
    { key: 'detail', label: '详情', icon: 'detail', tone: 'info' },
    { key: 'approve', label: '批准', icon: 'enable', tone: 'success', disabled: !pending },
    {
      key: 'reject',
      label: '拒绝',
      icon: 'disable',
      tone: 'danger',
      disabled: !pending,
      confirmTitle: pending ? '确认拒绝这个 MCP 工具调用？' : undefined,
      confirmOkText: pending ? '拒绝' : undefined
    }
  ]
}

function serverPayload(): OpenAICompatibleMcpServerPayload {
  return {
    label: serverForm.label.trim(),
    serverUrl: serverForm.serverUrl.trim(),
    description: nullableTrimmed(serverForm.description),
    enabled: serverForm.enabled,
    allowedTools: normalizedStringList(serverForm.allowedTools),
    defaultApprovalPolicy: serverForm.defaultApprovalPolicy,
    timeoutMs: nullableNumber(serverForm.timeoutMs),
    maxRetries: nullableNumber(serverForm.maxRetries),
    retryDelayMs: nullableNumber(serverForm.retryDelayMs),
    maxBodyBytes: nullableNumber(serverForm.maxBodyBytes),
    maxOutputBytes: nullableNumber(serverForm.maxOutputBytes),
    allowRequestAuthorization: serverForm.allowRequestAuthorization,
    authorizationRef: nullableTrimmed(serverForm.authorizationRef)
  }
}

function serverMutationScopeParams(systemAccountId = serverFilters.systemAccountId): Pick<OpenAICompatibleMcpServerListParams, 'systemAccountId'> | undefined {
  const id = selectedSystemAccountId(systemAccountId, isManagementView.value)
  return id ? { systemAccountId: id } : undefined
}

function approvalMutationScopeParams(systemAccountId: string): Pick<OpenAICompatibleMcpApprovalRequestListParams, 'systemAccountId'> | undefined {
  return isManagementView.value ? { systemAccountId } : undefined
}

function executionMutationScopeParams(systemAccountId: string): Pick<OpenAICompatibleMcpExecutionRecordListParams, 'systemAccountId'> | undefined {
  return isManagementView.value ? { systemAccountId } : undefined
}

function loadSystemAccounts(): void {
  if (!isManagementView.value) return
  systemAccountOptionsLoading.value = true
  api.systemAccounts.options({ limit: 100 })
    .then((items) => {
      systemAccountOptions.value = items
    })
    .catch((error) => {
      console.error(error)
      message.error('加载系统账户选项失败')
    })
    .finally(() => {
      systemAccountOptionsLoading.value = false
    })
}

function approvalPolicyText(value: OpenAICompatibleMcpApprovalPolicy): string {
  return value === 'always' ? '总是审批' : '默认免批'
}

function approvalPolicyColor(value: OpenAICompatibleMcpApprovalPolicy): string {
  return value === 'always' ? 'orange' : 'green'
}

function approvalStatusText(value: OpenAICompatibleMcpApprovalStatus): string {
  const map: Record<OpenAICompatibleMcpApprovalStatus, string> = {
    pending: '待审批',
    approved: '已批准',
    rejected: '已拒绝',
    expired: '已过期',
    consumed: '已消费'
  }
  return map[value]
}

function approvalStatusColor(value: OpenAICompatibleMcpApprovalStatus): string {
  const map: Record<OpenAICompatibleMcpApprovalStatus, string> = {
    pending: 'orange',
    approved: 'green',
    rejected: 'red',
    expired: 'default',
    consumed: 'blue'
  }
  return map[value]
}

function executionStatusText(value: OpenAICompatibleMcpExecutionStatus): string {
  return value === 'succeeded' ? '成功' : '失败'
}

function executionStatusColor(value: OpenAICompatibleMcpExecutionStatus): string {
  return value === 'succeeded' ? 'green' : 'red'
}

function limitSummary(record: OpenAICompatibleMcpServerSummary): string {
  return [
    record.timeoutMs ? `超时 ${record.timeoutMs}ms` : '',
    record.maxRetries !== undefined ? `重试 ${record.maxRetries}` : '',
    record.maxOutputBytes ? `输出 ${formatBytes(record.maxOutputBytes)}` : ''
  ].filter(Boolean).join(' / ') || '-'
}

function shortDigest(value?: string): string {
  if (!value) return '-'
  return value.length <= 18 ? value : `${value.slice(0, 10)}...${value.slice(-6)}`
}

function formatDateTime(value?: string): string {
  if (!value) return '-'
  const date = dayjs(value)
  return date.isValid() ? date.format('YYYY-MM-DD HH:mm:ss') : value
}

function formatDuration(value?: number): string {
  if (value === undefined || value === null) return '-'
  return `${value} ms`
}

function formatBytes(value?: number): string {
  if (value === undefined || value === null) return '-'
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${(value / 1024 / 1024).toFixed(2)} MB`
}

function serverDetailItems(record: OpenAICompatibleMcpServerSummary): DetailItem[] {
  const latest = serverLatestDiagnostic.value
  return [
    { label: 'ID', value: record.id, mono: true },
    { label: '系统账户 ID', value: record.systemAccountId, mono: true },
    { label: 'Label', value: record.label, mono: true },
    { label: 'Server URL', value: record.serverUrl, mono: true },
    { label: '状态', value: record.enabled ? '启用' : '停用' },
    { label: '默认审批策略', value: approvalPolicyText(record.defaultApprovalPolicy) },
    { label: '允许工具', value: record.allowedTools.length ? record.allowedTools.join(', ') : '全部远程工具' },
    { label: '允许请求内 Authorization', value: record.allowRequestAuthorization ? '允许' : '禁止' },
    { label: '授权引用', value: record.authorizationRef || '-' },
    { label: '限制', value: limitSummary(record) },
    { label: '最近诊断', value: latest ? `${diagnosticStatusText(latest.status)} / ${latest.toolCount} 个工具 / ${formatDateTime(latest.finishedAt)}` : '-' },
    { label: '诊断错误码', value: latest?.errorCode || '-' },
    { label: '缓存工具', value: serverToolCache.value.length ? serverToolCache.value.map((tool) => tool.toolName).join(', ') : '-' },
    { label: '说明', value: record.description || '-' },
    { label: '创建时间', value: formatDateTime(record.createdAt) },
    { label: '更新时间', value: formatDateTime(record.updatedAt) }
  ]
}

function diagnosticStatusText(value: 'succeeded' | 'failed'): string {
  return value === 'succeeded' ? '成功' : '失败'
}

function approvalDetailItems(record: OpenAICompatibleMcpApprovalRequestSummary): DetailItem[] {
  return [
    { label: 'ID', value: record.id, mono: true },
    { label: '系统账户 ID', value: record.systemAccountId, mono: true },
    { label: 'API Key ID', value: record.apiKeyId || '-', mono: Boolean(record.apiKeyId) },
    { label: '分组 ID', value: record.groupId || '-', mono: Boolean(record.groupId) },
    { label: 'Trace ID', value: record.traceId || '-', mono: Boolean(record.traceId) },
    { label: 'Server label', value: record.serverLabel, mono: true },
    { label: 'Server URL', value: record.serverUrl, mono: true },
    { label: '工具名', value: record.toolName },
    { label: '参数摘要', value: record.argumentsDigest, mono: true },
    { label: '参数预览', value: record.argumentsPreview || '-' },
    { label: '状态', value: approvalStatusText(record.status) },
    { label: '创建时间', value: formatDateTime(record.createdAt) },
    { label: '过期时间', value: formatDateTime(record.expiresAt) },
    { label: '批准时间', value: formatDateTime(record.approvedAt) },
    { label: '拒绝时间', value: formatDateTime(record.rejectedAt) },
    { label: '消费时间', value: formatDateTime(record.consumedAt) },
    { label: '拒绝原因', value: record.rejectReason || '-' }
  ]
}

function executionDetailItems(record: OpenAICompatibleMcpExecutionRecordSummary): DetailItem[] {
  return [
    { label: 'ID', value: record.id, mono: true },
    { label: '系统账户 ID', value: record.systemAccountId, mono: true },
    { label: 'API Key ID', value: record.apiKeyId || '-', mono: Boolean(record.apiKeyId) },
    { label: '分组 ID', value: record.groupId || '-', mono: Boolean(record.groupId) },
    { label: 'Trace ID', value: record.traceId || '-', mono: Boolean(record.traceId) },
    { label: '审批请求 ID', value: record.approvalRequestId || '-', mono: Boolean(record.approvalRequestId) },
    { label: 'Server label', value: record.serverLabel, mono: true },
    { label: 'Server URL', value: record.serverUrl, mono: true },
    { label: '工具名', value: record.toolName },
    { label: '参数摘要', value: record.argumentsDigest, mono: true },
    { label: '参数预览', value: record.argumentsPreview || '-' },
    { label: '状态', value: executionStatusText(record.status) },
    { label: '输出摘要', value: record.outputDigest || '-', mono: Boolean(record.outputDigest) },
    { label: '输出大小', value: formatBytes(record.outputBytes) },
    { label: '输出截断', value: record.outputTruncated ? '是' : '否' },
    { label: '错误码', value: record.errorCode || '-' },
    { label: '错误信息', value: record.errorMessage || '-' },
    { label: '开始时间', value: formatDateTime(record.startedAt) },
    { label: '完成时间', value: formatDateTime(record.finishedAt) },
    { label: '耗时', value: formatDuration(record.durationMs) },
    { label: '创建时间', value: formatDateTime(record.createdAt) }
  ]
}

function trimmed(value: string): string | undefined {
  const text = value.trim()
  return text || undefined
}

function nullableTrimmed(value: string): string | null {
  return value.trim() || null
}

function nullableNumber(value: number | undefined): number | null {
  return Number.isFinite(value) ? Number(value) : null
}

function normalizedStringList(values: string[]): string[] {
  return [...new Set(values.map((item) => item.trim()).filter(Boolean))]
}

function countActive(values: Array<string | undefined>): number {
  return values.filter((value) => Boolean(value?.trim())).length
}

onMounted(() => {
  loadSystemAccounts()
  void loadServers()
})
</script>

<style scoped>
.mcp-runtime-page {
  min-width: 0;
}

.mcp-runtime-tabs :deep(.ant-tabs-nav) {
  margin-bottom: 14px;
}

.mcp-principal-filter {
  min-width: 220px;
}

.mcp-filter-form {
  display: grid;
  gap: 2px;
}

.mcp-primary-cell,
.mcp-target-cell {
  display: grid;
  min-width: 0;
  gap: 4px;
}

.mcp-url-cell {
  display: inline-block;
  max-width: 280px;
  overflow: hidden;
  text-overflow: ellipsis;
  vertical-align: bottom;
  white-space: nowrap;
}

.mcp-digest-inline {
  display: inline-block;
  margin-left: 8px;
  color: #64748b;
}

@media (max-width: 900px) {
  .mcp-runtime-tabs :deep(.ant-tabs-tab) {
    padding: 10px 0;
  }
}
</style>
