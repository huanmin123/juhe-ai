export class AccountConfigRevisionConflictError extends Error {
  constructor(
    readonly accountId: string,
    readonly expectedConfigRevision: number,
    readonly actualConfigRevision?: number
  ) {
    super(`账户配置已发生并发变更，请重试：${accountId}`)
    this.name = 'AccountConfigRevisionConflictError'
  }
}
