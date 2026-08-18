import type { AccountSummary } from '../../domain/types.js'
import type { AccountHealthJobsInputRevisions } from '../../storage/account-health-jobs-input.repository.js'

// A gateway failure can refer to an account outside the frozen J1 scope.
// Such an account intentionally has no J1 input epoch and must not be
// treated as a failed request publication.
export interface CurrentAccountHealthJobsProbeInput {
  account: AccountSummary
  inputVersion: number
}

export function currentAccountHealthJobsProbeInput(
  account: AccountSummary | undefined,
  inputVersion: number | undefined,
  revisions: AccountHealthJobsInputRevisions | undefined
): CurrentAccountHealthJobsProbeInput | undefined {
  if (
    account === undefined ||
    inputVersion === undefined ||
    !Number.isSafeInteger(inputVersion) ||
    inputVersion < 1 ||
    revisions === undefined ||
    revisions.configRevision !== account.configRevision
  ) return undefined
  return { account: { ...account, dispatchRevision: revisions.dispatchRevision }, inputVersion }
}
