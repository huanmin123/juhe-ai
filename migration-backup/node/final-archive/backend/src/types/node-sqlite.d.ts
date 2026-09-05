declare module 'node:sqlite' {
  export interface SQLiteRunResult {
    changes: number
    lastInsertRowid?: number | bigint
  }

  export class StatementSync {
    constructor(sql: string)
    run(...params: unknown[]): SQLiteRunResult
    get(...params: unknown[]): Record<string, unknown> | undefined
    all(...params: unknown[]): Record<string, unknown>[]
  }

  export class DatabaseSync {
    constructor(path: string)
    readonly isTransaction: boolean
    exec(sql: string): void
    prepare(sql: string): StatementSync
    close(): void
  }

  export const constants: Record<string, number>
  export function backup(...params: unknown[]): Promise<void>
}
