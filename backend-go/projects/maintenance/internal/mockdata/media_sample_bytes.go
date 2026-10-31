package mockdata

// 媒体样本字节生成器（原与 chat 媒体会话样本同文件
// domain_chat_media_sample.go；chat 会话面样本已随演练场设计 §8 退役，本文件
// 只保留 business 域 media_jobs 与 observability blob 样本仍消费的纯字节生成）。

// chatCodexWAVBytes 是 1 秒静音 PCM WAV 的字节数（44 字节 RIFF 头 + 8000 帧
// 16bit mono 采样），与 chatCodexWAV 的输出恒等。
const chatCodexWAVBytes = 44 + 8000*2

// chatCodexWAV 生成合法 RIFF WAV（PCM 16bit mono 8kHz，1 秒静音，16044 字节）：
// 浏览器 <audio> 只需容器与 fmt 块正确即可解码播放，静音数据满足「可播放且不
// 含内容」的造数要求。
func chatCodexWAV() []byte {
	const sampleRate = 8000
	const dataBytes = sampleRate * 2 // 1 秒 * 16bit mono
	file := []byte{'R', 'I', 'F', 'F'}
	file = appendLittleEndian(file, uint32(36+dataBytes))
	file = append(file, 'W', 'A', 'V', 'E', 'f', 'm', 't', ' ')
	file = appendLittleEndian(file, 16)           // fmt 块固定 16 字节（PCM 无扩展区）
	file = appendUint16(file, 1)                  // audio format = 1（PCM）
	file = appendUint16(file, 1)                  // 声道数 = 1（mono）
	file = appendLittleEndian(file, sampleRate)   // 采样率
	file = appendLittleEndian(file, sampleRate*2) // byte rate = 采样率 * 块对齐
	file = appendUint16(file, 2)                  // block align = 2（16bit mono）
	file = appendUint16(file, 16)                 // 位深 = 16
	file = append(file, 'd', 'a', 't', 'a')
	file = appendLittleEndian(file, dataBytes)
	return append(file, make([]byte, dataBytes)...)
}

// appendLittleEndian 以小端序追加 32 位字段（RIFF 尺寸、采样率等）。
func appendLittleEndian(target []byte, value uint32) []byte {
	return append(target, byte(value), byte(value>>8), byte(value>>16), byte(value>>24))
}

// appendUint16 以小端序追加 16 位字段；RIFF 头里的 format/channels/block align/
// 位深都是 16 位宽，误用 4 字节 appendLittleEndian 会把头部撑错位（浏览器拒解）。
func appendUint16(target []byte, value uint16) []byte {
	return append(target, byte(value), byte(value>>8))
}

// chatCodexMP4 返回可播放 MP4 样本（chatCodexMP4Bytes 的防御性拷贝：调用方
// 拿到的是独立切片，追加或改写不会污染常量）。来源与生成命令见
// mp4_sample_bytes.go 文件头。
func chatCodexMP4() []byte {
	out := make([]byte, len(chatCodexMP4Bytes))
	copy(out, chatCodexMP4Bytes)
	return out
}
