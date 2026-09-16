package accounts

// w10a 第四批：m11 读取投影（valueOrSource / geminiOAuthContextType）、错误
// 类型消息、Key 池供应商支持判定、授权调度不可用消息全分支、retryQueue 清理。

import (
	"context"
	"database/sql"
	"testing"
)

func TestW10AValueOrSource(t *testing.T) {
	owner := sql.NullString{String: "owner-val", Valid: true}
	missing := sql.NullString{}
	source := sql.NullString{String: "src-val", Valid: true}
	if valueOrSource(owner, source, true) != "src-val" {
		t.Fatal("授权模式应返回 source")
	}
	if valueOrSource(owner, missing, true) != "" {
		t.Fatal("授权模式缺失 source 应返回空")
	}
	if valueOrSource(owner, source, false) != "owner-val" {
		t.Fatal("owner 模式应返回 owner")
	}
	if valueOrSource(missing, source, false) != "" {
		t.Fatal("owner 模式缺失 owner 应返回空")
	}
}

func TestW10AGeminiOAuthContextType(t *testing.T) {
	base := "https://api.example.com"
	if got := geminiOAuthContextType(Credentials{"oauth_type": "google_one"}); got != "google_one" {
		t.Fatalf("枚举透传不一致：%s", got)
	}
	if got := geminiOAuthContextType(Credentials{"oauth_type": "ai_studio"}); got != "ai_studio" {
		t.Fatalf("枚举透传不一致：%s", got)
	}
	if got := geminiOAuthContextType(Credentials{"oauth_type": "code_assist"}); got != "code_assist" {
		t.Fatalf("枚举透传不一致：%s", got)
	}
	if got := geminiOAuthContextType(Credentials{"base_url": "https://generativelanguage.googleapis.com/v1"}); got != "ai_studio" {
		t.Fatalf("generativelanguage 应 ai_studio：%s", got)
	}
	if got := geminiOAuthContextType(Credentials{"project_id": "proj-1", "base_url": base}); got != "code_assist" {
		t.Fatalf("project_id 应 code_assist：%s", got)
	}
	if got := geminiOAuthContextType(Credentials{"base_url": "https://cloudcode-pa.googleapis.com"}); got != "code_assist" {
		t.Fatalf("cloudcode-pa 应 code_assist：%s", got)
	}
	if got := geminiOAuthContextType(Credentials{"client_id": "custom-client", "base_url": base}); got != "ai_studio" {
		t.Fatalf("自定义 client_id 应 ai_studio：%s", got)
	}
	if got := geminiOAuthContextType(Credentials{"client_id": geminiCLIOAuthClientID, "base_url": base}); got != "code_assist" {
		t.Fatalf("CLI client_id 应 code_assist：%s", got)
	}
	if got := geminiOAuthContextType(Credentials{"base_url": base}); got != "code_assist" {
		t.Fatalf("默认应 code_assist：%s", got)
	}
}

func TestW10AErrorTypeMessages(t *testing.T) {
	if (&interactionContextForbiddenError{message: "授权实例不能克隆"}).Error() != "授权实例不能克隆" {
		t.Fatal("interactionContextForbiddenError 消息不一致")
	}
	if (&AuthorizedDispatchRevisionConflictError{AccountID: "acc-1", ExpectedConfigRevision: 3}).Error() != "授权账户配置已发生并发变更，请重试：acc-1" {
		t.Fatal("授权分发冲突消息不一致")
	}
	if (&trafficMigrationFailure{message: "目标账户不能和当前账户相同"}).Error() != "目标账户不能和当前账户相同" {
		t.Fatal("流量迁移失败消息不一致")
	}
	if errTrafficSameAccount.Error() != "目标账户不能和当前账户相同" {
		t.Fatal("errTrafficSameAccount 消息不一致")
	}
}

func TestW10AIsAPIKeyPoolProviderSupported(t *testing.T) {
	// openai 兼容 / gpt。
	for _, code := range []string{openAICompatibleProviderCodeConstant, gptVendorCode, "OPENAI", "Gpt"} {
		if !isAccountAPIKeyPoolProviderSupported(code, "", "") {
			t.Fatalf("%s 应支持 API Key 池", code)
		}
	}
	// deepseek / glm / gemini。
	for _, code := range []string{"deepseek", "glm", "gemini"} {
		if !isAccountAPIKeyPoolProviderSupported(code, "", "") {
			t.Fatalf("%s 应支持 API Key 池", code)
		}
	}
	// anthropic 协议档案。
	if !isAccountAPIKeyPoolProviderSupported("", anthropicProtocolCodeConstant, anthropicProtocolVersionConstant) {
		t.Fatal("anthropic 协议档案应支持")
	}
	// anthropic 供应商代码。
	if !isAccountAPIKeyPoolProviderSupported(anthropicProviderCode, "", "") {
		t.Fatal("anthropic 供应商应支持")
	}
	if isAccountAPIKeyPoolProviderSupported("weird-vendor", "", "") {
		t.Fatal("未识别供应商不应支持")
	}
}

func TestW10AAuthorizedDispatchUnavailableMessage(t *testing.T) {
	env := newTestEnv(t)
	healthy := authorizedDispatchRow{
		authorizationStatus:    sql.NullString{String: "active", Valid: true},
		authorizationExpiresAt: sql.NullString{String: "2030-01-01T00:00:00Z", Valid: true},
		sourceID:               sql.NullString{String: "acc-src", Valid: true},
		sourceStatus:           sql.NullString{String: "active", Valid: true},
		sourceSchedulable:      sql.NullInt64{Int64: 1, Valid: true},
		sourceAccountExpiresAt: sql.NullString{String: "2030-01-01T00:00:00Z", Valid: true},
		accountExpiresAt:       sql.NullString{String: "2030-01-01T00:00:00Z", Valid: true},
		status:                 "active",
	}
	base := func() authorizedDispatchRow {
		row := healthy
		row.status = "disabled"
		return row
	}
	cases := []struct {
		name string
		make func() authorizedDispatchRow
		want string
	}{
		{"授权到期状态", func() authorizedDispatchRow {
			row := base()
			row.authorizationStatus = sql.NullString{String: "expired", Valid: true}
			return row
		}, "授权已到期，当前账户不能调用"},
		{"授权暂停", func() authorizedDispatchRow {
			row := base()
			row.authorizationStatus = sql.NullString{String: "paused", Valid: true}
			return row
		}, "授权已暂停，当前账户不能调用"},
		{"授权撤销", func() authorizedDispatchRow {
			row := base()
			row.authorizationStatus = sql.NullString{String: "revoked", Valid: true}
			return row
		}, "授权关系已失效，当前账户不能调用"},
		{"授权到期时间已过", func() authorizedDispatchRow {
			row := base()
			row.authorizationExpiresAt = sql.NullString{String: "2020-01-01T00:00:00Z", Valid: true}
			return row
		}, "授权已到期，当前账户不能调用"},
		{"来源缺失", func() authorizedDispatchRow {
			row := base()
			row.sourceID = sql.NullString{}
			return row
		}, "授权方原账户不存在或已删除，当前账户不能调用"},
		{"来源状态为空", func() authorizedDispatchRow {
			row := base()
			row.sourceStatus = sql.NullString{}
			return row
		}, "授权方原账户不存在或已删除，当前账户不能调用"},
		{"来源过期标记", func() authorizedDispatchRow {
			row := base()
			row.sourceLastErrorCode = sql.NullString{String: "account_expired", Valid: true}
			return row
		}, "授权方原账户已到期，当前账户不能调用"},
		{"来源停用", func() authorizedDispatchRow {
			row := base()
			row.sourceStatus = sql.NullString{String: "disabled", Valid: true}
			return row
		}, "授权方原账户已停用，当前账户不能调用"},
		{"来源待检查", func() authorizedDispatchRow {
			row := base()
			row.sourceStatus = sql.NullString{String: "pending_test", Valid: true}
			return row
		}, "授权方原账户尚未通过后台健康检查，当前账户不能调用"},
		{"来源异常带消息", func() authorizedDispatchRow {
			row := base()
			row.sourceStatus = sql.NullString{String: "error", Valid: true}
			row.sourceLastErrorMessage = sql.NullString{String: "源异常", Valid: true}
			return row
		}, "源异常"},
		{"来源限流", func() authorizedDispatchRow {
			row := base()
			row.sourceStatus = sql.NullString{String: "rate_limited", Valid: true}
			return row
		}, "授权方原账户限流中，当前账户不能调用"},
		{"来源临时不可用", func() authorizedDispatchRow {
			row := base()
			row.sourceStatus = sql.NullString{String: "temporary_unavailable", Valid: true}
			return row
		}, "授权方原账户临时不可调用，当前账户不能调用"},
		{"来源质量隔离", func() authorizedDispatchRow {
			row := base()
			row.sourceStatus = sql.NullString{String: "quality_isolated", Valid: true}
			return row
		}, "授权方原账户因模型质量不达标已隔离，恢复前不能调用"},
		{"来源冷却中", func() authorizedDispatchRow {
			row := base()
			row.sourceCooldownUntil = sql.NullString{String: "2030-01-01T00:00:00Z", Valid: true}
			return row
		}, "授权方原账户正在冷却，恢复前当前账户不能调用"},
		{"来源关闭调度", func() authorizedDispatchRow {
			row := base()
			row.sourceSchedulable = sql.NullInt64{Int64: 0, Valid: true}
			return row
		}, "授权方原账户已关闭调度，当前账户不能调用"},
		{"授权账户到期", func() authorizedDispatchRow {
			row := base()
			row.lastErrorCode = sql.NullString{String: "account_expired", Valid: true}
			return row
		}, "授权账户已到期，当前不可用"},
		{"本地停用", func() authorizedDispatchRow {
			return base()
		}, "授权账户已停用，当前不可用"},
		{"本地待检查", func() authorizedDispatchRow {
			row := base()
			row.status = "pending_test"
			return row
		}, "授权账户正在等待后台健康检查，检查通过前不会参与调度"},
		{"本地异常", func() authorizedDispatchRow {
			row := base()
			row.status = "error"
			return row
		}, "授权账户处于异常状态，当前不可用"},
		{"本地限流带消息", func() authorizedDispatchRow {
			row := base()
			row.status = "rate_limited"
			row.lastErrorMessage = sql.NullString{String: "限流详情", Valid: true}
			return row
		}, "限流详情"},
		{"本地临时不可用", func() authorizedDispatchRow {
			row := base()
			row.status = "temporary_unavailable"
			return row
		}, "授权账户临时不可调用，恢复前不会参与调度"},
		{"本地质量隔离", func() authorizedDispatchRow {
			row := base()
			row.status = "quality_isolated"
			return row
		}, "授权账户因模型质量不达标已隔离，质量恢复检查通过前不会参与调度"},
	}
	for _, tc := range cases {
		row := tc.make()
		got := env.store.authorizedDispatchUnavailableMessage(&row, false)
		if got != tc.want {
			t.Fatalf("%s：got %q want %q", tc.name, got, tc.want)
		}
	}

	// allowLocalRecovery=true：跳过本地状态臂，健康返回空。
	healthyRow := healthy
	if got := env.store.authorizedDispatchUnavailableMessage(&healthyRow, true); got != "" {
		t.Fatalf("allowLocalRecovery 健康应返回空：%q", got)
	}
	// allowLocalRecovery=true 时本地停用不返回消息。
	localDisabled := base()
	if got := env.store.authorizedDispatchUnavailableMessage(&localDisabled, true); got != "" {
		t.Fatalf("allowLocalRecovery 应忽略本地停用：%q", got)
	}
}

func TestW10ARetryQueueClearAndDelete(t *testing.T) {
	queue := newRetryQueue("w10a", []int64{1}, 1, func(_ string, _ int) error { return nil }, retryQueueCallbacks[string]{})
	queue.clear()
	queue.delete("k")
	if pending, _ := queue.counts(); pending != 0 {
		t.Fatalf("清理后应空：%d", pending)
	}
	queue.enqueue("k", "v", retryQueueEnqueueOptions{})
	queue.delete("k")
	if pending, _ := queue.counts(); pending != 0 {
		t.Fatalf("删除后应空：%d", pending)
	}
	queue.enqueue("a", "v", retryQueueEnqueueOptions{})
	queue.clear()
	if pending, _ := queue.counts(); pending != 0 {
		t.Fatalf("清空后应空：%d", pending)
	}
}

func TestW10AHasRetainedActiveAccountAPIKeyState(t *testing.T) {
	// 空指纹 → false 臂；有效指纹 → 存在 true。
	env := newTestEnv(t)
	row, err := env.store.hasRetainedActiveAccountAPIKeyState(context.Background(), env.db, "no-such",
		Credentials{}, Credentials{})
	if err != nil || row {
		t.Fatalf("空 next 指纹应返回 false：%v %v", row, err)
	}
}
