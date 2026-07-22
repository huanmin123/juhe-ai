import dayjs, { type Dayjs } from 'dayjs'

import type { PrincipalSelection } from '@/shared/principalLabelCache'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'

export type OperationLogsPageState = {
  actionFilter: string
  actorSystemAccountFilter: string
  actorSystemAccountSelection?: PrincipalSelection
  affectedSystemAccountFilter: string
  affectedSystemAccountSelection?: PrincipalSelection
  createdAtRange?: [string, string]
  resourceIdFilter: string
  resourceTypeFilter: string
  summaryKeywordFilter: string
  moduleFilter: string
  operationScopeSystemAccountFilter: string
  operationScopeSystemAccountSelection?: PrincipalSelection
  pagination: { current: number; pageSize: number }
  traceIdFilter: string
}

export type CreatedAtRangeValue = [Dayjs | null | undefined, Dayjs | null | undefined] | null | undefined

export const defaultOperationLogsPageState = (pageSize: number): OperationLogsPageState => ({
  actionFilter: 'all',
  actorSystemAccountFilter: allSystemAccountsValue,
  actorSystemAccountSelection: undefined,
  affectedSystemAccountFilter: allSystemAccountsValue,
  affectedSystemAccountSelection: undefined,
  createdAtRange: undefined,
  resourceIdFilter: '',
  resourceTypeFilter: 'all',
  summaryKeywordFilter: '',
  moduleFilter: 'all',
  operationScopeSystemAccountFilter: allSystemAccountsValue,
  operationScopeSystemAccountSelection: undefined,
  pagination: { current: 1, pageSize },
  traceIdFilter: ''
})

export function parseCreatedAtRange(value?: [string, string]): [Dayjs, Dayjs] | undefined {
  if (!value) return undefined
  const start = dayjs(value[0])
  const end = dayjs(value[1])
  return normalizeCreatedAtRange(start.isValid() && end.isValid() ? [start, end] : undefined)
}

export function normalizeCreatedAtRange(value: CreatedAtRangeValue): [Dayjs, Dayjs] | undefined {
  const start = value?.[0]
  const end = value?.[1]
  if (!start?.isValid() || !end?.isValid()) return undefined
  return start.isAfter(end) ? [end, start] : [start, end]
}
