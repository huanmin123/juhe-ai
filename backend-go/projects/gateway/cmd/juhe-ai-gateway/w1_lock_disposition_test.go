package main

import (
	"net/http"
	"testing"
)

// TestAccountErrorPolicyFailureDisposition 锁定 D12 锁死处置轴：
// 确定性硬门释放，其余可恢复失败继续 hold。
func TestAccountErrorPolicyFailureDisposition(t *testing.T) {
	cases := []struct {
		name     string
		decision *accountErrorPolicyDecision
		status   int
		has      bool
		want     string
	}{
		{"无规则 500 可恢复", nil, http.StatusInternalServerError, true, failureDispositionTransientHold},
		{"无规则 429 可恢复", nil, http.StatusTooManyRequests, true, failureDispositionTransientHold},
		{"网络失败可恢复", nil, 0, false, failureDispositionTransientHold},
		{"disable 硬门", &accountErrorPolicyDecision{Action: decisionActionDisable}, http.StatusInternalServerError, true, failureDispositionDeterministicRelease},
		{"系统额度耗尽硬门", &accountErrorPolicyDecision{Action: decisionActionCooldown, RuleSource: "system", RuleID: systemInsufficientQuotaRuleID}, http.StatusTooManyRequests, true, failureDispositionDeterministicRelease},
		{"401 凭据无效硬门", &accountErrorPolicyDecision{Action: decisionActionCooldown, RuleSource: "account"}, http.StatusUnauthorized, true, failureDispositionDeterministicRelease},
		{"403 凭据无效硬门", &accountErrorPolicyDecision{Action: decisionActionCooldown, RuleSource: "account"}, http.StatusForbidden, true, failureDispositionDeterministicRelease},
		{"404 模型/端点硬门", &accountErrorPolicyDecision{Action: decisionActionCooldown, RuleSource: "account"}, http.StatusNotFound, true, failureDispositionDeterministicRelease},
		{"400 请求硬门", &accountErrorPolicyDecision{Action: decisionActionCooldown, RuleSource: "account"}, http.StatusBadRequest, true, failureDispositionDeterministicRelease},
		{"422 请求硬门", &accountErrorPolicyDecision{Action: decisionActionCooldown, RuleSource: "account"}, http.StatusUnprocessableEntity, true, failureDispositionDeterministicRelease},
		{"429 显式策略仍 hold", &accountErrorPolicyDecision{Action: decisionActionCooldown, RuleSource: "account"}, http.StatusTooManyRequests, true, failureDispositionTransientHold},
		{"500 显式策略仍 hold", &accountErrorPolicyDecision{Action: decisionActionCooldown, RuleSource: "account"}, http.StatusInternalServerError, true, failureDispositionTransientHold},
		{"retry_next 仍 hold", &accountErrorPolicyDecision{Action: decisionActionRetryNext}, http.StatusInternalServerError, true, failureDispositionTransientHold},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := accountErrorPolicyFailureDisposition(tc.decision, tc.status, tc.has); got != tc.want {
				t.Fatalf("disposition = %s, want %s", got, tc.want)
			}
		})
	}
}
