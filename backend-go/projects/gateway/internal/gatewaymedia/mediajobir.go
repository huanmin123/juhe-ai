// mediajobir.go 是 M2 异步媒体任务的统一中间表示（媒体设计 §5：create →
// job{id,status,progress,error,usage} → poll → artifact 定位；§8.2 media_jobs
// 表字段同构）。上游各家任务状态机差异在 adapter 内吸收为统一 status 词表
// （契约 §2.6 状态归一总表）；本文件只承载模型与归一函数，不触 chain/HTTP/
// 仓储（M2 后续任务接入）。
package gatewaymedia

import "time"

// MediaJobKind 是异步任务种类（媒体设计 §8.2 media_jobs.kind 词表：video |
// audio_transcription | audio_speech；M2 仅 video 生效，音频 jobs 为 M3）。
type MediaJobKind string

const (
	JobKindVideo              MediaJobKind = "video"
	JobKindAudioTranscription MediaJobKind = "audio_transcription"
	JobKindAudioSpeech        MediaJobKind = "audio_speech"
)

// MediaJobStatus 是统一任务状态词表（契约 §2.6）。queued/in_progress/
// completed/failed 由上游状态归一产生；cancelled/expired 是本地终态（上游
// 删除后查询 404 → 本地收敛、TTL 过期），不从上游状态字面量归一产生。
type MediaJobStatus string

const (
	JobStatusQueued     MediaJobStatus = "queued"
	JobStatusInProgress MediaJobStatus = "in_progress"
	JobStatusCompleted  MediaJobStatus = "completed"
	JobStatusFailed     MediaJobStatus = "failed"
	JobStatusCancelled  MediaJobStatus = "cancelled"
	JobStatusExpired    MediaJobStatus = "expired"
)

// Terminal 报告是否终态（completed/failed/cancelled/expired）。终态后本地
// 行不再被轮询响应推进，终态计费在终态时回填（媒体设计 §8.2/§10）。
func (s MediaJobStatus) Terminal() bool {
	switch s {
	case JobStatusCompleted, JobStatusFailed, JobStatusCancelled, JobStatusExpired:
		return true
	default:
		return false
	}
}

// MediaJobError 是终态错误摘要（media_jobs.error，脱敏后的 code/message 对）。
// JSON 键 camelCase 小写：对外 job 对象（契约 §4.3）与管理面列表行统一。
type MediaJobError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// MediaJobUsage 是任务用量面。OutputVideoSeconds 是输出视频秒数（契约 §2.8：
// openai 视频上游无 usage 回报，网关自算口径取轮询响应 seconds_length；失败
// 任务不虚计）；Raw 保留厂商原生用量字段（openai 无，留空不猜测）。
type MediaJobUsage struct {
	OutputVideoSeconds *float64
	Raw                map[string]any
}

// MediaJobArtifact 是产物定位（上游 uri / 重新调用参数 + 上游时效）。零资源
// 存储原则（媒体设计 §8.1）：只记定位与时效，不存资源字节。openai content
// 时效约 1 小时但上游不回报时间戳，ExpiresAt 由链上 TTL 逻辑裁决，adapter
// 不伪造。
type MediaJobArtifact struct {
	ContentURL string
	ExpiresAt  *time.Time
}

// MediaJobIR 是异步媒体任务的统一模型：adapter 把上游 job 语义归一到此，
// 链上层（M2 后续任务）据此驱动 media_jobs 行与对外 job 对象。JobID 是对外
// job id（media_jobs 落库层生成，不使用上游 id，媒体设计 §8.2），adapter 不
// 填；ParamsApplied/ParamsIgnored 在创建时由参数归一层产出、落库后随轮询
// 响应回显（契约 §2.4 规则 5），轮询解析不重算。
type MediaJobIR struct {
	JobID         string
	Kind          MediaJobKind
	UpstreamJobID string
	Status        MediaJobStatus
	// RawStatus 记录归一时的未知上游原始状态值（契约 §2.6：未知状态一律归
	// in_progress 并记录原始值，不猜测失败、不伪造终态）。
	RawStatus     string
	Progress      *int
	Error         *MediaJobError
	Usage         MediaJobUsage
	Artifact      MediaJobArtifact
	ParamsApplied []string
	ParamsIgnored []string
}

// NormalizeOpenAIStatus 把 openai 视频任务状态字面量归一为统一 status
// （契约 §2.6 openai 列：queued/in_progress/completed/failed）。第二返回值
// 报告是否词表内的已知值；未知值（含空串）归 in_progress 且返回 false，
// 调用方应把原始值记入 MediaJobIR.RawStatus。cancelled/expired 是本地终态，
// 不在本函数词表内。
func NormalizeOpenAIStatus(s string) (MediaJobStatus, bool) {
	switch s {
	case "queued":
		return JobStatusQueued, true
	case "in_progress":
		return JobStatusInProgress, true
	case "completed":
		return JobStatusCompleted, true
	case "failed":
		return JobStatusFailed, true
	default:
		return JobStatusInProgress, false
	}
}
