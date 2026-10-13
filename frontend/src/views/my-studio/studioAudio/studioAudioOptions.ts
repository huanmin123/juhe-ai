import type { StudioWorkbenchDomain } from '@/types/domain'

/** 音频工作台常量（规划 F2.2）：轮询/同步作用域、域、历史分页。 */
export const AUDIO_WORKBENCH_SCOPE = 'my-studio/audio'

export const audioWorkbenchDomain: StudioWorkbenchDomain = 'audio'

export const studioAudioHistoryPageSize = 10

/** 端点模式词表（§4.1）：TTS/STT 页签据此过滤模型目录。 */
export const AUDIO_SPEECH_ENDPOINT_MODE = 'audio_speech'
export const AUDIO_TRANSCRIPTION_ENDPOINT_MODE = 'audio_transcription'
