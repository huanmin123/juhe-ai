package accounts

// T100 批量编辑 supportedEndpointModes 连接行同步回归（真 PG，pgtestkit）：
//   - 单连接账户：account_endpoint_modes 与凭据域副本同事务更新，提交后失效
//     端口（lookup + runtime）被调；
//   - 多连接 / 无连接行账户：连接行跳过、凭据域副本照写、逐账户 notice 报告
//     （不做猜默认的隐式行为）；
//   - 仅凭据副本同值而连接行漂移（T100 缺陷遗留状态）的再运行：连接行差异
//     仍按 supportedEndpointModes 变更披露并同步（漂移自愈）；
//   - 不改能力字段：连接行不被触碰；
//   - 检查形态解析口径：多连接账户以选定连接（健康检查连接优先）现行 modes
//     为准——连接行未声明的显式检查形态响亮拒绝，不再按新凭据集静默放行。

import (
	"sort"
	"testing"
)

// t100ConnectionModes 读一个账户全部连接行的 endpoint modes（preset → 有序
// 集合），作为连接行事实源的测试投影。
func t100ConnectionModes(t *testing.T, env *testEnv, accountID string) map[string][]string {
	t.Helper()
	rows, err := env.db.Query(`SELECT c.connection_preset_code, m.endpoint_mode
		FROM account_connections c
		LEFT JOIN account_endpoint_modes m ON m.connection_id = c.id
		WHERE c.account_id = $1
		ORDER BY c.connection_preset_code ASC, m.endpoint_mode ASC`, accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	modes := map[string][]string{}
	for rows.Next() {
		var preset, mode string
		if err := rows.Scan(&preset, &mode); err != nil {
			t.Fatal(err)
		}
		modes[preset] = append(modes[preset], mode)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return modes
}

// t100ConnectionFacts 读连接行身份快照（id/preset/base_url/enabled），供
// 「不被触碰」断言比较；modes 写面合法更新 updated_at，不纳入快照。
func t100ConnectionFacts(t *testing.T, env *testEnv, accountID string) string {
	t.Helper()
	rows, err := env.db.Query(`SELECT id || '|' || connection_preset_code || '|' || base_url || '|' || enabled
		FROM account_connections WHERE account_id = $1 ORDER BY id ASC`, accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := ""
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		out += line + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// t100CredentialModes 解密凭据域副本的 supported_endpoint_modes（JSON 反解为
// []any，与存储形状一致）。
func t100CredentialModes(t *testing.T, env *testEnv, accountID string) []any {
	t.Helper()
	var sealed string
	if err := env.db.QueryRow(`SELECT credentials_encrypted FROM accounts WHERE id = $1`, accountID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	var credentials Credentials
	if err := DecryptJSON(testSecret, sealed, &credentials); err != nil {
		t.Fatalf("凭据解密失败：%v", err)
	}
	raw, ok := credentials["supported_endpoint_modes"].([]any)
	if !ok {
		raw = []any{}
	}
	return raw
}

func t100ModeSet(modes []string) []string {
	out := append([]string{}, modes...)
	sort.Strings(out)
	return out
}

func t100AnyToStrings(values []any) []string {
	out := []string{}
	for _, item := range values {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	sort.Strings(out)
	return out
}

func t100AssertModes(t *testing.T, got []string, want ...string) {
	t.Helper()
	expected := t100ModeSet(want)
	actual := t100ModeSet(got)
	if len(actual) != len(expected) {
		t.Fatalf("modes 集合不一致：got %v want %v", actual, expected)
	}
	for index := range expected {
		if actual[index] != expected[index] {
			t.Fatalf("modes 集合不一致：got %v want %v", actual, expected)
		}
	}
}

// t100SeedExtraConnection 给账户直插第二条连接行（多连接夹具）：registry 词表
// 内 preset，连接行 schema 与写入面同形。
func t100SeedExtraConnection(t *testing.T, env *testEnv, accountID string) {
	t.Helper()
	env.exec(t, `INSERT INTO account_connections (id, account_id, connection_preset_code, protocol_surface_code, base_url, enabled, created_at, updated_at)
		VALUES ('conn-t100-extra', ?, 'xai_openai_v1', 'openai_v1', 'https://relay.example.com/v1', 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, accountID)
	env.exec(t, `INSERT INTO account_endpoint_modes (connection_id, endpoint_mode)
		SELECT 'conn-t100-extra', m.mode FROM (VALUES ('chat_json'), ('chat_sse')) AS m(mode)`)
}

// TestT100BatchEndpointModesSyncSingleConnection 钉单连接账户三件事：连接行
// modes 更新、凭据副本更新、失效端口被调；连接行 id 与 base_url/enabled 不变。
func TestT100BatchEndpointModesSyncSingleConnection(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	first, second, rev1, rev2 := w2BatchAccounts(t, env, adminID)
	fake := &fakeBatchInvalidator{}
	env.store.SetCacheInvalidator(fake)

	connectionBefore := map[string]string{
		first:  t100ConnectionFacts(t, env, first),
		second: t100ConnectionFacts(t, env, second),
	}
	input := BatchUpdateInput{
		Targets: []BatchUpdateTarget{{AccountID: first, ConfigRevision: rev1}, {AccountID: second, ConfigRevision: rev2}},
		Updates: w2EnabledFields(map[string]any{
			"supportedEndpointModes": []any{"chat_json", "chat_sse", "responses_json"},
		}),
	}
	result, err := w2BatchUpdate(t, env, adminID, input)
	if err != nil {
		t.Fatalf("BatchUpdate 错误：%v", err)
	}
	for _, item := range result.Items {
		if item.Notice != "" {
			t.Fatalf("单连接账户不应携带跳过报告：%s %q", item.ID, item.Notice)
		}
		if !containsChangeField(item.ChangedFields, "supportedEndpointModes") {
			t.Fatalf("变更字段缺少 supportedEndpointModes：%v", item.ChangedFields)
		}
		if item.ConfigRevision != rev1+1 && item.ConfigRevision != rev2+1 {
			t.Fatalf("修订号未递增：%+v", result.Items)
		}
	}
	for _, accountID := range []string{first, second} {
		connectionModes := t100ConnectionModes(t, env, accountID)
		if len(connectionModes) != 1 {
			t.Fatalf("单连接账户连接行数不一致：%s %v", accountID, connectionModes)
		}
		for preset, modes := range connectionModes {
			if preset != "gpt_openai_v1" {
				t.Fatalf("连接 preset 不一致：%s", preset)
			}
			t100AssertModes(t, modes, "chat_json", "chat_sse", "responses_json")
		}
		t100AssertModes(t, t100AnyToStrings(t100CredentialModes(t, env, accountID)),
			"chat_json", "chat_sse", "responses_json")
		if after := t100ConnectionFacts(t, env, accountID); after != connectionBefore[accountID] {
			t.Fatalf("连接行身份/Base URL/开关不应改变：%s\nbefore=%s after=%s", accountID, connectionBefore[accountID], after)
		}
	}
	lookups, reasons := fake.snapshot()
	if len(lookups) != 2 || !(lookups[0] == first && lookups[1] == second || lookups[0] == second && lookups[1] == first) {
		t.Fatalf("lookup 失效端口未被逐账户调用：%v", lookups)
	}
	if len(reasons) != 1 || reasons[0] != "account_batch_updated" {
		t.Fatalf("runtime 失效端口未被调用：%v", reasons)
	}
}

// TestT100BatchEndpointModesHealsConnectionDrift 钉漂移自愈臂：凭据副本已同
// 值而连接行仍是旧集合（T100 缺陷遗留状态）时，同值再运行仍按
// supportedEndpointModes 变更披露并同步连接行（CAS + 失效链照走）。
func TestT100BatchEndpointModesHealsConnectionDrift(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	first, second, rev1, rev2 := w2BatchAccounts(t, env, adminID)

	modes := []any{"chat_json", "chat_sse", "responses_json"}
	input := BatchUpdateInput{
		Targets: []BatchUpdateTarget{{AccountID: first, ConfigRevision: rev1}, {AccountID: second, ConfigRevision: rev2}},
		Updates: w2EnabledFields(map[string]any{"supportedEndpointModes": modes}),
	}
	if _, err := w2BatchUpdate(t, env, adminID, input); err != nil {
		t.Fatalf("首轮批量失败：%v", err)
	}
	// 复刻生产漂移：把首账户连接行拨回旧集合（凭据副本保持新集合）。
	env.exec(t, `DELETE FROM account_endpoint_modes WHERE connection_id IN
		(SELECT id FROM account_connections WHERE account_id = ?)`, first)
	env.exec(t, `INSERT INTO account_endpoint_modes (connection_id, endpoint_mode)
		SELECT id, m.mode FROM account_connections, (VALUES ('chat_json'), ('chat_sse')) AS m(mode)
		WHERE account_connections.account_id = ?`, first)

	var revision int64
	if err := env.db.QueryRow(`SELECT config_revision FROM accounts WHERE id = $1`, first).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	retry := BatchUpdateInput{
		Targets: []BatchUpdateTarget{{AccountID: first, ConfigRevision: revision}, {AccountID: second, ConfigRevision: rev2 + 1}},
		Updates: w2EnabledFields(map[string]any{"supportedEndpointModes": modes}),
	}
	result, err := w2BatchUpdate(t, env, adminID, retry)
	if err != nil {
		t.Fatalf("漂移再运行失败：%v", err)
	}
	for _, item := range result.Items {
		if item.ID == first && !containsChangeField(item.ChangedFields, "supportedEndpointModes") {
			t.Fatalf("漂移自愈臂未披露变更：%+v", item)
		}
	}
	for preset, rowModes := range t100ConnectionModes(t, env, first) {
		_ = preset
		t100AssertModes(t, rowModes, "chat_json", "chat_sse", "responses_json")
	}
}

// TestT100BatchEndpointModesSkipsMultiAndNoConnection 钉两档跳过：多连接与无
// 连接行账户连接行不动、凭据副本照写、逐账户 notice 报告。
func TestT100BatchEndpointModesSkipsMultiAndNoConnection(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	first, second, rev1, rev2 := w2BatchAccounts(t, env, adminID)
	// first → 多连接（直插第二条连接行）；second → 无连接行（先解健康检查
	// 引用再删连接行，RESTRICT 语义）。
	t100SeedExtraConnection(t, env, first)
	env.exec(t, `UPDATE accounts SET health_check_connection_id = NULL WHERE id = ?`, second)
	env.exec(t, `DELETE FROM account_endpoint_modes WHERE connection_id IN
		(SELECT id FROM account_connections WHERE account_id = ?)`, second)
	env.exec(t, `DELETE FROM account_connections WHERE account_id = ?`, second)

	modesBefore := map[string]map[string][]string{
		first: t100ConnectionModes(t, env, first),
	}
	factsBefore := t100ConnectionFacts(t, env, first)

	input := BatchUpdateInput{
		Targets: []BatchUpdateTarget{{AccountID: first, ConfigRevision: rev1}, {AccountID: second, ConfigRevision: rev2}},
		Updates: w2EnabledFields(map[string]any{
			"supportedEndpointModes": []any{"chat_json", "chat_sse", "responses_json"},
		}),
	}
	result, err := w2BatchUpdate(t, env, adminID, input)
	if err != nil {
		t.Fatalf("BatchUpdate 错误：%v", err)
	}
	notices := map[string]string{}
	for _, item := range result.Items {
		notices[item.ID] = item.Notice
		if !containsChangeField(item.ChangedFields, "supportedEndpointModes") {
			t.Fatalf("凭据域副本变化应披露变更字段：%+v", item)
		}
	}
	if notices[first] != "多连接，未改能力，请单独编辑" {
		t.Fatalf("多连接账户报告不一致：%q", notices[first])
	}
	if notices[second] != "无连接行，未改能力，请单独编辑" {
		t.Fatalf("无连接行账户报告不一致：%q", notices[second])
	}
	// 连接行事实源三档中跳过的两档保持原样。
	if after := t100ConnectionModes(t, env, first); !t100ModesMapsEqual(modesBefore[first], after) {
		t.Fatalf("多连接账户连接行被改动：before=%v after=%v", modesBefore[first], after)
	}
	if after := t100ConnectionFacts(t, env, first); after != factsBefore {
		t.Fatalf("多连接账户连接行快照被改动：before=%s after=%s", factsBefore, after)
	}
	if modes := t100ConnectionModes(t, env, second); len(modes) != 0 {
		t.Fatalf("无连接行账户应无连接行：%v", modes)
	}
	// 凭据域副本照写（读面回落兜底）。
	for _, accountID := range []string{first, second} {
		t100AssertModes(t, t100AnyToStrings(t100CredentialModes(t, env, accountID)),
			"chat_json", "chat_sse", "responses_json")
	}
}

// TestT100BatchHealthModeResolvesAgainstConnectionModes 钉检查形态解析口径：
// 多连接账户以选定连接现行 modes 为准——新凭据集包含、连接行未声明的显式检
// 查形态响亮拒绝（整批回滚）；连接行声明的形态照常通过且单连接账户照常同步。
func TestT100BatchHealthModeResolvesAgainstConnectionModes(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	first, second, rev1, rev2 := w2BatchAccounts(t, env, adminID)
	t100SeedExtraConnection(t, env, first)

	// first 的健康检查连接是原 gpt_openai_v1 行（modes chat_json/chat_sse）；
	// responses_json 只在新凭据集里。旧口径按新凭据集放行会落连接行未声明的
	// 检查形态，新口径按选定连接现行 modes 拒绝。
	rejected := BatchUpdateInput{
		Targets: []BatchUpdateTarget{{AccountID: first, ConfigRevision: rev1}, {AccountID: second, ConfigRevision: rev2}},
		Updates: w2EnabledFields(map[string]any{
			"supportedEndpointModes":  []any{"chat_json", "chat_sse", "responses_json"},
			"healthCheckEndpointMode": "responses_json",
		}),
	}
	_, err := w2BatchUpdate(t, env, adminID, rejected)
	if err == nil {
		t.Fatal("连接行未声明的检查形态应被拒绝")
	}
	if want := "账户健康检查请求形态 responses_json 未启用"; err.Error() != want {
		t.Fatalf("拒绝文案不一致：got %q want %q", err.Error(), want)
	}
	// 整批回滚：修订与连接行均保持原状。
	for _, pair := range []struct {
		id       string
		revision int64
	}{{first, rev1}, {second, rev2}} {
		var revision int64
		if err := env.db.QueryRow(`SELECT config_revision FROM accounts WHERE id = $1`, pair.id).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if revision != pair.revision {
			t.Fatalf("中止批量不应递增修订号：%s %d", pair.id, revision)
		}
	}

	// 连接行已声明 chat_json：显式检查形态通过；单连接账户连接行照常同步到
	// 新集合（以更新后的连接行为准），多连接账户连接行保持不动。
	accepted := BatchUpdateInput{
		Targets: []BatchUpdateTarget{{AccountID: first, ConfigRevision: rev1}, {AccountID: second, ConfigRevision: rev2}},
		Updates: w2EnabledFields(map[string]any{
			"supportedEndpointModes":  []any{"chat_json", "chat_sse", "responses_json"},
			"healthCheckEndpointMode": "chat_json",
		}),
	}
	if _, err := w2BatchUpdate(t, env, adminID, accepted); err != nil {
		t.Fatalf("连接行已声明形态的批量不应失败：%v", err)
	}
	for _, rowModes := range t100ConnectionModes(t, env, first) {
		t100AssertModes(t, rowModes, "chat_json", "chat_sse")
	}
	for _, rowModes := range t100ConnectionModes(t, env, second) {
		t100AssertModes(t, rowModes, "chat_json", "chat_sse", "responses_json")
	}
	var healthMode string
	if err := env.db.QueryRow(`SELECT health_check_endpoint_mode FROM accounts WHERE id = $1`, first).Scan(&healthMode); err != nil {
		t.Fatal(err)
	}
	if healthMode != "chat_json" {
		t.Fatalf("检查形态落库不一致：%q", healthMode)
	}
}

// TestT100BatchWithoutModesFieldKeepsConnectionRowsUntouched 钉不改能力字段
// 的批量不触碰连接行（notes-only 路径无连接读/写面）。
func TestT100BatchWithoutModesFieldKeepsConnectionRowsUntouched(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	first, second, rev1, rev2 := w2BatchAccounts(t, env, adminID)
	t100SeedExtraConnection(t, env, first)

	factsBefore := map[string]string{
		first:  t100ConnectionFacts(t, env, first),
		second: t100ConnectionFacts(t, env, second),
	}
	modesBefore := map[string]map[string][]string{
		first:  t100ConnectionModes(t, env, first),
		second: t100ConnectionModes(t, env, second),
	}
	input := BatchUpdateInput{
		Targets: []BatchUpdateTarget{{AccountID: first, ConfigRevision: rev1}, {AccountID: second, ConfigRevision: rev2}},
		Updates: w2EnabledFields(map[string]any{"notes": "不改能力字段"}),
	}
	if _, err := w2BatchUpdate(t, env, adminID, input); err != nil {
		t.Fatalf("notes 批量失败：%v", err)
	}
	for _, accountID := range []string{first, second} {
		if after := t100ConnectionFacts(t, env, accountID); after != factsBefore[accountID] {
			t.Fatalf("notes 批量不应触碰连接行：%s\nbefore=%s after=%s", accountID, factsBefore[accountID], after)
		}
		if after := t100ConnectionModes(t, env, accountID); !t100ModesMapsEqual(modesBefore[accountID], after) {
			t.Fatalf("notes 批量不应触碰 endpoint modes：%s", accountID)
		}
	}
}

func t100ModesMapsEqual(left, right map[string][]string) bool {
	if len(left) != len(right) {
		return false
	}
	for preset, modes := range left {
		other, ok := right[preset]
		if !ok || !unorderedStringListEqual(modes, other) {
			return false
		}
	}
	return true
}

func containsChangeField(fields []string, want string) bool {
	for _, field := range fields {
		if field == want {
			return true
		}
	}
	return false
}
