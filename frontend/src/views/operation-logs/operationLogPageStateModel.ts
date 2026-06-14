import { ref, type Ref } from 'vue'

import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type { OperationLogFilterValues, OperationLogPageWindow } from './operationLogFilters'
import {
  normalizeCreatedAtRange,
  parseCreatedAtRange,
  type CreatedAtRangeValue,
  type OperationLogsPageState
} from './operationLogPageState'

export interface OperationLogPageStateRefs {
  actionFilter: Ref<string>
  actorSystemAccountFilter: Ref<string>
  actorSystemAccountSelection: Ref<PrincipalSelection | undefined>
  affectedSystemAccountFilter: Ref<string>
  affectedSystemAccountSelection: Ref<PrincipalSelection | undefined>
  createdAtRange: Ref<CreatedAtRangeValue>
  resourceIdFilter: Ref<string>
  resourceTypeFilter: Ref<string>
  summaryKeywordFilter: Ref<string>
  moduleFilter: Ref<string>
  operationScopeSystemAccountFilter: Ref<string>
  operationScopeSystemAccountSelection: Ref<PrincipalSelection | undefined>
  traceIdFilter: Ref<string>
}

export function createOperationLogPageStateRefs(state: OperationLogsPageState): OperationLogPageStateRefs {
  return {
    actionFilter: ref(state.actionFilter),
    actorSystemAccountFilter: ref(state.actorSystemAccountFilter),
    actorSystemAccountSelection: ref(state.actorSystemAccountSelection),
    affectedSystemAccountFilter: ref(state.affectedSystemAccountFilter),
    affectedSystemAccountSelection: ref(state.affectedSystemAccountSelection),
    createdAtRange: ref(parseCreatedAtRange(state.createdAtRange)),
    resourceIdFilter: ref(state.resourceIdFilter),
    resourceTypeFilter: ref(state.resourceTypeFilter),
    summaryKeywordFilter: ref(state.summaryKeywordFilter),
    moduleFilter: ref(state.moduleFilter),
    operationScopeSystemAccountFilter: ref(state.operationScopeSystemAccountFilter),
    operationScopeSystemAccountSelection: ref(state.operationScopeSystemAccountSelection),
    traceIdFilter: ref(state.traceIdFilter)
  }
}

export function applyOperationLogPageState(
  refs: OperationLogPageStateRefs,
  pagination: OperationLogPageWindow,
  state: OperationLogsPageState
): void {
  refs.actionFilter.value = state.actionFilter
  refs.actorSystemAccountFilter.value = state.actorSystemAccountFilter
  refs.actorSystemAccountSelection.value = state.actorSystemAccountSelection
  refs.affectedSystemAccountFilter.value = state.affectedSystemAccountFilter
  refs.affectedSystemAccountSelection.value = state.affectedSystemAccountSelection
  refs.createdAtRange.value = parseCreatedAtRange(state.createdAtRange)
  refs.resourceIdFilter.value = state.resourceIdFilter
  refs.resourceTypeFilter.value = state.resourceTypeFilter
  refs.summaryKeywordFilter.value = state.summaryKeywordFilter
  refs.moduleFilter.value = state.moduleFilter
  refs.operationScopeSystemAccountFilter.value = state.operationScopeSystemAccountFilter
  refs.operationScopeSystemAccountSelection.value = state.operationScopeSystemAccountSelection
  refs.traceIdFilter.value = state.traceIdFilter
  pagination.current = state.pagination.current
  pagination.pageSize = state.pagination.pageSize
}

export function operationLogFilterValuesFromRefs(refs: OperationLogPageStateRefs): OperationLogFilterValues {
  return {
    actionFilter: refs.actionFilter.value,
    actorSystemAccountFilter: refs.actorSystemAccountFilter.value,
    affectedSystemAccountFilter: refs.affectedSystemAccountFilter.value,
    createdAtRange: refs.createdAtRange.value,
    resourceIdFilter: refs.resourceIdFilter.value,
    resourceTypeFilter: refs.resourceTypeFilter.value,
    summaryKeywordFilter: refs.summaryKeywordFilter.value,
    moduleFilter: refs.moduleFilter.value,
    operationScopeSystemAccountFilter: refs.operationScopeSystemAccountFilter.value,
    traceIdFilter: refs.traceIdFilter.value
  }
}

export function snapshotOperationLogPageState(
  refs: OperationLogPageStateRefs,
  pagination: OperationLogPageWindow
): OperationLogsPageState {
  const range = normalizeCreatedAtRange(refs.createdAtRange.value)
  return {
    actionFilter: refs.actionFilter.value,
    actorSystemAccountFilter: refs.actorSystemAccountFilter.value,
    actorSystemAccountSelection: refs.actorSystemAccountSelection.value,
    affectedSystemAccountFilter: refs.affectedSystemAccountFilter.value,
    affectedSystemAccountSelection: refs.affectedSystemAccountSelection.value,
    createdAtRange: range ? [range[0].toISOString(), range[1].toISOString()] : undefined,
    resourceIdFilter: refs.resourceIdFilter.value,
    resourceTypeFilter: refs.resourceTypeFilter.value,
    summaryKeywordFilter: refs.summaryKeywordFilter.value,
    moduleFilter: refs.moduleFilter.value,
    operationScopeSystemAccountFilter: refs.operationScopeSystemAccountFilter.value,
    operationScopeSystemAccountSelection: refs.operationScopeSystemAccountSelection.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize },
    traceIdFilter: refs.traceIdFilter.value
  }
}
