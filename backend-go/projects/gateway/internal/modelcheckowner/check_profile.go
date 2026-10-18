package modelcheckowner

import (
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// businessCheckProfileIDByPreset 是新连接器身份（account_connections 的
// connection_preset_code）到探针协议目录 ID（modelcheckprofile 的
// profile_* 常量）的确定性映射。旧 accounts.provider_code +
// provider_protocol_profile_id 身份列随设计 §8 去供应商化退役；探针的目录
// 本身未变，因此继续用 (vendor_code, preset) 解析出同一批 profile_* ID，
// 下游（modelcheckprofile.Find / FindForModel、探针 Suite、运行记录
// provider_code 列）的取值形状保持不变。不在表内的 preset（generic_*、
// 媒体 preset）没有已注册的模型检测协议目录，解析失败并 fail closed。
//
// 映射来源：modelcheckprofile 目录逐条比对新 preset 词表
//（shared/contracts/connector_registry.go）；anthropic 目录 ID
// （profile_anthropic_anthropic_v1）与 preset（anthropic_v1）字面不同，
// 按目录字面映射，不做字符串拼推导。
var businessCheckProfileIDByPreset = map[string]string{
	"gpt_openai_v1":             "profile_gpt_openai_v1",
	"deepseek_openai_v1":        "profile_deepseek_openai_v1",
	"deepseek_anthropic_v1":     "profile_deepseek_anthropic_v1",
	"glm_general_openai_v1":     "profile_glm_general_openai_v1",
	"glm_coding_openai_v1":      "profile_glm_coding_openai_v1",
	"glm_coding_anthropic_v1":   "profile_glm_coding_anthropic_v1",
	"anthropic_v1":              "profile_anthropic_anthropic_v1",
	"gemini_native_v1beta":      "profile_gemini_native_v1beta",
	"gemini_openai_chat_v1beta": "profile_gemini_openai_chat_v1beta",
}

// businessCheckProfile resolves the probe protocol profile from the
// de-provider account identity: accounts.vendor_code plus the preset of the
// account's health-check connection. The returned string is the catalog
// profile ID that downstream consumers keep seeing.
func businessCheckProfile(vendorCode, preset string) (modelcheckprofile.ProtocolProfile, string, bool) {
	profileID, ok := businessCheckProfileIDByPreset[strings.TrimSpace(preset)]
	if !ok {
		return modelcheckprofile.ProtocolProfile{}, "", false
	}
	profile, ok := modelcheckprofile.Find(vendorCode, profileID)
	if !ok {
		return modelcheckprofile.ProtocolProfile{}, "", false
	}
	return profile, profileID, true
}
