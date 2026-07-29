import type {
  SystemTeamDetail,
  SystemTeamListItem,
  SystemTeamListResult,
  SystemTeamMemberHistoryResult,
  SystemTeamMemberListResult,
  SystemTeamMemberRemovedResult,
  SystemTeamMembersAddedResult,
  SystemTeamMutationResult
} from '@/types/domain'
import type { TeamListParams } from '../contracts'
import { http, unwrap } from '../http'
import { teamListParams } from '../params'

export const systemTeamsApi = {
  list: (params?: TeamListParams) => unwrap<SystemTeamListResult>(http.get('/system-teams', { params: teamListParams(params) })),
  detail: (id: string, params?: TeamListParams) => unwrap<SystemTeamDetail>(http.get(`/system-teams/${id}`, { params: teamListParams(params) })),
  members: (id: string, params?: TeamListParams) => unwrap<SystemTeamMemberListResult>(http.get(`/system-teams/${id}/members`, { params: teamListParams(params) })),
  memberHistory: (id: string, params?: TeamListParams) => unwrap<SystemTeamMemberHistoryResult>(http.get(`/system-teams/${id}/members/history`, { params: teamListParams(params) })),
  create: (payload: { name: string; description?: string; status?: 'active' | 'disabled' }, params?: TeamListParams) => unwrap<SystemTeamListItem>(http.post('/system-teams', payload, params ? { params: teamListParams(params) } : undefined)),
  update: (id: string, payload: { expectedUpdatedAt: string; name?: string; description?: string | null; status?: 'active' | 'disabled' }, params?: TeamListParams) => unwrap<SystemTeamMutationResult>(http.patch(`/system-teams/${id}`, payload, params ? { params: teamListParams(params) } : undefined)),
  addMembers: (id: string, payload: { systemAccountIds: string[]; expectedUpdatedAt: string }, params?: TeamListParams) => unwrap<SystemTeamMembersAddedResult>(http.post(`/system-teams/${id}/members`, payload, params ? { params: teamListParams(params) } : undefined)),
  removeMember: (id: string, memberId: string, payload: { expectedUpdatedAt: string }, params?: TeamListParams) => unwrap<SystemTeamMemberRemovedResult>(http.delete(`/system-teams/${id}/members/${memberId}`, {
    data: payload,
    ...(params ? { params: teamListParams(params) } : {})
  }))
}

export const myTeamsApi = {
  list: (params?: Omit<TeamListParams, 'systemAccountId'>) => unwrap<SystemTeamListResult>(http.get('/my-teams', { params: teamListParams(params, false) })),
  detail: (id: string) => unwrap<SystemTeamDetail>(http.get(`/my-teams/${id}`)),
  members: (id: string) => unwrap<SystemTeamMemberListResult>(http.get(`/my-teams/${id}/members`)),
  memberHistory: (id: string, params?: Omit<TeamListParams, 'systemAccountId'>) => unwrap<SystemTeamMemberHistoryResult>(http.get(`/my-teams/${id}/members/history`, { params: teamListParams(params, false) }))
}
