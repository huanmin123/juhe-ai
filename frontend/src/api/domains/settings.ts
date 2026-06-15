import type { GlobalSettings, SystemSettings, SystemSettingsPatch } from '@/types/domain'
import { http, unwrap } from '../http'

export const settingsApi = {
  public: () => unwrap<GlobalSettings>(http.get('/settings/public')),
  global: () => unwrap<GlobalSettings>(http.get('/settings/global')),
  updateGlobal: (payload: GlobalSettings) => unwrap<GlobalSettings>(http.patch('/settings/global', payload)),
  get: () => unwrap<SystemSettings>(http.get('/settings')),
  update: (payload: SystemSettingsPatch) => unwrap<SystemSettings>(http.patch('/settings', payload))
}
