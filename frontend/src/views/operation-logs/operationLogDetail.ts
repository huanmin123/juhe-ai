import type {
  OperationLogDetailSupplement,
  OperationLogListItem,
  OperationLogRenderedDetail
} from '@/types/domain'

export function mergeOperationLogDetail(
  row: OperationLogListItem,
  supplement: OperationLogDetailSupplement
): OperationLogRenderedDetail {
  return {
    ...row,
    ...supplement
  }
}
