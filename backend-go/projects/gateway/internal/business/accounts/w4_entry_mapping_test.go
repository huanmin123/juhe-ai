package accounts

// w4_entry_mapping_test.go —— 入口映射写路径（B4 第一批，设计 §5.1/§5.3/§8）
// 的真实 SQLite 用例：connection preset 解析、enabled 门、§5.2/§5.3 family
// 联动校验（文本至少一个 source shape、视频四 mode 全启用、TTS audio_speech）、
// patch 整集合替换语义与 DTO 往返。

import (
	"context"
	"errors"
	"testing"
)

// w4Exec 是 testDB 之上的小种子帮手，失败即终止用例。
func w4Exec(t *testing.T, s *Service, query string, args ...any) {
	t.Helper()
	if _, err := s.store.db.Exec(query, args...); err != nil {
		t.Fatalf("seed: %v: %s", err, query)
	}
}

// w4Mapping 构造一条 chat_completions→chat_completions 映射（夹具连接与
// m-gpt5 的 modes 交集可执行）。
func w4Mapping(source string) ModelMapping {
	return ModelMapping{SourceModel: source, SourceEndpointFamily: "chat_completions", UpstreamModel: "gpt-5", UpstreamEndpointFamily: "chat_completions", TargetConnectionPresetCode: "generic_openai_v1", Enabled: true}
}

// TestW4CreateWritesEntryMappingsOverTargetDDL：create 的映射行落
// account_entry_mappings，target 双解析为连接 ID 与 models.id，DTO 往返带出
// preset 自然键；旧 account_model_mappings 不再写。
func TestW4CreateWritesEntryMappingsOverTargetDDL(t *testing.T) {
	s := ready(t)
	ctx := context.Background()
	a, err := s.Create(ctx, input())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.ModelMappings) != 1 {
		t.Fatalf("mappings=%+v", a.ModelMappings)
	}
	got := a.ModelMappings[0]
	want := ModelMapping{SourceModel: "gpt-5", SourceEndpointFamily: "chat_completions", UpstreamModel: "gpt-5", UpstreamEndpointFamily: "chat_completions", TargetConnectionPresetCode: "generic_openai_v1", Enabled: true}
	if got != want {
		t.Fatalf("roundtrip mapping=%+v want %+v", got, want)
	}
	var connectionID, modelID, targetFamily string
	var enabled int
	if err := s.store.db.QueryRow(`SELECT target_connection_id, target_model_id, target_endpoint_family, enabled FROM account_entry_mappings WHERE account_id='a1' AND source_model='gpt-5'`).Scan(&connectionID, &modelID, &targetFamily, &enabled); err != nil {
		t.Fatalf("entry mapping row: %v", err)
	}
	if connectionID != "conn-a1-generic" || modelID != "m-gpt5" || targetFamily != "chat_completions" || enabled != 1 {
		t.Fatalf("entry mapping row=(%s,%s,%s,%d)", connectionID, modelID, targetFamily, enabled)
	}
	// 幂等重试（同负载）仍成功：读回聚合与入参按 mappingKey 全序等价。
	if _, err := s.Create(ctx, input()); err != nil {
		t.Fatalf("idempotent retry err=%v", err)
	}
}

// TestW4MappingPresetMustResolveToEnabledConnection：preset 缺省/未知/停用均按
// ErrMappingTargetUnsupported 显式拒绝（§8：连接 ID 永不由客户端提交；缺省
// 不静默跳过、不回落凭据 JSON 旧字段）并整体回滚。
func TestW4MappingPresetMustResolveToEnabledConnection(t *testing.T) {
	cases := map[string]func(*ModelMapping){
		"缺省 preset": func(m *ModelMapping) { m.TargetConnectionPresetCode = "" },
		"未知 preset": func(m *ModelMapping) { m.TargetConnectionPresetCode = "no_such_preset" },
		"停用 preset": func(m *ModelMapping) { m.TargetConnectionPresetCode = "generic_openai_v1" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := ready(t)
			if name == "停用 preset" {
				w4Exec(t, s, `UPDATE account_connections SET enabled=0 WHERE id='conn-a1-generic'`)
			}
			in := input()
			mutate(&in.ModelMappings[0])
			if _, err := s.Create(context.Background(), in); !errors.Is(err, ErrMappingTargetUnsupported) {
				t.Fatalf("err=%v want ErrMappingTargetUnsupported", err)
			}
			if _, err := s.Get(context.Background(), "sys", "a1"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("failed create must rollback, err=%v", err)
			}
		})
	}
}

// TestW4MappingTargetModelMustResolve：target 模型名解析不到（不可见/停用/
// 不存在）折叠为 ErrMappingTargetUnsupported 并保留 ErrModelNotFound 链。
func TestW4MappingTargetModelMustResolve(t *testing.T) {
	s := ready(t)
	in := input()
	in.ModelMappings[0].UpstreamModel = "nope"
	err := func() error {
		_, err := s.Create(context.Background(), in)
		return err
	}()
	if !errors.Is(err, ErrMappingTargetUnsupported) || !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("err=%v want both sentinels", err)
	}
}

// TestW4TextMappingRequiresExecutableSourceShape：文本映射要求至少一个
// source shape 落在连接 mode ∩ 模型 mode 交集（§5.3）。responses→chat 只认
// chat_sse 形态：连接只有 chat_json 时拒绝，补上 chat_sse 后同负载可写。
func TestW4TextMappingRequiresExecutableSourceShape(t *testing.T) {
	s := ready(t)
	ctx := context.Background()
	// 连接 modes 收窄为 chat_json：responses→chat_completions 的唯一形态
	// chat_sse 不可执行 → 拒绝。
	w4Exec(t, s, `DELETE FROM account_endpoint_modes WHERE connection_id='conn-a1-generic'`)
	w4Exec(t, s, `INSERT INTO account_endpoint_modes (connection_id,endpoint_mode) VALUES ('conn-a1-generic','chat_json')`)
	in := input()
	in.ModelMappings[0] = ModelMapping{SourceModel: "client-res", SourceEndpointFamily: "responses", UpstreamModel: "gpt-5", UpstreamEndpointFamily: "chat_completions", TargetConnectionPresetCode: "generic_openai_v1", Enabled: true}
	if _, err := s.Create(ctx, in); !errors.Is(err, ErrMappingTargetUnsupported) {
		t.Fatalf("chat_sse missing err=%v", err)
	}
	// 模型 modes 收窄为 chat_json 同样拒绝（交集双侧参与）。
	w4Exec(t, s, `INSERT INTO account_endpoint_modes (connection_id,endpoint_mode) VALUES ('conn-a1-generic','chat_sse')`)
	w4Exec(t, s, `UPDATE models SET supported_endpoint_modes_json='["chat_json"]' WHERE id='m-gpt5'`)
	if _, err := s.Create(ctx, in); !errors.Is(err, ErrMappingTargetUnsupported) {
		t.Fatalf("model chat_sse missing err=%v", err)
	}
	// 模型补回 chat_sse：至少一个可执行 shape → 写入成功。
	w4Exec(t, s, `UPDATE models SET supported_endpoint_modes_json='["chat_json","chat_sse"]' WHERE id='m-gpt5'`)
	a, err := s.Create(ctx, in)
	if err != nil {
		t.Fatalf("executable shape must pass: %v", err)
	}
	if len(a.ModelMappings) != 1 || a.ModelMappings[0].SourceModel != "client-res" {
		t.Fatalf("mappings=%+v", a.ModelMappings)
	}
}

// TestW4FamilyPairMatrixEnforced：矩阵外 pair（含设计点名列举的
// chat_completions→responses 与 messages→responses）一律拒绝。
func TestW4FamilyPairMatrixEnforced(t *testing.T) {
	for _, pair := range [][2]string{
		{"chat_completions", "responses"},
		{"messages", "responses"},
		{"tts", "chat_completions"},
		{"generate_content", "tts"},
	} {
		s := ready(t)
		in := input()
		in.ModelMappings[0] = ModelMapping{SourceModel: "m", SourceEndpointFamily: pair[0], UpstreamModel: "gpt-5", UpstreamEndpointFamily: pair[1], TargetConnectionPresetCode: "generic_openai_v1", Enabled: true}
		if _, err := s.Create(context.Background(), in); !errors.Is(err, ErrMappingTargetUnsupported) {
			t.Fatalf("pair %v must be rejected, err=%v", pair, err)
		}
	}
}

// TestW4VideoMappingRequiresAllFourModes（§5.2 #16 / §5.3）：视频目标必须在
// 连接与模型双侧同时具备四个 video mode；只声明 video_create 拒绝。
func TestW4VideoMappingRequiresAllFourModes(t *testing.T) {
	s := ready(t)
	ctx := context.Background()
	w4Exec(t, s, `INSERT INTO account_connections (id,account_id,connection_preset_code,protocol_surface_code,base_url,enabled,created_at,updated_at)
		VALUES ('conn-a1-video','a1','generic_gemini_v1beta','gemini_v1beta','https://video.example.invalid',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	w4Exec(t, s, `INSERT INTO account_endpoint_modes (connection_id,endpoint_mode) VALUES ('conn-a1-video','video_create'),('conn-a1-video','video_get'),('conn-a1-video','video_content'),('conn-a1-video','video_cancel')`)
	w4Exec(t, s, `INSERT INTO models (id,vendor_code,public_model,upstream_model,scope,system_account_id,status,supported_endpoint_modes_json)
		VALUES ('m-veo',NULL,'veo-test','veo-test','global',NULL,'active','["video_create","video_get","video_content","video_cancel"]')`)
	in := input()
	in.ModelMappings[0] = ModelMapping{SourceModel: "client-veo", SourceEndpointFamily: "video_generation", UpstreamModel: "veo-test", UpstreamEndpointFamily: "video_generation", TargetConnectionPresetCode: "generic_gemini_v1beta", Enabled: true}
	// 模型缺 video_cancel → 拒绝。
	w4Exec(t, s, `UPDATE models SET supported_endpoint_modes_json='["video_create","video_get","video_content"]' WHERE id='m-veo'`)
	if _, err := s.Create(ctx, in); !errors.Is(err, ErrMappingTargetUnsupported) {
		t.Fatalf("three-mode video target must be rejected, err=%v", err)
	}
	// 连接缺 video_get → 拒绝（模型已补全）。
	w4Exec(t, s, `UPDATE models SET supported_endpoint_modes_json='["video_create","video_get","video_content","video_cancel"]' WHERE id='m-veo'`)
	w4Exec(t, s, `DELETE FROM account_endpoint_modes WHERE connection_id='conn-a1-video' AND endpoint_mode='video_get'`)
	if _, err := s.Create(ctx, in); !errors.Is(err, ErrMappingTargetUnsupported) {
		t.Fatalf("connection missing video_get must be rejected, err=%v", err)
	}
	// 四 mode 全启用 + 全声明 → 写入成功。
	w4Exec(t, s, `INSERT INTO account_endpoint_modes (connection_id,endpoint_mode) VALUES ('conn-a1-video','video_get')`)
	a, err := s.Create(ctx, in)
	if err != nil {
		t.Fatalf("four-mode video target must pass: %v", err)
	}
	if a.ModelMappings[0].UpstreamModel != "veo-test" {
		t.Fatalf("mappings=%+v", a.ModelMappings)
	}
}

// TestW4TTSMappingRequiresAudioSpeech（§5.2 #17）：TTS 目标必须有 audio_speech
// （连接与模型双侧）。
func TestW4TTSMappingRequiresAudioSpeech(t *testing.T) {
	s := ready(t)
	ctx := context.Background()
	w4Exec(t, s, `INSERT INTO account_connections (id,account_id,connection_preset_code,protocol_surface_code,base_url,enabled,created_at,updated_at)
		VALUES ('conn-a1-audio','a1','generic_openai_v1_audio','openai_v1','https://audio.example.invalid',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	w4Exec(t, s, `INSERT INTO account_endpoint_modes (connection_id,endpoint_mode) VALUES ('conn-a1-audio','chat_json')`)
	w4Exec(t, s, `INSERT INTO models (id,vendor_code,public_model,upstream_model,scope,system_account_id,status,supported_endpoint_modes_json)
		VALUES ('m-tts',NULL,'tts-test','tts-test','global',NULL,'active','["chat_json"]')`)
	in := input()
	in.ModelMappings[0] = ModelMapping{SourceModel: "client-tts", SourceEndpointFamily: "tts", UpstreamModel: "tts-test", UpstreamEndpointFamily: "tts", TargetConnectionPresetCode: "generic_openai_v1_audio", Enabled: true}
	if _, err := s.Create(ctx, in); !errors.Is(err, ErrMappingTargetUnsupported) {
		t.Fatalf("tts without audio_speech must be rejected, err=%v", err)
	}
	w4Exec(t, s, `INSERT INTO account_endpoint_modes (connection_id,endpoint_mode) VALUES ('conn-a1-audio','audio_speech')`)
	w4Exec(t, s, `UPDATE models SET supported_endpoint_modes_json='["audio_speech"]' WHERE id='m-tts'`)
	if _, err := s.Create(ctx, in); err != nil {
		t.Fatalf("tts with audio_speech must pass: %v", err)
	}
}

// TestW4PatchReplacesEntryMappings：patch 的 ModelMappings 整集合替换
// （§8「任一集合字段出现时整集合替换…省略即删除」），且 replace 后 DTO 往返
// 一致、config_revision 恰好 +1。
func TestW4PatchReplacesEntryMappings(t *testing.T) {
	s := ready(t)
	ctx := context.Background()
	a, err := s.Create(ctx, input())
	if err != nil {
		t.Fatal(err)
	}
	replacement := []ModelMapping{
		{SourceModel: "alias-a", SourceEndpointFamily: "chat_completions", UpstreamModel: "gpt-5", UpstreamEndpointFamily: "chat_completions", TargetConnectionPresetCode: "generic_openai_v1", Enabled: true},
		{SourceModel: "alias-b", SourceEndpointFamily: "chat_completions", UpstreamModel: "gpt-5", UpstreamEndpointFamily: "chat_completions", TargetConnectionPresetCode: "generic_openai_v1", Enabled: false},
	}
	patched, err := s.Patch(ctx, "sys", "a1", Patch{ExpectedConfigRevision: a.ConfigRevision, ModelMappings: &replacement})
	if err != nil {
		t.Fatal(err)
	}
	if patched.ConfigRevision != a.ConfigRevision+1 || len(patched.ModelMappings) != 2 {
		t.Fatalf("patched=%+v", patched)
	}
	var count int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM account_entry_mappings WHERE account_id='a1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("entry mapping rows=%d want 2 (整集合替换)", count)
	}
	// 省略（空集合）即删除。
	empty := []ModelMapping{}
	cleared, err := s.Patch(ctx, "sys", "a1", Patch{ExpectedConfigRevision: patched.ConfigRevision, ModelMappings: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.ModelMappings) != 0 {
		t.Fatalf("cleared=%+v", cleared)
	}
}
