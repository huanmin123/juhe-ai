import type {
  PublicApiLogDetailSupplement,
  PublicApiLogListItem,
  PublicApiLogRenderedDetail
} from '@/types/domain'

export function mergePublicApiLogDetail(
  row: PublicApiLogListItem,
  supplement: PublicApiLogDetailSupplement
): PublicApiLogRenderedDetail {
  return {
    ...row,
    ...supplement
  }
}
