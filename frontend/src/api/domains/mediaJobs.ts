import type { MediaJobListResult } from '@/types/domain'
import type { MediaJobListParams } from '../contracts'
import { http, unwrap } from '../http'

export const mediaJobsApi = {
  list: (params?: MediaJobListParams) => unwrap<MediaJobListResult>(http.get('/media-jobs', { params }))
}
