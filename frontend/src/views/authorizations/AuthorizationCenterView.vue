<template>
  <a-card class="page-card responsive-page-card authorization-center">
    <template #title>
      <div class="authorization-center__title">
        <span>{{ isManagementView ? '统一授权' : '授权中心' }}</span>
        <a-tag v-if="summary && !isManagementView" color="blue">待确认 {{ summary.pendingSources }}</a-tag>
        <a-tag v-if="summary && !isManagementView" color="orange">需复核 {{ summary.needsReviewSources }}</a-tag>
        <a-tag v-if="summary && !isManagementView" color="purple">待审批 {{ summary.teamApprovals }}</a-tag>
        <a-tag v-if="summary && !isManagementView" color="gold">所有者待确认 {{ summary.ownerApprovals }}</a-tag>
      </div>
    </template>
    <template #extra>
      <a-space>
        <a-badge v-if="!isManagementView" :count="summary?.unreadNotifications || 0" :offset="[-4, 4]">
          <a-button @click="openNotifications">通知</a-button>
        </a-badge>
        <a-button v-if="!isManagementView" @click="openBlocks">屏蔽列表</a-button>
        <a-button type="primary" @click="openCreate">发起授权</a-button>
        <a-button :loading="loading" @click="refresh">刷新</a-button>
      </a-space>
    </template>

    <a-tabs v-model:active-key="activeTab" @change="handleTabChange">
      <a-tab-pane v-for="tab in tabs" :key="tab.key" :tab="tab.label">
        <a-alert
          v-if="tab.key === 'inbound'"
          type="info"
          show-icon
          message="收到的授权需要你主动接受，并自行绑定到自己的分组后才会参与调度。"
          style="margin-bottom: 12px"
        />
        <a-alert
          v-if="tab.key === 'team_approvals'"
          type="warning"
          show-icon
          message="团队审批只决定是否进入成员确认流程；成员逐人接受后才生效，不会自动加入成员分组。"
          style="margin-bottom: 12px"
        />

        <a-spin :spinning="loading">
          <template v-if="tab.key === 'inbound'">
            <a-table
              :columns="sourceColumns"
              :data-source="sources"
              :pagination="false"
              :row-key="(row: AuthorizationSourceSummary) => row.sourceId"
              size="middle"
              :scroll="{ x: 1180 }"
            >
              <template #bodyCell="{ column, record }">
                <template v-if="column.key === 'resource'">
                  <div class="authorization-center__primary">{{ record.resourceName || record.resourceId }}</div>
                  <div class="authorization-center__muted">
                    {{ record.resourceType === 'group' ? '分组' : '账户' }} · 授权方 {{ record.resourceOwnerName || record.resourceOwnerSystemAccountId }}
                  </div>
                </template>
                <template v-else-if="column.key === 'source'">
                  <a-tag :color="record.sourceType === 'team' ? 'purple' : 'default'">
                    {{ record.sourceType === 'team' ? `团队：${record.teamName || record.teamId}` : '个人' }}
                  </a-tag>
                  <div class="authorization-center__muted">{{ record.vendorCode || '—' }}</div>
                </template>
                <template v-else-if="column.key === 'status'">
                  <a-tag :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
                  <div v-if="record.runtimeStatus" class="authorization-center__muted">运行态：{{ record.runtimeStatus }}</div>
                </template>
                <template v-else-if="column.key === 'material'">
                  <div v-if="record.connectionHosts?.length">endpoint：{{ record.connectionHosts.join('、') }}</div>
                  <div v-if="record.proxySummary" class="authorization-center__muted">代理：{{ record.proxySummary }}</div>
                  <div v-if="record.supportedModelCount" class="authorization-center__muted">模型 {{ record.supportedModelCount }} 个</div>
                  <div v-if="record.needsReviewReason" class="authorization-center__danger">{{ record.needsReviewReason }}</div>
                  <div v-if="record.needsReviewDetail" class="authorization-center__muted">
                    {{ formatNeedsReviewDetail(record.needsReviewDetail) }}
                  </div>
                </template>
                <template v-else-if="column.key === 'expiry'">
                  <div>{{ record.expiresAt ? formatTime(record.expiresAt) : '长期有效' }}</div>
                  <div v-if="record.pendingExpiresAt" class="authorization-center__muted">确认截止：{{ formatTime(record.pendingExpiresAt) }}</div>
                </template>
                <template v-else-if="column.key === 'binding'">
                  <a-tag v-if="record.resourceType === 'account'" :color="record.bound ? 'green' : 'default'">
                    {{ record.bound ? '已绑定分组' : '未绑定分组' }}
                  </a-tag>
                  <a-tag v-else :color="record.groupEnabled ? 'green' : 'default'">
                    {{ record.groupEnabled ? '授权分组已启用' : '授权分组未启用' }}
                  </a-tag>
                </template>
                <template v-else-if="column.key === 'actions'">
                  <a-space wrap>
                    <a-button v-if="record.canAccept" size="small" type="primary" @click="openAccept(record)">{{ record.status === 'needs_review' ? '重新确认' : '查看并接受' }}</a-button>
                    <a-button v-if="record.resourceType === 'account' && record.status === 'active' && !record.bound" size="small" @click="goBindAccount(record)">去绑定分组</a-button>
                    <a-button v-if="record.canBind" size="small" @click="run(() => api.myAuthorizationCenter.bind(record.sourceId), '授权分组已启用')">启用分组</a-button>
                    <a-button v-if="record.canUnbind" size="small" @click="run(() => api.myAuthorizationCenter.unbind(record.sourceId), '授权分组已停用')">停用分组</a-button>
                    <a-button v-if="record.resourceType === 'group' && (record.status === 'active' || record.status === 'dormant')" size="small" @click="openScope(record)">查看范围</a-button>
                    <a-popconfirm v-if="record.canReturn" title="归还后该来源立即不可用，确定归还？" @confirm="run(() => api.myAuthorizationCenter.return(record.sourceId), '已归还来源')">
                      <a-button size="small" danger>归还</a-button>
                    </a-popconfirm>
                    <a-popconfirm v-if="record.canDecline" title="拒绝后 24 小时内授权方不能再次向你发送同一资源，确定拒绝？" @confirm="run(() => api.myAuthorizationCenter.decline(record.sourceId), '已拒绝授权')">
                      <a-button size="small" danger>拒绝</a-button>
                    </a-popconfirm>
                    <a-button size="small" @click="openBlock(record)">屏蔽</a-button>
                    <a-button size="small" type="link" @click="openDetail(record)">详情</a-button>
                  </a-space>
                </template>
              </template>
            </a-table>
            <a-empty v-if="!loading && !sources.length" description="暂无收到的授权" />
          </template>

          <template v-else-if="tab.key === 'outbound' || tab.key === 'owned' || tab.key === 'owner_pending' || tab.key === 'all'">
            <a-table
              :columns="offerColumns"
              :data-source="offers"
              :pagination="false"
              :row-key="(row: AuthorizationOfferSummary) => row.grantId"
              size="middle"
              :scroll="{ x: 1200 }"
            >
              <template #bodyCell="{ column, record }">
                <template v-if="column.key === 'resource'">
                  <div class="authorization-center__primary">{{ record.resourceName || record.resourceId }}</div>
                  <div class="authorization-center__muted">{{ record.resourceType === 'group' ? '分组' : '账户' }} · {{ record.grantId }}</div>
                </template>
                <template v-else-if="column.key === 'grantee'">
                  <a-tag :color="record.granteeType === 'team' ? 'purple' : 'default'">
                    {{ record.granteeType === 'team' ? `团队：${record.teamName || record.teamId}` : record.granteeName || record.granteeSystemAccountId }}
                  </a-tag>
                  <div class="authorization-center__muted">创建人：{{ record.createdByName || record.createdBy }}</div>
                </template>
                <template v-else-if="column.key === 'status'">
                  <a-tag :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
                  <div v-if="record.ownerApprovalPending" class="authorization-center__muted">等待资源所有者确认</div>
                  <div v-if="record.endedReason" class="authorization-center__muted">{{ record.endedReason }}</div>
                </template>
                <template v-else-if="column.key === 'sources'">
                  <span>共 {{ record.sourceCount }}</span>
                  <span class="authorization-center__muted"> · 待确认 {{ record.pendingCount }} · 生效 {{ record.activeCount }} · 需复核 {{ record.needsReviewCount }}</span>
                </template>
                <template v-else-if="column.key === 'expiry'">
                  {{ record.expiresAt ? formatTime(record.expiresAt) : '长期有效' }}
                </template>
                <template v-else-if="column.key === 'actions'">
                  <a-space wrap>
                    <a-button v-if="tab.key === 'owner_pending'" size="small" type="primary" @click="run(() => api.myAuthorizationCenter.ownerApprove(record.grantId), '已确认授权')">同意以我名义授权</a-button>
                    <a-button v-if="tab.key === 'owner_pending'" size="small" danger @click="run(() => api.myAuthorizationCenter.ownerDecline(record.grantId), '已拒绝授权')">拒绝</a-button>
                    <a-button v-if="canPause(record)" size="small" @click="run(() => currentApi().pause(record.grantId), '已暂停授权')">暂停</a-button>
                    <a-button v-if="canResume(record)" size="small" @click="run(() => currentApi().resume(record.grantId), '已恢复授权')">恢复</a-button>
                    <a-button v-if="canExtend(record)" size="small" @click="openExtend(record)">延长</a-button>
                    <a-popconfirm v-if="canRevoke(record)" title="回收后所有非终态来源立即撤销，确定回收？" @confirm="run(() => currentApi().revoke(record.grantId), '已回收授权')">
                      <a-button size="small" danger>回收</a-button>
                    </a-popconfirm>
                  </a-space>
                </template>
              </template>
            </a-table>
            <a-empty v-if="!loading && !offers.length" description="暂无授权记录" />
          </template>

          <template v-else-if="tab.key === 'team_approvals'">
            <a-table
              :columns="teamOfferColumns"
              :data-source="teamOffers"
              :pagination="false"
              :row-key="(row: TeamOfferRow) => `${row.teamId}:${row.grantId}`"
              size="middle"
              :scroll="{ x: 1100 }"
            >
              <template #bodyCell="{ column, record }">
                <template v-if="column.key === 'team'">
                  <div class="authorization-center__primary">{{ record.teamName }}</div>
                  <div class="authorization-center__muted">授权方：{{ record.resourceOwnerName || record.createdByName }}</div>
                </template>
                <template v-else-if="column.key === 'resource'">
                  {{ record.resourceName || record.resourceId }}
                  <span class="authorization-center__muted">（{{ record.resourceType === 'group' ? '分组' : '账户' }}）</span>
                </template>
                <template v-else-if="column.key === 'status'">
                  <a-tag :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
                </template>
                <template v-else-if="column.key === 'members'">
                  成员 {{ record.memberCount }} · 已接受 {{ record.acceptedMembers }} · 已拒绝 {{ record.declinedMembers }}
                </template>
                <template v-else-if="column.key === 'actions'">
                  <a-space v-if="record.status === 'pending_team_approval'">
                    <a-popconfirm title="同意后成员会收到待确认，成员接受后才生效。确定同意？" @confirm="approveTeam(record)">
                      <a-button size="small" type="primary">同意</a-button>
                    </a-popconfirm>
                    <a-popconfirm title="拒绝后授权方 24 小时内不能再次向该团队发起。确定拒绝？" @confirm="declineTeam(record)">
                      <a-button size="small" danger>拒绝</a-button>
                    </a-popconfirm>
                  </a-space>
                  <span v-else class="authorization-center__muted">—</span>
                </template>
              </template>
            </a-table>
            <a-empty v-if="!loading && !teamOffers.length" description="没有待审批或历史的团队授权" />
          </template>

          <template v-else-if="tab.key === 'blocks'">
            <a-table :columns="blockColumns" :data-source="blocks" :pagination="false" row-key="id" size="middle">
              <template #bodyCell="{ column, record }">
                <template v-if="column.key === 'target'">
                  {{ record.blockedResourceType ? `${record.blockedResourceType}：${record.blockedResourceId}` : record.blockedSystemAccountId || '该授权方全部资源' }}
                </template>
                <template v-else-if="column.key === 'actions'">
                  <a-popconfirm v-if="!isManagementView" title="解除后对方可以再次向你发起授权。确定解除？" @confirm="removeBlock(record.id)">
                    <a-button size="small">解除屏蔽</a-button>
                  </a-popconfirm>
                  <span v-else class="authorization-center__muted">仅供滥用排查查看</span>
                </template>
              </template>
            </a-table>
            <a-empty v-if="!loading && !blocks.length" description="暂无屏蔽记录" />
          </template>
        </a-spin>
      </a-tab-pane>
    </a-tabs>

    <a-modal v-model:open="detailOpen" title="授权来源详情" :footer="null" width="760px">
      <template v-if="detail">
        <a-descriptions :column="2" bordered size="small">
          <a-descriptions-item label="资源">{{ detail.resourceName || detail.resourceId }}</a-descriptions-item>
          <a-descriptions-item label="类型">{{ detail.resourceType === 'group' ? '授权分组' : '授权账户' }}</a-descriptions-item>
          <a-descriptions-item label="授权方">{{ detail.resourceOwnerName || detail.resourceOwnerSystemAccountId }}</a-descriptions-item>
          <a-descriptions-item label="来源">{{ detail.sourceType === 'team' ? `团队 ${detail.teamName || detail.teamId}` : '个人授权' }}</a-descriptions-item>
          <a-descriptions-item label="供应商">{{ detail.vendorCode || '—' }}</a-descriptions-item>
          <a-descriptions-item label="到期">{{ detail.expiresAt ? formatTime(detail.expiresAt) : '长期有效' }}</a-descriptions-item>
        </a-descriptions>
        <a-divider orientation="left">可使用的上游连接</a-divider>
        <a-empty v-if="!detail.connections?.length" description="无连接信息" />
        <a-list v-else size="small" :data-source="detail.connections">
          <template #renderItem="{ item }">
            <a-list-item>
              <div>
                <div>{{ item.protocolSurfaceCode }} · {{ item.connectionPresetCode }}</div>
                <div class="authorization-center__muted">{{ item.baseUrl }}</div>
                <div class="authorization-center__muted">endpoint modes：{{ item.endpointModes?.join('、') || '默认' }}</div>
              </div>
            </a-list-item>
          </template>
        </a-list>
        <a-divider orientation="left">支持模型</a-divider>
        <a-space wrap>
          <a-tag v-for="model in detail.supportedModels || []" :key="model">{{ model }}</a-tag>
          <span v-if="!detail.supportedModels?.length" class="authorization-center__muted">未限制/未配置</span>
        </a-space>
      </template>
    </a-modal>

    <a-modal
      v-model:open="acceptOpen"
      :title="acceptTarget?.status === 'needs_review' ? '重新确认授权' : '接受授权'"
      :confirm-loading="acting"
      :ok-text="acceptTarget?.status === 'needs_review' ? '确认重新接受' : '确认接受'"
      @ok="confirmAccept"
    >
      <template v-if="acceptTarget">
        <a-alert
          type="warning"
          show-icon
          :message="acceptTarget.status === 'needs_review'
            ? '来源账户材料已变化，请核对变更摘要后重新确认。不重新确认时来源保持不可用。'
            : '请先核对授权方、上游地址、支持模型和代理摘要。接受后再自行绑定分组或启用授权分组。'"
          style="margin-bottom: 12px"
        />
        <a-descriptions :column="1" bordered size="small">
          <a-descriptions-item label="资源">{{ acceptTarget.resourceName || acceptTarget.resourceId }}</a-descriptions-item>
          <a-descriptions-item label="授权方">{{ acceptTarget.resourceOwnerName || acceptTarget.resourceOwnerSystemAccountId }}</a-descriptions-item>
          <a-descriptions-item label="上游地址">{{ acceptTarget.connectionHosts?.join('、') || '—' }}</a-descriptions-item>
          <a-descriptions-item label="代理摘要">{{ acceptTarget.proxySummary || '—' }}</a-descriptions-item>
          <a-descriptions-item label="支持模型数">{{ acceptTarget.supportedModelCount || 0 }}</a-descriptions-item>
          <a-descriptions-item label="额度">{{ formatLimits(acceptTarget.limits) }}</a-descriptions-item>
          <a-descriptions-item label="到期">{{ acceptTarget.expiresAt ? formatTime(acceptTarget.expiresAt) : '长期有效' }}</a-descriptions-item>
          <a-descriptions-item v-if="acceptTarget.needsReviewDetail" label="变更摘要">
            {{ formatNeedsReviewDetail(acceptTarget.needsReviewDetail) }}
          </a-descriptions-item>
        </a-descriptions>
        <a-checkbox v-if="acceptTarget.sourceType === 'team' && acceptTarget.status !== 'needs_review'" v-model:checked="retainFallback" style="margin-top: 12px">
          同时保留我的个人回退来源（团队授权结束时继续使用个人授权）
        </a-checkbox>
      </template>
    </a-modal>

    <a-modal
      v-model:open="blockOpen"
      title="屏蔽授权方/资源"
      :confirm-loading="acting"
      ok-text="确认屏蔽"
      @ok="confirmBlock"
    >
      <a-form layout="vertical">
        <a-form-item label="屏蔽范围">
          <a-radio-group v-model:value="blockScope">
            <a-radio value="grantor">屏蔽该授权方后续全部个人/团队授权</a-radio>
            <a-radio value="resource">只屏蔽当前资源</a-radio>
          </a-radio-group>
        </a-form-item>
        <a-form-item label="同时归还原有来源">
          <a-checkbox v-model:checked="blockReturnExisting">归还原有生效/休眠/需复核来源</a-checkbox>
        </a-form-item>
        <a-form-item label="原因（可选，仅自己可见）">
          <a-input v-model:value="blockReason" placeholder="例如：来源账户疑似被共享或存在风险" />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal v-model:open="scopeOpen" title="授权分组范围" width="860px" :footer="null">
      <template v-if="scope">
        <a-alert
          v-if="scope.snapshotMissing"
          type="warning"
          show-icon
          message="快照缺失。重建后会保持未启用状态，需要重新启用授权分组。"
          style="margin-bottom: 12px"
        />
        <a-space style="margin-bottom: 12px">
          <a-tag>当前分组账户 {{ scope.currentCount }}</a-tag>
          <a-tag>快照账户 {{ scope.snapshotCount }}</a-tag>
          <a-button size="small" type="primary" :loading="acting" @click="refreshScope">
            {{ scope.snapshotMissing ? '重建快照' : '纳入新增账户并刷新' }}
          </a-button>
        </a-space>
        <a-divider orientation="left">新增账户（{{ scope.added.length }}）</a-divider>
        <a-table :columns="scopeColumns" :data-source="scope.added" :pagination="false" size="small" row-key="accountId">
          <template #bodyCell="{ column }">
            <template v-if="column.key === 'action'">
              <span class="authorization-center__muted">刷新范围时纳入</span>
            </template>
          </template>
        </a-table>
        <a-divider orientation="left">修订变化的账户（{{ scope.revisionChanged.length }}）</a-divider>
        <a-table :columns="scopeColumns" :data-source="scope.revisionChanged" :pagination="false" size="small" row-key="accountId">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'action'">
              <a-button size="small" @click="reconfirmAccount(record)">逐账户重新确认</a-button>
            </template>
          </template>
        </a-table>
        <a-divider orientation="left">已移除账户（{{ scope.removed.length }}）</a-divider>
        <a-table :columns="scopeColumns" :data-source="scope.removed" :pagination="false" size="small" row-key="accountId">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'action'">
              <span class="authorization-center__muted">{{ record.accountName || record.accountId }}</span>
            </template>
          </template>
        </a-table>
      </template>
    </a-modal>

    <a-modal
      v-model:open="extendOpen"
      title="延长授权有效期"
      :confirm-loading="acting"
      ok-text="确认延长"
      @ok="confirmExtend"
    >
      <a-form layout="vertical">
        <a-form-item label="新的到期时间">
          <a-date-picker v-model:value="extendDate" show-time style="width: 100%" />
        </a-form-item>
        <a-form-item v-if="extendTarget?.legacyExpiryUnknown" label="存量授权到期前状态">
          <a-select v-model:value="extendConfirmStatus" placeholder="请选择迁移前状态">
            <a-select-option value="active">生效中</a-select-option>
            <a-select-option value="paused">暂停中</a-select-option>
          </a-select>
        </a-form-item>
        <a-alert type="info" show-icon message="只恢复本次到期周期由授权自身到期产生且未被归还/拒绝/回收的来源；不会自动恢复旧绑定。" />
      </a-form>
    </a-modal>

    <a-modal
      v-model:open="createOpen"
      title="发起授权"
      width="640px"
      :confirm-loading="acting"
      ok-text="发送待确认"
      @ok="confirmCreate"
    >
      <a-alert
        type="info"
        show-icon
        :message="createHint"
        style="margin-bottom: 12px"
      />
      <a-form layout="vertical">
        <a-form-item v-if="isManagementView" label="资源所有者" required>
          <a-select
            v-model:value="createForm.ownerSystemAccountId"
            show-search
            :filter-option="false"
            :options="ownerOptions"
            :loading="ownerLoading"
            placeholder="搜索并选择资源所有者"
            @search="scheduleOwnerSearch"
            @change="handleOwnerChange"
          />
        </a-form-item>
        <a-form-item label="资源类型" required>
          <a-radio-group v-model:value="createForm.resourceType" button-style="solid" @change="handleResourceTypeChange">
            <a-radio-button value="account">AI 账户</a-radio-button>
            <a-radio-button value="group">分组</a-radio-button>
          </a-radio-group>
        </a-form-item>
        <a-form-item label="授权资源" required>
          <a-select
            v-model:value="createForm.resourceId"
            show-search
            :filter-option="false"
            :options="resourceOptions"
            :loading="resourceLoading"
            :disabled="isManagementView && !createForm.ownerSystemAccountId"
            placeholder="搜索并选择要授权出去的资源"
            @search="scheduleResourceSearch"
          />
        </a-form-item>
        <a-form-item label="接收方类型" required>
          <a-radio-group v-model:value="createForm.granteeType" button-style="solid">
            <a-radio-button value="system_account">个人</a-radio-button>
            <a-radio-button value="team">团队</a-radio-button>
          </a-radio-group>
        </a-form-item>
        <a-form-item :label="createForm.granteeType === 'system_account' ? '接收方用户名' : '接收方团队名'" required>
          <a-input
            v-if="createForm.granteeType === 'system_account'"
            v-model:value="createForm.granteeUsername"
            placeholder="输入精确用户名，不接受模糊匹配"
          />
          <a-input v-else v-model:value="createForm.teamName" placeholder="输入精确团队名，审批人同意后成员逐人确认" />
        </a-form-item>
        <a-form-item label="说明">
          <a-textarea v-model:value="createForm.remark" :rows="2" placeholder="可选，说明用途或范围" />
        </a-form-item>
        <a-form-item label="到期时间">
          <a-date-picker v-model:value="createForm.expiresAt" show-time allow-clear style="width: 100%" />
        </a-form-item>
        <RequestQuotaFields :model="createForm.quotaLimits" />
      </a-form>
    </a-modal>

    <a-drawer v-model:open="notificationsOpen" title="授权通知" width="520px">
      <a-spin :spinning="notificationsLoading">
        <a-list :data-source="notifications" size="small">
          <template #renderItem="{ item }">
            <a-list-item>
              <a-list-item-meta>
                <template #title>
                  <span>{{ notificationLabel(item.type) }}</span>
                  <a-tag v-if="!item.readAt" color="blue" style="margin-left: 8px">未读</a-tag>
                </template>
                <template #description>
                  <div>{{ formatTime(item.createdAt) }}</div>
                  <div v-if="notificationSummary(item)" class="authorization-center__muted">{{ notificationSummary(item) }}</div>
                </template>
              </a-list-item-meta>
              <template #actions>
                <a-button v-if="!item.readAt" size="small" type="link" @click="readNotification(item.id)">标记已读</a-button>
              </template>
            </a-list-item>
          </template>
        </a-list>
        <a-empty v-if="!notificationsLoading && !notifications.length" description="暂无通知" />
      </a-spin>
    </a-drawer>
  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import dayjs, { type Dayjs } from 'dayjs'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'

import { api } from '@/api/client'
import type {
  AuthorizationBlock,
  AuthorizationInboxSummary,
  AuthorizationNotification,
  AuthorizationOfferSummary,
  AuthorizationScopeAccount,
  AuthorizationScopeDiff,
  AuthorizationSourceDetail,
  AuthorizationSourceSummary,
  AuthorizationTeamOfferSummary
} from '@/api/domains/authorizationGovernance'
import RequestQuotaFields from '@/views/shared/RequestQuotaFields.vue'
import { createQuotaLimitForm, quotaLimitsPayload, type RequestQuotaFormModel } from '@/views/shared/requestQuotaForm'
import { createAuthorizationSearchScheduler } from '@/views/authorizations/authorizationSearchScheduler'
import { authState } from '@/composables/useAuth'
import { useScopedMenuView } from '@/composables/useScopedMenuView'

type TeamOfferRow = AuthorizationTeamOfferSummary & { teamId: string; teamName: string }
type ResourceOption = { label: string; value: string }

interface CreateFormState {
  ownerSystemAccountId?: string
  resourceType: 'account' | 'group'
  resourceId?: string
  granteeType: 'system_account' | 'team'
  granteeUsername: string
  teamName: string
  remark: string
  expiresAt?: Dayjs
  quotaLimits: RequestQuotaFormModel
}

const { isManagementView } = useScopedMenuView()
const router = useRouter()
const loading = ref(false)
const acting = ref(false)
const activeTab = ref(isManagementView.value ? 'all' : 'inbound')
const summary = ref<AuthorizationInboxSummary>()
const sources = ref<AuthorizationSourceSummary[]>([])
const offers = ref<AuthorizationOfferSummary[]>([])
const teamOffers = ref<TeamOfferRow[]>([])
const blocks = ref<AuthorizationBlock[]>([])
const detail = ref<AuthorizationSourceDetail>()
const detailOpen = ref(false)
const acceptOpen = ref(false)
const acceptTarget = ref<AuthorizationSourceSummary>()
const retainFallback = ref(true)
const blockOpen = ref(false)
const blockTarget = ref<AuthorizationSourceSummary>()
const blockScope = ref<'grantor' | 'resource'>('grantor')
const blockReturnExisting = ref(true)
const blockReason = ref('')
const scopeOpen = ref(false)
const scope = ref<AuthorizationScopeDiff>()
const scopeSource = ref<AuthorizationSourceSummary>()
const extendOpen = ref(false)
const extendTarget = ref<AuthorizationOfferSummary>()
const extendDate = ref<Dayjs>(dayjs().add(30, 'day'))
const extendConfirmStatus = ref<string>()
const createOpen = ref(false)
const ownerLoading = ref(false)
const resourceLoading = ref(false)
const ownerOptions = ref<ResourceOption[]>([])
const resourceOptions = ref<ResourceOption[]>([])
const ownerKeyword = ref('')
const resourceKeyword = ref('')
const ownerSearch = createAuthorizationSearchScheduler({ delayMs: 250, keyword: ownerKeyword, load: loadOwnerOptions })
const resourceSearch = createAuthorizationSearchScheduler({ delayMs: 250, keyword: resourceKeyword, load: loadCreateResources })
const createForm = reactive<CreateFormState>({
  ownerSystemAccountId: undefined,
  resourceType: 'account',
  resourceId: undefined,
  granteeType: 'system_account',
  granteeUsername: '',
  teamName: '',
  remark: '',
  expiresAt: undefined,
  quotaLimits: createQuotaLimitForm()
})
const notificationsOpen = ref(false)
const notificationsLoading = ref(false)
const notifications = ref<AuthorizationNotification[]>([])

const createHint = computed(() => {
  if (isManagementView.value) {
    return '管理员代资源所有者发起时，必须先经资源所有者确认；确认后才会发送给接收方。'
  }
  if (createForm.granteeType === 'team') {
    return '团队授权需团队审批人同意，成员逐人确认；不会自动加入任何成员的分组。'
  }
  return '发送后接收方需主动接受，并自行绑定到自己的分组；不会自动加入任何分组。'
})

const tabs = computed(() => {
  if (isManagementView.value) {
    return [
      { key: 'all', label: '全部业务记录' },
      { key: 'team_approvals', label: '团队审批' },
      { key: 'blocks', label: '屏蔽记录' }
    ]
  }
  return [
    { key: 'inbound', label: '收到的授权' },
    { key: 'outbound', label: '我发出的授权' },
    { key: 'owned', label: '我的资源授权' },
    { key: 'owner_pending', label: '待我确认' },
    { key: 'team_approvals', label: '团队审批' },
    { key: 'blocks', label: '屏蔽列表' }
  ]
})

const sourceColumns = [
  { title: '资源', key: 'resource', width: 220 },
  { title: '来源', key: 'source', width: 180 },
  { title: '状态', key: 'status', width: 130 },
  { title: '上游与模型', key: 'material', width: 260 },
  { title: '到期', key: 'expiry', width: 150 },
  { title: '绑定', key: 'binding', width: 130 },
  { title: '操作', key: 'actions', width: 360 }
]

const offerColumns = [
  { title: '资源', key: 'resource', width: 220 },
  { title: '接收方', key: 'grantee', width: 200 },
  { title: '状态', key: 'status', width: 150 },
  { title: '来源统计', key: 'sources', width: 220 },
  { title: '到期', key: 'expiry', width: 150 },
  { title: '操作', key: 'actions', width: 280 }
]

const teamOfferColumns = [
  { title: '团队', key: 'team', width: 200 },
  { title: '资源', key: 'resource', width: 220 },
  { title: '状态', key: 'status', width: 150 },
  { title: '成员', key: 'members', width: 220 },
  { title: '操作', key: 'actions', width: 180 }
]

const blockColumns = [
  { title: '屏蔽对象', key: 'target' },
  { title: '原因', dataIndex: 'reason', key: 'reason' },
  { title: '时间', dataIndex: 'createdAt', key: 'createdAt' },
  { title: '操作', key: 'actions', width: 120 }
]

const scopeColumns = [
  { title: '账户', dataIndex: 'accountName', key: 'accountName' },
  { title: '账户 ID', dataIndex: 'accountId', key: 'accountId' },
  { title: '修订版本', dataIndex: 'revisionVersion', key: 'revisionVersion', width: 100 },
  { title: '操作', key: 'action', width: 160 }
]

const statusLabels: Record<string, string> = {
  pending_owner_approval: '待资源所有者确认',
  pending: '待确认',
  pending_team_approval: '待团队审批',
  pending_member_acceptance: '待成员确认',
  active: '生效中',
  paused: '已暂停',
  expired: '已到期',
  revoked: '已回收',
  returned: '已归还',
  declined: '已拒绝',
  dormant: '休眠（团队覆盖）',
  needs_review: '需重新确认'
}

function statusLabel(status: string): string { return statusLabels[status] || status }
function statusColor(status: string): string {
  if (status === 'active') return 'green'
  if (status === 'pending' || status === 'pending_owner_approval' || status === 'pending_team_approval' || status === 'pending_member_acceptance') return 'blue'
  if (status === 'needs_review' || status === 'dormant') return 'orange'
  if (status === 'paused') return 'gold'
  if (status === 'returned' || status === 'declined' || status === 'expired') return 'default'
  return 'red'
}

function formatTime(value?: string): string {
  if (!value) return '—'
  const parsed = dayjs(value)
  return parsed.isValid() ? parsed.format('YYYY-MM-DD HH:mm') : value
}

function formatLimits(limits?: Record<string, unknown>): string {
  if (!limits) return '不限'
  const parts = Object.entries(limits).map(([key, value]) => `${key}: ${String(value)}`)
  return parts.length ? parts.join('；') : '不限'
}

function formatNeedsReviewDetail(detail: unknown): string {
  if (detail == null) return ''
  if (typeof detail !== 'object') return String(detail)
  const labels: Record<string, string> = {
    accountId: '账户',
    vendorCode: '供应商',
    connectionHosts: '上游地址',
    modelCount: '模型数',
    proxyBound: '代理绑定'
  }
  return Object.entries(detail as Record<string, unknown>).map(([key, value]) => {
    let text: string
    if (Array.isArray(value)) text = value.join('、')
    else if (typeof value === 'boolean') text = value ? '有' : '无'
    else text = value == null ? '—' : String(value)
    return `${labels[key] || key}: ${text}`
  }).join('；')
}

function currentApi() {
  return isManagementView.value ? api.authorizationCenter : api.myAuthorizationCenter
}

function canPause(record: AuthorizationOfferSummary): boolean {
  if (record.status !== 'active') return false
  return isManagementView.value || record.resourceOwnerSystemAccountId === authState.currentUser.value?.id
}
function canResume(record: AuthorizationOfferSummary): boolean {
  if (record.status !== 'paused') return false
  return isManagementView.value || record.resourceOwnerSystemAccountId === authState.currentUser.value?.id
}
function canExtend(record: AuthorizationOfferSummary): boolean {
  if (record.status === 'active') return Boolean(record.expiresAt)
  if (record.status !== 'expired') return false
  return record.statusReason === 'authorization_expired' || record.statusReason === 'offer_expired'
}
function canRevoke(record: AuthorizationOfferSummary): boolean {
  if (['revoked', 'returned', 'declined', 'expired'].includes(record.status)) return false
  return isManagementView.value || record.resourceOwnerSystemAccountId === authState.currentUser.value?.id
}

async function refresh() {
  loading.value = true
  try {
    if (!isManagementView.value) {
      summary.value = await api.myAuthorizationCenter.inboxSummary()
    }
    await loadActiveTab()
  } catch (error) {
    message.error(errorMessage(error))
  } finally {
    loading.value = false
  }
}

async function loadActiveTab() {
  if (activeTab.value === 'inbound') {
    sources.value = await api.myAuthorizationCenter.inbound()
  } else if (activeTab.value === 'outbound') {
    offers.value = await api.myAuthorizationCenter.outbound()
  } else if (activeTab.value === 'owned') {
    offers.value = await api.myAuthorizationCenter.owned()
  } else if (activeTab.value === 'owner_pending') {
    offers.value = await api.myAuthorizationCenter.ownerPending()
  } else if (activeTab.value === 'all') {
    offers.value = await api.authorizationCenter.list()
  } else if (activeTab.value === 'team_approvals') {
    await loadTeamApprovals()
  } else if (activeTab.value === 'blocks') {
    blocks.value = isManagementView.value ? await api.authorizationCenter.blocks() : await api.myAuthorizationCenter.blocks()
  }
}

function handleTabChange() {
  refresh()
}

async function listAllTeams() {
  const items: Array<{ id: string; name: string }> = []
  // 后端 pageSize 上限为 20，超出会被静默钳制；审批页必须翻页取全，
  // 否则团队数量超过 20 后，后面的团队审批会被漏掉。
  for (let page = 1; page <= 50; page++) {
    const result = isManagementView.value
      ? await api.systemTeams.list({ page, pageSize: 20 })
      : await api.myTeams.list({ page, pageSize: 20 })
    items.push(...result.items)
    if (!result.hasMore) break
  }
  return items
}

async function loadTeamApprovals() {
  const teams = await listAllTeams()
  const rows: TeamOfferRow[] = []
  for (const team of teams) {
    try {
      const offersForTeam = isManagementView.value
        ? await api.authorizationCenter.teamOffers(team.id)
        : await api.myAuthorizationCenter.teamOffers(team.id)
      rows.push(...offersForTeam.map((offer) => ({ ...offer, teamId: team.id, teamName: team.name })))
    } catch {
      // A member who is not the approver has no access to this read; skip quietly.
    }
  }
  teamOffers.value = rows
}

async function run(action: () => Promise<unknown>, success: string) {
  acting.value = true
  try {
    await action()
    message.success(success)
    await refresh()
  } catch (error) {
    message.error(errorMessage(error))
  } finally {
    acting.value = false
  }
}

async function openDetail(record: AuthorizationSourceSummary) {
  try {
    detail.value = await api.myAuthorizationCenter.sourceDetail(record.sourceId)
    detailOpen.value = true
  } catch (error) {
    message.error(errorMessage(error))
  }
}

async function openAccept(record: AuthorizationSourceSummary) {
  acceptTarget.value = record
  retainFallback.value = true
  acceptOpen.value = true
}

async function confirmAccept() {
  const target = acceptTarget.value
  if (!target) return
  acting.value = true
  try {
    await api.myAuthorizationCenter.accept(target.sourceId, {
      expectedRevision: target.observedSourceRevision,
      expectedRevisionVersion: target.revisionVersion,
      ...(target.sourceType === 'team' ? { retainFallback: retainFallback.value } : {})
    })
    message.success('已接受授权。请按需绑定分组或启用授权分组。')
    acceptOpen.value = false
    await refresh()
  } catch (error) {
    const text = errorMessage(error)
    message.error(text)
    if (text.includes('来源已变化') || text.includes('重新查看')) {
      try {
        acceptTarget.value = await api.myAuthorizationCenter.sourceDetail(target.sourceId)
        retainFallback.value = true
      } catch {
        // Keep the original error visible; a later refresh will retry.
      }
    }
  } finally {
    acting.value = false
  }
}

function openBlock(record: AuthorizationSourceSummary) {
  blockTarget.value = record
  blockScope.value = 'grantor'
  blockReturnExisting.value = true
  blockReason.value = ''
  blockOpen.value = true
}

async function confirmBlock() {
  const target = blockTarget.value
  if (!target) return
  acting.value = true
  try {
    await api.myAuthorizationCenter.block(target.sourceId, {
      scope: blockScope.value,
      returnExisting: blockReturnExisting.value,
      reason: blockReason.value.trim()
    })
    message.success('已屏蔽')
    blockOpen.value = false
    await refresh()
  } catch (error) {
    message.error(errorMessage(error))
  } finally {
    acting.value = false
  }
}

function openBlocks() {
  activeTab.value = 'blocks'
  refresh()
}

async function removeBlock(blockId: string) {
  await run(() => api.myAuthorizationCenter.deleteBlock(blockId), '已解除屏蔽')
}

async function openScope(record: AuthorizationSourceSummary) {
  scopeSource.value = record
  try {
    scope.value = await api.myAuthorizationCenter.scopeDiff(record.sourceId)
    scopeOpen.value = true
  } catch (error) {
    message.error(errorMessage(error))
  }
}

async function refreshScope() {
  const current = scope.value
  const source = scopeSource.value
  if (!current || !source) return
  acting.value = true
  try {
    scope.value = await api.myAuthorizationCenter.refreshScope(source.sourceId, {
      expectedSnapshotRevision: current.snapshotRevision || '',
      reviewedAdded: current.added.map((account) => ({
        accountId: account.accountId,
        sourceRevision: account.sourceRevision,
        revisionVersion: account.revisionVersion
      })),
      rebuildMissingSnapshot: current.snapshotMissing
    })
    message.success('授权范围已刷新')
    await refresh()
  } catch (error) {
    const text = errorMessage(error)
    message.error(text)
    if (text.includes('刷新') || text.includes('变化') || text.includes('审阅')) {
      try {
        scope.value = await api.myAuthorizationCenter.scopeDiff(source.sourceId)
      } catch {
        // Keep the original error visible.
      }
    }
  } finally {
    acting.value = false
  }
}

async function reconfirmAccount(account: AuthorizationScopeAccount) {
  const source = scopeSource.value
  if (!source) return
  acting.value = true
  try {
    scope.value = await api.myAuthorizationCenter.reconfirmScopeAccount(source.sourceId, account.accountId, {
      expectedRevision: account.sourceRevision,
      expectedRevisionVersion: account.revisionVersion
    })
    message.success('已重新确认该账户')
    await refresh()
  } catch (error) {
    const text = errorMessage(error)
    message.error(text)
    if (text.includes('刷新') || text.includes('变化') || text.includes('审阅')) {
      try {
        scope.value = await api.myAuthorizationCenter.scopeDiff(source.sourceId)
      } catch {
        // Keep the original error visible.
      }
    }
  } finally {
    acting.value = false
  }
}

function openExtend(record: AuthorizationOfferSummary) {
  extendTarget.value = record
  extendDate.value = dayjs().add(30, 'day')
  extendConfirmStatus.value = undefined
  extendOpen.value = true
}

async function confirmExtend() {
  const target = extendTarget.value
  if (!target) return
  if (!extendDate.value?.isValid()) {
    message.error('请选择新的到期时间')
    return
  }
  acting.value = true
  try {
    await currentApi().extend(target.grantId, {
      expiresAt: extendDate.value.toISOString(),
      ...(extendConfirmStatus.value ? { confirmExpiredFromStatus: extendConfirmStatus.value } : {})
    })
    message.success('已延长授权')
    extendOpen.value = false
    await refresh()
  } catch (error) {
    message.error(errorMessage(error))
  } finally {
    acting.value = false
  }
}

async function approveTeam(record: TeamOfferRow) {
  await run(
    () => isManagementView.value
      ? api.authorizationCenter.approveTeamOffer(record.teamId, record.grantId)
      : api.myAuthorizationCenter.approveTeamOffer(record.teamId, record.grantId),
    '已同意团队授权，成员将收到待确认'
  )
}

async function declineTeam(record: TeamOfferRow) {
  await run(
    () => isManagementView.value
      ? api.authorizationCenter.declineTeamOffer(record.teamId, record.grantId)
      : api.myAuthorizationCenter.declineTeamOffer(record.teamId, record.grantId),
    '已拒绝团队授权'
  )
}

function goBindAccount(record: AuthorizationSourceSummary) {
  router.push({ path: '/my-accounts/chat', query: { keyword: record.resourceName || record.resourceId } })
}

async function openCreate() {
  ownerSearch.clear()
  resourceSearch.clear()
  ownerKeyword.value = ''
  resourceKeyword.value = ''
  createForm.ownerSystemAccountId = isManagementView.value ? undefined : authState.currentUser.value?.id
  createForm.resourceType = 'account'
  createForm.resourceId = undefined
  createForm.granteeType = 'system_account'
  createForm.granteeUsername = ''
  createForm.teamName = ''
  createForm.remark = ''
  createForm.expiresAt = undefined
  createForm.quotaLimits = createQuotaLimitForm()
  resourceOptions.value = []
  createOpen.value = true
  if (isManagementView.value) {
    await loadOwnerOptions()
  } else {
    await loadCreateResources()
  }
}

async function loadOwnerOptions(keyword = '') {
  ownerLoading.value = true
  try {
    const options = await api.systemAccounts.options({ keyword: keyword.trim() || undefined, limit: 50 })
    const next = options
      .filter((item) => item.status !== 'disabled')
      .map((item) => ({ value: item.id, label: `${item.displayName || item.username}（${item.username}）` }))
    const selected = ownerOptions.value.find((item) => item.value === createForm.ownerSystemAccountId)
    if (selected && !next.some((item) => item.value === selected.value)) {
      next.unshift(selected)
    }
    ownerOptions.value = next
  } catch (error) {
    message.error(errorMessage(error))
  } finally {
    ownerLoading.value = false
  }
}

function scheduleOwnerSearch(keyword: string) {
  ownerSearch.schedule(keyword)
}

async function handleOwnerChange() {
  resourceSearch.clear()
  resourceKeyword.value = ''
  createForm.resourceId = undefined
  await loadCreateResources()
}

async function handleResourceTypeChange() {
  resourceSearch.clear()
  resourceKeyword.value = ''
  createForm.resourceId = undefined
  await loadCreateResources()
}

async function loadCreateResources(keyword = '') {
  const ownerID = isManagementView.value ? createForm.ownerSystemAccountId : authState.currentUser.value?.id
  if (!ownerID) {
    resourceOptions.value = []
    return
  }
  resourceLoading.value = true
  const search = keyword.trim() || undefined
  try {
    let next: ResourceOption[]
    if (createForm.resourceType === 'account') {
      const accounts = isManagementView.value
        ? await api.accounts.options({ systemAccountId: ownerID, keyword: search, limit: 50 })
        : await api.myAccounts.options({ keyword: search, limit: 50 })
      next = accounts
        .filter((item) => !item.accountAuthorizationId)
        .map((item) => ({ value: item.id, label: `${item.name} · ${item.providerCode || item.type}` }))
    } else {
      const groups = isManagementView.value
        ? await api.groups.authorizationOptions({ systemAccountId: ownerID, keyword: search, limit: 50 })
        : await api.myGroups.authorizationOptions({ keyword: search, limit: 50 })
      next = groups.map((item) => ({ value: item.id, label: item.name }))
    }
    const selected = resourceOptions.value.find((item) => item.value === createForm.resourceId)
    if (selected && !next.some((item) => item.value === selected.value)) {
      next.unshift(selected)
    }
    resourceOptions.value = next
  } catch (error) {
    message.error(errorMessage(error))
  } finally {
    resourceLoading.value = false
  }
}

function scheduleResourceSearch(keyword: string) {
  resourceSearch.schedule(keyword)
}

async function confirmCreate() {
  const ownerID = createForm.ownerSystemAccountId
  if (isManagementView.value && !ownerID) {
    message.error('请选择资源所有者')
    return
  }
  if (!createForm.resourceId) {
    message.error('请选择授权资源')
    return
  }
  if (createForm.granteeType === 'system_account' && !createForm.granteeUsername.trim()) {
    message.error('请输入精确用户名')
    return
  }
  if (createForm.granteeType === 'team' && !createForm.teamName.trim()) {
    message.error('请输入精确团队名')
    return
  }
  const limits = quotaLimitsPayload(createForm.quotaLimits)
  const body = {
    resourceType: createForm.resourceType,
    resourceId: createForm.resourceId,
    granteeType: createForm.granteeType,
    ...(createForm.granteeType === 'system_account'
      ? { granteeUsername: createForm.granteeUsername.trim() }
      : { teamName: createForm.teamName.trim() }),
    ...(createForm.remark.trim() ? { remark: createForm.remark.trim() } : {}),
    ...(createForm.expiresAt?.isValid() ? { expiresAt: createForm.expiresAt.toISOString() } : {}),
    ...(Object.keys(limits).length ? { limits: limits as Record<string, unknown> } : {})
  }
  acting.value = true
  try {
    const result = isManagementView.value && ownerID
      ? await api.authorizationCenter.create(body, ownerID)
      : await api.myAuthorizationCenter.create(body)
    if (result.status === 'pending_owner_approval') {
      message.success('已提交，等待资源所有者确认后才会发送给接收方。')
    } else if (result.status === 'pending_team_approval') {
      message.success('已发送，等待团队审批人同意；成员确认后才生效。')
    } else {
      message.success('已发送待确认，接收方接受后需自行绑定分组。')
    }
    createOpen.value = false
    activeTab.value = isManagementView.value ? 'all' : 'outbound'
    await refresh()
  } catch (error) {
    message.error(errorMessage(error))
  } finally {
    acting.value = false
  }
}

async function openNotifications() {
  notificationsOpen.value = true
  await loadNotifications()
}

async function loadNotifications() {
  notificationsLoading.value = true
  try {
    const result = await api.myAuthorizationCenter.notifications(1, 100)
    notifications.value = result.items
  } catch (error) {
    message.error(errorMessage(error))
  } finally {
    notificationsLoading.value = false
  }
}

async function readNotification(id: string) {
  try {
    await api.myAuthorizationCenter.markNotificationRead(id)
    await loadNotifications()
    summary.value = await api.myAuthorizationCenter.inboxSummary()
  } catch (error) {
    message.error(errorMessage(error))
  }
}

function notificationLabel(type: string): string {
  const labels: Record<string, string> = {
    offer_pending: '收到新的授权',
    owner_approval_pending: '管理员代你发起授权，待确认',
    team_offer_pending_approval: '团队授权待审批',
    member_offer_pending: '团队授权待你确认',
    offer_suppressed: '授权请求已被屏蔽或限制',
    offer_declined: '授权被拒绝',
    source_needs_review: '授权来源发生变化，需重新确认',
    legacy_review_required: '存量授权需要复核',
    group_scope_changed: '授权分组范围发生变化',
    source_returned: '授权来源已归还',
    source_revoked: '授权来源已回收',
    grant_expired: '授权已到期'
  }
  return labels[type] || type
}

function notificationSummary(item: AuthorizationNotification): string {
  if (!item.payload || typeof item.payload !== 'object') return ''
  const payload = item.payload as Record<string, unknown>
  const resourceType = String(payload.resourceType || '')
  const resourceID = String(payload.resourceId || '')
  const reason = String(payload.reason || '')
  const resource = resourceType || resourceID ? `资源 ${resourceType || '—'} ${resourceID}`.trim() : ''
  return [resource, reason ? `原因：${reason}` : ''].filter(Boolean).join('；')
}

function errorMessage(error: unknown): string {
  if (error && typeof error === 'object' && 'response' in error) {
    const response = (error as { response?: { data?: { message?: string } } }).response
    if (response?.data?.message) return response.data.message
  }
  return error instanceof Error ? error.message : '操作失败'
}

watch(isManagementView, (management) => {
  activeTab.value = management ? 'all' : 'inbound'
  refresh()
})

onMounted(refresh)
</script>

<style scoped>
.authorization-center__title {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}
.authorization-center__primary {
  font-weight: 600;
  color: var(--text-color, #1f2329);
}
.authorization-center__muted {
  color: var(--text-color-secondary, #8a919f);
  font-size: 12px;
  line-height: 1.5;
}
.authorization-center__danger {
  color: #d4380d;
  font-size: 12px;
  line-height: 1.5;
}
</style>
