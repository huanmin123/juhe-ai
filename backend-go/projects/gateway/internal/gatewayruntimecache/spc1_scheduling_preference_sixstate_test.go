// 调度偏好独立实体 P2——派发读取链六态解析测试（接口契约「读取分阶段契约」
// P2 形态；v2 用户裁决修订 2026-10-13 后停用回落态不存在）：purpose=chat Key
// 旁路调度解析（沿用策略 config）；普通 Key 绑定 NULL 属未迁移/数据不一致，显式错误失败；绑
// 定内置/自定义偏好经 routestrategies 既有 normalize 合成（策略调度字段不再
// 影响普通 Key 派发）；引用损坏/配置损坏显式错误上抛；fingerprint 只依赖规范
// 化参数（同参不同名相同、改名不变）。读取链只旁路 chat；studio 已退役，不再
// 旁路（B04a，纯函数断言见 TestSchedulingPreferencePurposeExemptStudioRetired）。
package gatewayruntimecache

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// spc1SeedEnabledKey 在 fixture 之上插入一条可读取的普通 Key。
func spc1SeedEnabledKey(t *testing.T, db *sql.DB, keyID, keyHash, purpose string, preferenceID sql.NullString) {
	t.Helper()
	if purpose == "" {
		purpose = "general"
	}
	if preferenceID.Valid {
		_, err := db.Exec(`INSERT INTO juhe_business.api_keys (id, system_account_id, route_strategy_id, key_hash, status, purpose, scheduling_preference_id)
			VALUES (?, 'sys_owner', 'rs1', ?, 'active', ?, ?)`, keyID, keyHash, purpose, preferenceID.String)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	_, err := db.Exec(`INSERT INTO juhe_business.api_keys (id, system_account_id, route_strategy_id, key_hash, status, purpose)
		VALUES (?, 'sys_owner', 'rs1', ?, 'active', ?)`, keyID, keyHash, purpose)
	if err != nil {
		t.Fatal(err)
	}
}

func spc1SeedPreference(t *testing.T, db *sql.DB, id, ownerID, chatMode, configJSON string, builtIn int) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(`INSERT INTO juhe_business.scheduling_preferences (id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, ownerID, "偏好-"+id, chatMode, configJSON, builtIn, now, now)
	if err != nil {
		t.Fatal(err)
	}
}

func spc1ReadRow(t *testing.T, models *SQLReadModels, rawKey string) *GatewayAPIKeyRow {
	t.Helper()
	runtime, err := models.ReadGatewayRuntime(context.Background(), rawKey)
	if err != nil {
		t.Fatalf("ReadGatewayRuntime: %v", err)
	}
	return runtime.APIKey
}

// spc1SpeedFirstConfigJSON 是合法范围内的速度配置（触发缺省回填路径的仅有
// slowTriggerCount 显式值）。
const spc1SpeedFirstConfigJSON = `{"slowTriggerCount":4}`

func TestSPC2HistoricalNullFailsAndChatKeysKeepStrategyConfig(t *testing.T) {
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	seedGatewayKeyFixture(t, db)
	// 策略带 speed_first 配置（chat 键的现状来源；普通键不再允许回落该来源）。
	if _, err := db.Exec(`UPDATE juhe_business.route_strategies SET config_json = '{"normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":20000,"speedFirstConfig":{"slowTriggerCount":3}}}' WHERE id = 'rs1'`); err != nil {
		t.Fatal(err)
	}
	// 内置行已由 seedGatewayKeyFixture 提供；自定义偏好存在但普通 NULL 键也必须显式失败。
	spc1SeedPreference(t, db, "sp-enabled", "sys_owner", "cost_first", "", 0)
	spc1SeedEnabledKey(t, db, "key-hist", HashSecret("sk-hist"), "", sql.NullString{})
	spc1SeedEnabledKey(t, db, "key-chat", HashSecret("sk-chat"), "chat", sql.NullString{String: apikeys.BuiltinSchedulingPreferenceID, Valid: true})
	models, err := NewSQLReadModels(db, nil, seamAccountsSelector{results: map[string]OpenAIAccountsForGroupResult{}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// P2（D4「P2，迁移完成」行）：普通 Key 绑定 NULL = 未迁移/数据不一致，
	// 读快照显式失败；即使策略带 speed_first 配置也不得回落策略或内置。
	if _, err := models.ReadGatewayRuntime(context.Background(), "sk-hist"); err == nil {
		t.Fatalf("historical NULL general key must fail explicitly after migration")
	} else if !strings.Contains(err.Error(), "调度偏好绑定缺失") || !strings.Contains(err.Error(), "未迁移或数据不一致") {
		t.Fatalf("error %q must point at the unmigrated/inconsistent semantics", err.Error())
	}
	chat := spc1ReadRow(t, models, "sk-chat")
	if chat.SchedulingPreferenceID != "" || chat.SchedulingPreferenceFingerprint != "" || chat.SchedulingPreferenceChatMode != "" {
		t.Fatalf("chat key must not consume preference config: %+v", chat)
	}
	if chat.NormalRoutingConfig == nil || chat.NormalRoutingConfig.SchedulingPreference != "speed_first" {
		t.Fatalf("chat key must keep strategy config: %+v", chat.NormalRoutingConfig)
	}
}

// TestSchedulingPreferencePurposeExemptStudioRetired 锁定 B04a 读取链旁路收
// 窄：只旁路 purpose='chat'；studio 已退役，不再旁路（存量行由 D06 清理）。
func TestSchedulingPreferencePurposeExemptStudioRetired(t *testing.T) {
	if schedulingPreferencePurposeExempt(sql.NullString{String: "studio", Valid: true}) {
		t.Fatalf("studio purpose must no longer bypass scheduling preference resolution")
	}
	if !schedulingPreferencePurposeExempt(sql.NullString{String: "chat", Valid: true}) {
		t.Fatalf("chat purpose must keep bypassing scheduling preference resolution")
	}
	if schedulingPreferencePurposeExempt(sql.NullString{}) {
		t.Fatalf("NULL purpose must not bypass scheduling preference resolution")
	}
}

func TestSPC1BoundEnabledPreferenceSynthesizesConfig(t *testing.T) {
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	seedGatewayKeyFixture(t, db)
	spc1SeedPreference(t, db, "sp-speed", "sys_owner", "speed_first", spc1SpeedFirstConfigJSON, 0)
	spc1SeedEnabledKey(t, db, "key-speed", HashSecret("sk-speed"), "", sql.NullString{String: "sp-speed", Valid: true})
	models, err := NewSQLReadModels(db, nil, seamAccountsSelector{results: map[string]OpenAIAccountsForGroupResult{}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	row := spc1ReadRow(t, models, "sk-speed")
	if row.SchedulingPreferenceID != "sp-speed" || row.SchedulingPreferenceChatMode != "speed_first" {
		t.Fatalf("snapshot fields: %+v", row)
	}
	if row.SchedulingPreferenceFingerprint == "" || len(row.SchedulingPreferenceFingerprint) != 12 {
		t.Fatalf("fingerprint: %q", row.SchedulingPreferenceFingerprint)
	}
	config := row.NormalRoutingConfig
	if config == nil || config.SchedulingPreference != "speed_first" {
		t.Fatalf("synthesized config: %+v", config)
	}
	// 合成对象携带规范化完整 speedFirstConfig（缺省回填 + 显式值）与首字截止。
	var decoded map[string]any
	if err := json.Unmarshal(config.Raw, &decoded); err != nil {
		t.Fatalf("raw decode: %v", err)
	}
	if decoded["firstByteDeadlineMs"].(float64) != 30000 {
		t.Fatalf("firstByteDeadlineMs default: %v", decoded["firstByteDeadlineMs"])
	}
	speedFirst, ok := decoded["speedFirstConfig"].(map[string]any)
	if !ok || speedFirst["slowTriggerCount"].(float64) != 4 || speedFirst["totalTimeDeadlineSeconds"].(float64) != 120 {
		t.Fatalf("speedFirstConfig: %v", speedFirst)
	}
	// fingerprint 与规范化参数集一致（sha256 前 12 位）：同一配置经
	// routestrategies normalize 的期望值独立重算。
	var speedConfig any
	if err := json.Unmarshal([]byte(spc1SpeedFirstConfigJSON), &speedConfig); err != nil {
		t.Fatal(err)
	}
	normalized, err := routestrategies.NormalizeNormalRoutingConfig(map[string]any{
		"schedulingPreference": "speed_first",
		"speedFirstConfig":     speedConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	if row.SchedulingPreferenceFingerprint != hex.EncodeToString(digest[:])[:12] {
		t.Fatalf("fingerprint mismatch: %q", row.SchedulingPreferenceFingerprint)
	}
}

func TestSPC1BoundCostFirstCollapsesAndFingerprintIgnoresName(t *testing.T) {
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	seedGatewayKeyFixture(t, db)
	// 策略带 speed_first 配置：绑定 cost_first 偏好的普通 Key 派发配置必须来
	// 自偏好合成（裸 cost_first），策略调度字段不再影响派发（P2 读取收敛）。
	if _, err := db.Exec(`UPDATE juhe_business.route_strategies SET config_json = '{"normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":20000,"speedFirstConfig":{"slowTriggerCount":3}}}' WHERE id = 'rs1'`); err != nil {
		t.Fatal(err)
	}
	spc1SeedPreference(t, db, "sp-cost-a", "sys_owner", "cost_first", "", 0)
	spc1SeedPreference(t, db, "sp-cost-b", "other_owner", "cost_first", "", 0)
	spc1SeedEnabledKey(t, db, "key-cost", HashSecret("sk-cost"), "", sql.NullString{String: "sp-cost-a", Valid: true})
	models, err := NewSQLReadModels(db, nil, seamAccountsSelector{results: map[string]OpenAIAccountsForGroupResult{}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	row := spc1ReadRow(t, models, "sk-cost")
	if row.SchedulingPreferenceChatMode != "cost_first" || row.SchedulingPreferenceID != "sp-cost-a" {
		t.Fatalf("cost snapshot: %+v", row)
	}
	if row.NormalRoutingConfig == nil || row.NormalRoutingConfig.SchedulingPreference != "cost_first" || row.NormalRoutingConfig.Raw != nil {
		// 策略 speed_first 配置不得穿透：派发配置一律来自偏好合成覆盖。
		t.Fatalf("strategy scheduling fields must not affect dispatch; cost_first must collapse to the bare shape: %+v", row.NormalRoutingConfig)
	}
	// 同参数不同名/不同 owner 偏好 → 同 fingerprint；改名不改 fingerprint。
	costSnapshot, err := synthesizeSchedulingPreferenceSnapshot("sp-cost-a", "cost_first", "")
	if err != nil {
		t.Fatal(err)
	}
	costFingerprint, err := costSnapshot.fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := synthesizeSchedulingPreferenceSnapshot("sp-cost-b", "cost_first", "")
	if err != nil {
		t.Fatal(err)
	}
	renamedFingerprint, err := renamed.fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if costFingerprint != renamedFingerprint || costFingerprint != row.SchedulingPreferenceFingerprint {
		t.Fatalf("fingerprint must ignore name/owner: %q vs %q vs %q", costFingerprint, renamedFingerprint, row.SchedulingPreferenceFingerprint)
	}
	// 参数不同 → fingerprint 不同。
	speedSnapshot, err := synthesizeSchedulingPreferenceSnapshot("sp-speed", "speed_first", spc1SpeedFirstConfigJSON)
	if err != nil {
		t.Fatal(err)
	}
	speedFingerprint, err := speedSnapshot.fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if speedFingerprint == costFingerprint {
		t.Fatal("different normalized params must yield different fingerprints")
	}
}

func TestSPC1BrokenBindingOrConfigFailsExplicitly(t *testing.T) {
	cases := []struct {
		name          string
		preferenceID  string
		seedPrefs     []string
		seedSpeedJSON string
		wantFragment  string
	}{
		{
			name:         "missing-row",
			preferenceID: "sp-gone",
			wantFragment: "绑定引用不存在",
		},
		{
			name:         "cross-owner",
			preferenceID: "sp-foreign",
			seedPrefs:    []string{"sp-foreign|stranger|speed_first|" + spc1SpeedFirstConfigJSON + "|0"},
			wantFragment: "绑定引用不存在",
		},
		{
			name:          "broken-json",
			preferenceID:  "sp-broken",
			seedPrefs:     []string{"sp-broken|sys_owner|speed_first|{not-json|0"},
			seedSpeedJSON: "{not-json",
			wantFragment:  "JSON 损坏",
		},
		{
			name:          "illegal-chat-mode",
			preferenceID:  "sp-illegal",
			seedPrefs:     []string{"sp-illegal|sys_owner|hybrid|" + spc1SpeedFirstConfigJSON + "|0"},
			seedSpeedJSON: spc1SpeedFirstConfigJSON,
			wantFragment:  "调度偏好无效",
		},
		{
			name:          "out-of-range-param",
			preferenceID:  "sp-range",
			seedPrefs:     []string{"sp-range|sys_owner|speed_first|{\"slowTriggerCount\":99}|0"},
			seedSpeedJSON: `{"slowTriggerCount":99}`,
			wantFragment:  "速度优先触发次数",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := newSQLTestDB(t)
			seedGatewaySettingsKeys(t, db)
			seedGatewayKeyFixture(t, db)
			for _, spec := range testCase.seedPrefs {
				parts := strings.Split(spec, "|")
				spc1SeedPreference(t, db, parts[0], parts[1], parts[2], parts[3], 0)
			}
			spc1SeedEnabledKey(t, db, "key-broken", HashSecret("sk-broken"), "", sql.NullString{String: testCase.preferenceID, Valid: true})
			models, err := NewSQLReadModels(db, nil, seamAccountsSelector{results: map[string]OpenAIAccountsForGroupResult{}}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = models.ReadGatewayRuntime(context.Background(), "sk-broken")
			if err == nil {
				t.Fatalf("broken binding must fail explicitly")
			}
			if !strings.Contains(err.Error(), testCase.wantFragment) {
				t.Fatalf("error %q must contain %q", err.Error(), testCase.wantFragment)
			}
		})
	}
}
