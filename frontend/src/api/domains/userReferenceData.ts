import type { UserReferenceData } from '@/types/domain'
import { http, unwrap } from '../http'

export interface UserReferenceDataAdminParams {
  systemAccountId: string
}

export const uiBootstrapApi = {
  options: (params: UserReferenceDataAdminParams) => unwrap<UserReferenceData>(http.get('/ui-bootstrap/options', { params }))
}

export const myUiBootstrapApi = {
  options: () => unwrap<UserReferenceData>(http.get('/my-ui-bootstrap/options'))
}
