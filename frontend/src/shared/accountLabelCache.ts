import {
  mergeSelectedSelectOptions,
  rememberSelectOption,
  selectLabelForValue,
  type SelectOption
} from './selectLabelCache'

export type { SelectOption } from './selectLabelCache'

export interface AccountSelection {
  id: string
  name: string
}

export interface AccountOptionLike {
  id: string
  name: string
}

const defaultAccountCacheKey = 'accounts'

export function rememberAccountLabels(accounts: AccountOptionLike[], cacheKey = defaultAccountCacheKey): void {
  for (const account of accounts) {
    rememberAccountLabel(account.id, account.name, cacheKey)
  }
}

export function rememberAccountLabel(id: string | undefined, name: string | undefined, cacheKey = defaultAccountCacheKey): void {
  rememberSelectOption(cacheKey, id, name)
}

export function rememberAccountSelection(selection: AccountSelection | undefined, cacheKey = defaultAccountCacheKey): void {
  rememberAccountLabel(selection?.id, selection?.name, cacheKey)
}

export function rememberAccountSelections(selections: Array<AccountSelection | undefined>, cacheKey = defaultAccountCacheKey): void {
  for (const selection of selections) {
    rememberAccountSelection(selection, cacheKey)
  }
}

export function accountLabelForId(id: string | undefined, cacheKey = defaultAccountCacheKey): string | undefined {
  return selectLabelForValue(cacheKey, id)
}

export function accountSelectionForId(
  id: string | undefined,
  accounts: AccountOptionLike[] = [],
  options: SelectOption[] = [],
  cacheKey = defaultAccountCacheKey
): AccountSelection | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  const account = accounts.find((item) => item.id === normalizedId)
  if (account?.name?.trim()) return { id: normalizedId, name: account.name.trim() }
  const option = options.find((item) => item.value === normalizedId)
  if (option?.label?.trim()) return { id: normalizedId, name: option.label.trim() }
  const cached = accountLabelForId(normalizedId, cacheKey)
  return cached ? { id: normalizedId, name: cached } : undefined
}

export function accountSelectOptionLabel(account: AccountOptionLike): string {
  return account.name
}

export function mergeSelectedAccountOptions(
  cacheKey: string,
  options: SelectOption[],
  selectedIds: Array<string | undefined>,
  selectedAccounts: Array<AccountSelection | undefined> = []
): SelectOption[] {
  return mergeSelectedSelectOptions(
    cacheKey,
    options,
    selectedIds,
    selectedAccounts.map((account) => account ? { label: account.name, value: account.id } : undefined)
  )
}
