package managementmodelcheckoptions

var supportedModels = [...]string{
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"gpt-5.6-luna",
	"gpt-5.5",
	"gpt-5.4",
	"deepseek-v4-flash",
	"deepseek-v4-pro",
	"glm-5.2",
	"glm-5.1",
	"claude-opus-4-8",
	"claude-opus-4-7",
	"gemini-3.5-flash",
	"gemini-3.1-pro-preview",
}

type Service struct{}

type Option struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type TrustedComparison struct {
	EnabledByDefault  bool   `json:"enabledByDefault"`
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
	Message           string `json:"message,omitempty"`
}

type Result struct {
	SupportedModels   []Option          `json:"supportedModels"`
	SupportedProfiles []Option          `json:"supportedProfiles"`
	DefaultModel      string            `json:"defaultModel"`
	DefaultProfile    string            `json:"defaultProfile"`
	TrustedComparison TrustedComparison `json:"trustedComparison"`
}

func NewService() *Service {
	return &Service{}
}

func (*Service) Options() Result {
	models := make([]Option, 0, len(supportedModels))
	for _, model := range supportedModels {
		models = append(models, Option{Value: model, Label: model})
	}
	return Result{
		SupportedModels: models,
		SupportedProfiles: []Option{
			{
				Value:       "quick",
				Label:       "快速检测",
				Description: "最多执行 2 个轻量串行探针，快速给出初步判断",
			},
			{
				Value:       "full",
				Label:       "深度检测",
				Description: "准确优先，不以成本和耗时为约束，执行多轮协议、行为指纹、长上下文、稳定性和可信对比探针",
			},
		},
		DefaultModel:   supportedModels[0],
		DefaultProfile: "full",
		TrustedComparison: TrustedComparison{
			Available: true,
			Message:   "可信对比默认关闭；选择一个你信任的可用 OpenAI Responses / OpenAI Chat Completions / Anthropic Messages / Gemini native 协议账户后，会额外消耗该账户额度",
		},
	}
}
