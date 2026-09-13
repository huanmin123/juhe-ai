package proxyprofiles

// 写路径补充契约：字段归一化的类型化错误、PATCH 全字段变更的差异与密码重
// 加密、快照装载的检测字段回填，以及方言取值助手。

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestWJNormalizeTypedErrors 固定共享归一化的类型化错误（route 层统一渲染
// 为 400 代理参数无效）。
func TestWJNormalizeTypedErrors(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*proxyInput)
		want error
	}{
		{"name 必填", func(i *proxyInput) { blank := "  "; i.Name = &blank }, ErrNameRequired},
		{"description 超 200", func(i *proxyInput) {
			long := strings.Repeat("描", 201)
			i.Description, i.HasDescription = &long, true
		}, ErrDescriptionInvalid},
		{"type 非法", func(i *proxyInput) { ftp := "ftp"; i.Type = &ftp }, ErrTypeInvalid},
		{"host 空", func(i *proxyInput) { blankHost := " "; i.Host = &blankHost }, ErrHostRequired},
		{"port 越界", func(i *proxyInput) { bigPort := 70000; i.Port = &bigPort }, ErrPortInvalid},
		{"password 非字符串", func(i *proxyInput) { i.HasPassword = true }, ErrPasswordString},
		{"password 空", func(i *proxyInput) { blankPass := " "; i.HasPassword, i.Password = true, &blankPass }, ErrPasswordRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := proxyInput{}
			tc.mut(&input)
			if err := input.normalize(); !errorsIs(err, tc.want) {
				t.Fatalf("normalize = %v, 期望 %v", err, tc.want)
			}
		})
	}
	// 正常归一化：全部字段 trim。
	input := proxyInput{}
	name, host, username := " n ", " h ", " u "
	description, password := " ok ", " p "
	input.Name, input.Host, input.Username = &name, &host, &username
	input.HasUsername, input.HasPassword = true, true
	input.Password, input.Description, input.HasDescription = &password, &description, true
	scheme := "http"
	input.Type = &scheme
	port := 8080
	input.Port = &port
	if err := input.normalize(); err != nil {
		t.Fatalf("合法归一化失败: %v", err)
	}
	// 密码只做空值校验不 trim（Node zod 契约），其余字段 trim。
	if *input.Name != "n" || *input.Host != "h" || *input.Username != "u" || *input.Password != " p " {
		t.Fatalf("trim 不符: name=%q host=%q user=%q pass=%q", *input.Name, *input.Host, *input.Username, *input.Password)
	}
}

func errorsIs(err error, target error) bool {
	if err == nil || target == nil {
		return err == target
	}
	return err.Error() == target.Error()
}

// TestWJPatchAllFieldChanges 固定 PATCH 全字段变更的差异与写回。
func TestWJPatchAllFieldChanges(t *testing.T) {
	fixture := newProxyFixture(t)
	created := wjCreateProxy(t, fixture, "代理-全字段")
	updatedAt := currentUpdatedAt(t, fixture, created.ID)
	description := "新说明"
	newType, newHost, newUsername := "socks5", "newhost", "newuser"
	newPassword, newPort := "newpass", 9090
	disabled := false
	input := proxyInput{
		Description: &description, HasDescription: true,
		Type: &newType, Host: &newHost, Port: &newPort,
		Username: &newUsername, HasUsername: true,
		Password: &newPassword, HasPassword: true,
		Enabled:           &disabled,
		ExpectedUpdatedAt: updatedAt,
	}
	outcome, err := fixture.store.Patch(context.Background(), created.ID, input)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if outcome == nil || !outcome.Mutation.Changed {
		t.Fatalf("全字段 patch 必须产生变更: %+v", outcome)
	}
	if !outcome.PasswordChanged {
		t.Fatalf("密码变更必须标记: %+v", outcome)
	}
	// 差异必须覆盖六个通用字段。
	fields := map[string]bool{}
	for _, change := range diffSafeChanges(outcome.Before, outcome.After) {
		fields[change.Field] = true
	}
	for _, field := range []string{"description", "type", "host", "port", "username", "enabled"} {
		if !fields[field] {
			t.Fatalf("缺少 %s 差异: %#v", field, outcome.Before)
		}
	}
	// 密码已重加密：以相同密码再做 PATCH 不得再触发变更（证明可解回）。
	secondUpdatedAt := currentUpdatedAt(t, fixture, created.ID)
	sameAgain := "newpass"
	outcome, err = fixture.store.Patch(context.Background(), created.ID, proxyInput{
		Password: &sameAgain, HasPassword: true, ExpectedUpdatedAt: secondUpdatedAt,
	})
	if err != nil {
		t.Fatalf("patch again: %v", err)
	}
	if outcome == nil || outcome.PasswordChanged {
		t.Fatalf("重加密后的相同密码不得再变更: %+v", outcome)
	}
}

// TestWJPatchSamePasswordKeepsEnvelope 固定相同密码不重加密的契约。
func TestWJPatchSamePasswordKeepsEnvelope(t *testing.T) {
	fixture := newProxyFixture(t)
	created := wjCreateProxy(t, fixture, "代理-同密码")
	updatedAt := currentUpdatedAt(t, fixture, created.ID)
	samePassword := "pw"
	input := proxyInput{Password: &samePassword, HasPassword: true, ExpectedUpdatedAt: updatedAt}
	outcome, err := fixture.store.Patch(context.Background(), created.ID, input)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if outcome == nil || outcome.PasswordChanged {
		t.Fatalf("相同密码不得标记变更: %+v", outcome)
	}
}

// TestWJPatchConflictAndMissing 固定版本冲突与缺失行。
func TestWJPatchConflictAndMissing(t *testing.T) {
	fixture := newProxyFixture(t)
	created := wjCreateProxy(t, fixture, "代理-冲突")
	stale := "2020-01-01T00:00:00.000000Z"
	newName := "冲突改名"
	outcome, err := fixture.store.Patch(context.Background(), created.ID, proxyInput{
		Name: &newName, ExpectedUpdatedAt: stale,
	})
	if !errorsIs(err, ErrConflict) || outcome != nil {
		t.Fatalf("过期版本必须冲突: (%+v, %v)", outcome, err)
	}
	missing := "ghost"
	outcome, err = fixture.store.Patch(context.Background(), "ghost", proxyInput{
		Name: &missing, ExpectedUpdatedAt: "2026-09-04T12:00:00Z",
	})
	if outcome != nil || err != nil {
		t.Fatalf("缺失行必须 nil,nil: (%+v, %v)", outcome, err)
	}
}

// TestWJLoadProxyTestSnapshotHydratesTestState 固定快照装载回填检测字段。
func TestWJLoadProxyTestSnapshotHydratesTestState(t *testing.T) {
	fixture := newProxyTestFixture(t)
	if _, err := fixture.db.Exec(`INSERT INTO proxy_profiles
		(id, system_account_id, name, type, host, port, enabled, test_status, latency_ms, outbound_ip, outbound_region, last_test_message, last_tested_at, created_at, updated_at)
		VALUES ('p-snap', 'sa-1', '快照', 'http', 'h', 8080, 1, 'passed', 123, '1.2.3.4', 'US', 'ok', '2026-09-04T00:00:00.000Z', '2026-09-04T00:00:00Z', '2026-09-04T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	snapshot, err := fixture.store.LoadProxyTestSnapshot(context.Background(), "p-snap")
	if err != nil || snapshot == nil {
		t.Fatalf("LoadProxyTestSnapshot = (%+v, %v)", snapshot, err)
	}
	if snapshot.Before.Status != "passed" || snapshot.Before.LatencyMS == nil || *snapshot.Before.LatencyMS != 123 {
		t.Fatalf("快照检测字段不符: %+v", snapshot.Before)
	}
	if snapshot.Before.OutboundIP == nil || *snapshot.Before.OutboundIP != "1.2.3.4" {
		t.Fatalf("出口 IP 不符: %+v", snapshot.Before)
	}
	if snapshot.Before.Message == nil || *snapshot.Before.Message != "ok" {
		t.Fatalf("检测消息不符: %+v", snapshot.Before)
	}
}

// TestWJProxyTestURLEmptyPasswordRejected 固定空密码信封的拒绝与完整凭据。
func TestWJProxyTestURLEmptyPasswordRejected(t *testing.T) {
	fixture := newProxyFixture(t)
	sealed, err := fixture.store.encryptPassword("")
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	_, err = fixture.store.proxyTestURL(&proxyTestSnapshot{
		ProxyType: "http", ProxyHost: "h", ProxyPort: 8080,
		ProxyUsername: "u", PasswordEncrypted: sealed,
	})
	if err == nil || !strings.Contains(err.Error(), "代理密码凭据无效或为空") {
		t.Fatalf("空密码信封必须拒绝: %v", err)
	}
	sealedGood, err := fixture.store.encryptPassword("real")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	url, err := fixture.store.proxyTestURL(&proxyTestSnapshot{
		ProxyType: "https", ProxyHost: "h", ProxyPort: 8443,
		ProxyUsername: "u", PasswordEncrypted: sealedGood,
	})
	if err != nil || !strings.Contains(url, "u:real@h:8443") {
		t.Fatalf("完整凭据 URL = (%q, %v)", url, err)
	}
}

// TestWJDialectValueHelpers 固定方言取值助手的全部分支。
func TestWJDialectValueHelpers(t *testing.T) {
	if fromAnyInt(int64(5)) != 5 || fromAnyInt(float64(6)) != 6 || fromAnyInt([]byte("7")) != 7 || fromAnyInt(nil) != 0 {
		t.Fatal("fromAnyInt 不符")
	}
	if toTextValue("s") != "s" || toTextValue([]byte("b")) != "b" {
		t.Fatal("toTextValue 基础分支不符")
	}
	stamp := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	if got := toTextValue(stamp); got != "2026-09-04T00:00:00.000000Z" {
		t.Fatalf("toTextValue 时间 = %q", got)
	}
	if got := toTextValue(42); got != "" {
		t.Fatalf("toTextValue 默认 = %q", got)
	}
}
