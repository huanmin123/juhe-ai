import axios from 'axios'

export function requiredOAuthConfigRevision(account: { configRevision?: number }): number | undefined {
  const revision = Number(account.configRevision)
  return Number.isInteger(revision) && revision >= 1 ? revision : undefined
}

export function isOAuthConfigRevisionConflict(error: unknown): boolean {
  return axios.isAxiosError(error) && error.response?.status === 409
}
