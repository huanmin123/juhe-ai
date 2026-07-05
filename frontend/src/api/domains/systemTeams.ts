import type { SystemTeamListResult, SystemTeamSummary } from '@/types/domain'
import type { TeamListParams } from '../contracts'
import { http, unwrap } from '../http'
import { teamListParams } from '../params'

export const systemTeamsApi = {
  list: (params?: TeamListParams) => unwrap<SystemTeamListResult>(http.get('/system-teams', { params: teamListParams(params) })),
  detail: (id: string, params?: TeamListParams) => unwrap<SystemTeamSummary>(http.get(`/system-teams/${id}`, { params: teamListParams(params) })),
  create: (payload: { name: string; description?: string; status?: 'active' | 'disabled' }) => unwrap<SystemTeamSummary>(http.post('/system-teams', payload)),
  update: (id: string, payload: { name?: string; description?: string; status?: 'active' | 'disabled' }) => unwrap<SystemTeamSummary>(http.patch(`/system-teams/${id}`, payload)),
  addMembers: (id: string, payload: { systemAccountIds: string[] }) => unwrap<SystemTeamSummary>(http.post(`/system-teams/${id}/members`, payload)),
  removeMember: (id: string, memberId: string) => unwrap<SystemTeamSummary>(http.delete(`/system-teams/${id}/members/${memberId}`))
}

export const myTeamsApi = {
  list: (params?: Omit<TeamListParams, 'systemAccountId'>) => unwrap<SystemTeamListResult>(http.get('/my-teams', { params: teamListParams(params, false) })),
  detail: (id: string) => unwrap<SystemTeamSummary>(http.get(`/my-teams/${id}`))
}
