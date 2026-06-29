import type { AccountResponseInspectionRuleForm } from './accountResponseInspectionPolicyTypes'

export const makeAccountResponseInspectionRule = (patch: Partial<AccountResponseInspectionRuleForm>): AccountResponseInspectionRuleForm => ({
  enabled: true,
  name: '',
  priority: 1,
  clientProfiles: [],
  outputTextIncludes: '',
  outputTextExcludes: '',
  errorCodes: '',
  errorTypes: '',
  errorMessageIncludes: '',
  finishReasons: '',
  jsonPathsExists: '',
  rawTextIncludes: '',
  action: 'retry_next_account',
  notes: '',
  ...patch
})

export const createBlankAccountResponseInspectionRule = (priority = 1): AccountResponseInspectionRuleForm => makeAccountResponseInspectionRule({
  name: '账户响应检查规则',
  priority
})

export const normalizeAccountResponseInspectionPriorities = (rules: AccountResponseInspectionRuleForm[]): AccountResponseInspectionRuleForm[] => {
  return rules.map((rule, index) => ({ ...rule, priority: index + 1 }))
}

export const nextAccountResponseInspectionPriority = (rules: AccountResponseInspectionRuleForm[]): number => {
  const used = new Set(rules
    .map((rule) => rule.priority)
    .filter((priority): priority is number => typeof priority === 'number' && Number.isInteger(priority) && priority > 0 && priority <= 9999))
  for (let priority = 1; priority <= 9999; priority += 1) {
    if (!used.has(priority)) return priority
  }
  return 9999
}
