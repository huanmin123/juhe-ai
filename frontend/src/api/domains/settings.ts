import type { GlobalSettings, SystemSettings, SystemSettingsPatch } from '@/types/domain'
import { http, unwrap } from '../http'

export type ManagementSettingsSectionKey = 'brand' | 'gateway-core' | 'user-request-limit' | 'account-health' | 'api-rate-limit' | 'cooldown-retest' | 'data-retention'
export type ManagementSettingsSectionValues = Record<string, string | number>

export const settingsApi = {
  public: () => unwrap<GlobalSettings>(http.get('/settings/public')),
  global: () => unwrap<GlobalSettings>(http.get('/settings/global')),
  updateGlobal: (payload: GlobalSettings) => unwrap<GlobalSettings>(http.patch('/settings/global', payload)),
  get: () => unwrap<SystemSettings>(http.get('/settings')),
  update: (payload: SystemSettingsPatch) => unwrap<SystemSettings>(http.patch('/settings', payload)),
  section: (sectionKey: ManagementSettingsSectionKey) => unwrap<{ sectionKey: ManagementSettingsSectionKey; values: ManagementSettingsSectionValues }>(http.get(`/settings/sections/${sectionKey}`)),
  updateSection: (sectionKey: ManagementSettingsSectionKey, payload: Record<string, string | number>) => unwrap<{ sectionKey: ManagementSettingsSectionKey; values: ManagementSettingsSectionValues }>(http.patch(`/settings/sections/${sectionKey}`, payload))
}
