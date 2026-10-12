// 调度偏好独立实体 P2——公开策略 add/update 的旧调度字段退场拒绝（计划 D6：
// 「P2 公开策略 add/update 的旧配置字段退场需明确报错，不静默忽略」）。判定
// 对象是请求 payload 的 normalRoutingConfig，四个退场键（含旧别名
// firstByteThresholdMs）一律 400 专用文案；不携带退场键的载荷不受影响。
package aipublic

import (
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

func TestSPC2PublicStrategyAddRejectsRetiredSchedulingFields(t *testing.T) {
	env := newWCLifeEnv(t)
	groupID := wcAddGroup(t, env, "pusher", "spc2-组")

	cases := []struct {
		name string
		key  string
	}{
		{"schedulingPreference", `"schedulingPreference":"speed_first"`},
		{"speedFirstConfig", `"speedFirstConfig":{"slowTriggerCount":3}`},
		{"firstByteDeadlineMs", `"firstByteDeadlineMs":30000`},
		{"firstByteThresholdMs", `"firstByteThresholdMs":30000`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body := `{"targetUsername":"pusher","name":"spc2-` + testCase.name + `","groupBindings":[{"groupId":"` + groupID + `"}],"normalRoutingConfig":{` + testCase.key + `}}`
			status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/route-strategy/add", body, wcLifeToken)
			if status != http.StatusBadRequest {
				t.Fatalf("add 携带 %s 必须 400: %d %v", testCase.name, status, payload)
			}
			if payload["message"] != routestrategies.LegacySchedulingConfigRejectedMessage {
				t.Fatalf("专用文案: %v", payload["message"])
			}
		})
	}
}

func TestSPC2PublicStrategyUpdateRejectsRetiredSchedulingFields(t *testing.T) {
	env := newWCLifeEnv(t)
	groupID := wcAddGroup(t, env, "pusher", "spc2-upd-组")
	strategyID := wcAddStrategy(t, env, "pusher", "spc2-存量", groupID)

	cases := []struct {
		name string
		key  string
	}{
		{"schedulingPreference", `"schedulingPreference":"cost_first"`},
		{"speedFirstConfig", `"speedFirstConfig":{"totalTimeDeadlineSeconds":120}`},
		{"firstByteDeadlineMs", `"firstByteDeadlineMs":20000`},
		{"firstByteThresholdMs", `"firstByteThresholdMs":20000`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body := `{"targetUsername":"pusher","routeStrategyId":"` + strategyID + `","normalRoutingConfig":{` + testCase.key + `}}`
			status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/route-strategy/update", body, wcLifeToken)
			if status != http.StatusBadRequest {
				t.Fatalf("update 携带 %s 必须 400: %d %v", testCase.name, status, payload)
			}
			if payload["message"] != routestrategies.LegacySchedulingConfigRejectedMessage {
				t.Fatalf("专用文案: %v", payload["message"])
			}
		})
	}

	// 不携带退场键的 update（改名）不受影响。
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/route-strategy/update",
		`{"targetUsername":"pusher","routeStrategyId":"`+strategyID+`","name":"spc2-改名"}`, wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("无退场键 update 必须 200: %d %v", status, payload)
	}
}
