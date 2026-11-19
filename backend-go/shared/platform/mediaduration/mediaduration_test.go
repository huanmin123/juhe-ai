package mediaduration

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"math/rand"
	"testing"
)

// ---------- 夹具构造 ----------

func appendU16LE(dst []byte, v uint16) []byte {
	var tmp [2]byte
	binary.LittleEndian.PutUint16(tmp[:], v)
	return append(dst, tmp[:]...)
}

func appendU32LE(dst []byte, v uint32) []byte {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], v)
	return append(dst, tmp[:]...)
}

func appendSyncSafe(dst []byte, v uint32) []byte {
	return append(dst,
		byte((v>>21)&0x7F),
		byte((v>>14)&0x7F),
		byte((v>>7)&0x7F),
		byte(v&0x7F),
	)
}

func buildWAV(sampleRate, channels, bits int, pcm []byte) []byte {
	blockAlign := channels * bits / 8
	byteRate := sampleRate * blockAlign
	data := make([]byte, 0, 44+len(pcm)+1)
	data = append(data, "RIFF"...)
	data = appendU32LE(data, uint32(36+len(pcm)))
	data = append(data, "WAVE"...)
	data = append(data, "fmt "...)
	data = appendU32LE(data, 16)
	data = appendU16LE(data, 1) // PCM
	data = appendU16LE(data, uint16(channels))
	data = appendU32LE(data, uint32(sampleRate))
	data = appendU32LE(data, uint32(byteRate))
	data = appendU16LE(data, uint16(blockAlign))
	data = appendU16LE(data, uint16(bits))
	data = append(data, "data"...)
	data = appendU32LE(data, uint32(len(pcm)))
	data = append(data, pcm...)
	if len(pcm)%2 == 1 {
		data = append(data, 0)
	}
	return data
}

const (
	mp3FixtureFrameLen     = 417
	mp3FixtureSampleRate   = 44100
	mp3FixtureSamplesFrame = 1152
)

// buildMP3 构造 N 个 MPEG1 Layer III 128kbps/44.1kHz 帧，首帧带 Xing 帧数。
func buildMP3(frames int) []byte {
	return buildMP3WithVBR(frames, false)
}

// buildMP3VBRI 构造带 VBRI 帧数的同类夹具。
func buildMP3VBRI(frames int) []byte {
	return buildMP3WithVBR(frames, true)
}

func buildMP3WithVBR(frames int, vbri bool) []byte {
	data := append([]byte("ID3"), 0x03, 0x00, 0x00)
	data = appendSyncSafe(data, 64)
	data = append(data, make([]byte, 64)...)
	for i := 0; i < frames; i++ {
		frame := make([]byte, mp3FixtureFrameLen)
		frame[0] = 0xFF
		frame[1] = 0xFB // MPEG1 Layer III，无 CRC
		frame[2] = 0x90 // 128kbps，44.1kHz，无 padding
		frame[3] = 0x00 // stereo
		if i == 0 {
			if vbri {
				copy(frame[36:40], "VBRI")
				binary.BigEndian.PutUint16(frame[40:42], 1) // version
				binary.BigEndian.PutUint32(frame[50:54], uint32(frames))
			} else {
				xing := 4 + 32 // 帧头 + MPEG1 stereo side info
				copy(frame[xing:xing+4], "Xing")
				binary.BigEndian.PutUint32(frame[xing+4:xing+8], 1) // frames 标志
				binary.BigEndian.PutUint32(frame[xing+8:xing+12], uint32(frames))
			}
		}
		data = append(data, frame...)
	}
	return data
}

const (
	aacFixtureFrameLen     = 100
	aacFixtureSampleRateID = 4 // 44100
)

// buildAAC 构造 N 个 ADTS AAC-LC 帧。
func buildAAC(frames int) []byte {
	data := make([]byte, 0, frames*aacFixtureFrameLen)
	for i := 0; i < frames; i++ {
		frame := make([]byte, aacFixtureFrameLen)
		frame[0] = 0xFF
		frame[1] = 0xF1 // MPEG-4，layer 00，无 CRC
		frame[2] = byte(1<<6) | byte(aacFixtureSampleRateID<<2)
		frame[3] = byte(1 << 6) // channel_config=1
		frame[4] = byte(aacFixtureFrameLen >> 3)
		frame[5] = byte((aacFixtureFrameLen&7)<<5) | 0x1F
		frame[6] = 0xFC
		data = append(data, frame...)
	}
	return data
}

// buildAACCRC 构造带 2 字节 CRC 的 ADTS 帧（9 字节头）。
func buildAACCRC(frames int) []byte {
	data := make([]byte, 0, frames*aacFixtureFrameLen)
	for i := 0; i < frames; i++ {
		frame := make([]byte, aacFixtureFrameLen)
		frame[0] = 0xFF
		frame[1] = 0xF0 // MPEG-4，layer 00，有 CRC
		frame[2] = byte(1<<6) | byte(aacFixtureSampleRateID<<2)
		frame[3] = byte(1 << 6) // channel_config=1
		frame[4] = byte(aacFixtureFrameLen >> 3)
		frame[5] = byte((aacFixtureFrameLen&7)<<5) | 0x1F
		frame[6] = 0xFC
		// frame[7:9] 为 CRC
		data = append(data, frame...)
	}
	return data
}

// buildWAVUnknownDataSize 构造 data size 为 0xFFFFFFFF 的流式 WAV 头。
func buildWAVUnknownDataSize(sampleRate, channels, bits int, pcm []byte) []byte {
	data := buildWAV(sampleRate, channels, bits, pcm)
	binary.LittleEndian.PutUint32(data[40:44], 0xFFFFFFFF)
	return data
}

// buildWAVZeroDataSize 构造 data size 为 0 的流式 WAV 头。
func buildWAVZeroDataSize(sampleRate, channels, bits int, pcm []byte) []byte {
	data := buildWAV(sampleRate, channels, bits, pcm)
	binary.LittleEndian.PutUint32(data[40:44], 0)
	return data
}

func buildFLAC(sampleRate int, totalSamples uint64) []byte {
	streamInfo := make([]byte, 34)
	binary.BigEndian.PutUint16(streamInfo[0:2], 4096)
	binary.BigEndian.PutUint16(streamInfo[2:4], 4096)
	const bitsPerSample = 16
	streamInfo[10] = byte(sampleRate >> 12)
	streamInfo[11] = byte(sampleRate >> 4)
	streamInfo[12] = byte((sampleRate&0x0F)<<4) | byte(((bitsPerSample-1)>>4)&0x01)
	streamInfo[13] = byte(((bitsPerSample-1)&0x0F)<<4) | byte((totalSamples>>32)&0x0F)
	binary.BigEndian.PutUint32(streamInfo[14:18], uint32(totalSamples))

	data := []byte("fLaC")
	data = append(data, 0x80, 0x00, 0x00, 34) // last block，type 0
	return append(data, streamInfo...)
}

func buildOggPage(serial, sequence uint32, headerType byte, granule uint64, body []byte) []byte {
	lacing := make([]byte, 0, len(body)/255+1)
	remaining := len(body)
	for remaining >= 255 {
		lacing = append(lacing, 255)
		remaining -= 255
	}
	lacing = append(lacing, byte(remaining))
	page := make([]byte, 27+len(lacing), 27+len(lacing)+len(body))
	copy(page[0:4], "OggS")
	page[4] = 0
	page[5] = headerType
	binary.LittleEndian.PutUint64(page[6:14], granule)
	binary.LittleEndian.PutUint32(page[14:18], serial)
	binary.LittleEndian.PutUint32(page[18:22], sequence)
	page[26] = byte(len(lacing))
	copy(page[27:], lacing)
	return append(page, body...)
}

func buildOggVorbis(sampleRate uint32, granule uint64) []byte {
	identification := make([]byte, 30)
	identification[0] = 0x01
	copy(identification[1:7], "vorbis")
	identification[11] = 1 // channels
	binary.LittleEndian.PutUint32(identification[12:16], sampleRate)
	identification[29] = 1 // framing

	data := buildOggPage(0x1234, 0, 0x02, 0, identification)
	return append(data, buildOggPage(0x1234, 1, 0x04, granule, []byte{0})...)
}

func buildOggOpus(preSkip uint16, granule uint64) []byte {
	head := make([]byte, 19)
	copy(head[0:8], "OpusHead")
	head[8] = 1 // version
	head[9] = 1 // channels
	binary.LittleEndian.PutUint16(head[10:12], preSkip)
	binary.LittleEndian.PutUint32(head[12:16], 48000)

	data := buildOggPage(0x5678, 0, 0x02, 0, head)
	return append(data, buildOggPage(0x5678, 1, 0x04, granule, []byte{0})...)
}

func buildMP4Box(boxType string, payload []byte) []byte {
	box := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(box[0:4], uint32(8+len(payload)))
	copy(box[4:8], boxType)
	return append(box, payload...)
}

func buildFTYP() []byte {
	payload := []byte("isom\x00\x00\x00\x00isomiso2")
	return buildMP4Box("ftyp", payload)
}

func buildMVHD(timescale, duration uint32) []byte {
	payload := make([]byte, 100) // version 0 + flags + 各字段 + 结尾保留
	binary.BigEndian.PutUint32(payload[12:16], timescale)
	binary.BigEndian.PutUint32(payload[16:20], duration)
	return buildMP4Box("mvhd", payload)
}

func buildMP4(timescale, duration uint32, moovFirst bool, mdatSize int) []byte {
	ftyp := buildFTYP()
	moov := buildMP4Box("moov", buildMVHD(timescale, duration))
	mdat := buildMP4Box("mdat", make([]byte, mdatSize))
	data := make([]byte, 0, len(ftyp)+len(moov)+len(mdat))
	data = append(data, ftyp...)
	if moovFirst {
		data = append(data, moov...)
		data = append(data, mdat...)
	} else {
		data = append(data, mdat...)
		data = append(data, moov...)
	}
	return data
}

// buildMP4V1 构造 version 1 mvhd（64 位 creation/modification/duration）。
func buildMP4V1(timescale uint32, duration uint64, moovFirst bool, mdatSize int) []byte {
	payload := make([]byte, 112)
	payload[0] = 1
	binary.BigEndian.PutUint32(payload[20:24], timescale)
	binary.BigEndian.PutUint64(payload[24:32], duration)
	moov := buildMP4Box("moov", buildMP4Box("mvhd", payload))
	mdat := buildMP4Box("mdat", make([]byte, mdatSize))
	ftyp := buildFTYP()
	data := make([]byte, 0, len(ftyp)+len(moov)+len(mdat))
	data = append(data, ftyp...)
	if moovFirst {
		data = append(data, moov...)
		data = append(data, mdat...)
	} else {
		data = append(data, mdat...)
		data = append(data, moov...)
	}
	return data
}

func encodeEBMLSize(v int) []byte {
	switch {
	case v < 0x7F:
		return []byte{0x80 | byte(v)}
	case v < 0x3FFF:
		return []byte{0x40 | byte(v>>8), byte(v)}
	case v < 0x1FFFFF:
		return []byte{0x20 | byte(v>>16), byte(v >> 8), byte(v)}
	default:
		return []byte{0x10 | byte(v>>24), byte(v >> 16), byte(v >> 8), byte(v)}
	}
}

func ebmlElement(id []byte, payload []byte) []byte {
	data := append([]byte{}, id...)
	data = append(data, encodeEBMLSize(len(payload))...)
	return append(data, payload...)
}

func encodeEBMLUint(v uint64) []byte {
	if v == 0 {
		return []byte{0}
	}
	var buffer [8]byte
	index := len(buffer)
	for v > 0 {
		index--
		buffer[index] = byte(v)
		v >>= 8
	}
	return append([]byte{}, buffer[index:]...)
}

func encodeEBMLFloat64(v float64) []byte {
	var buffer [8]byte
	binary.BigEndian.PutUint64(buffer[:], math.Float64bits(v))
	return buffer[:]
}

func buildSimpleBlock(relative int16) []byte {
	payload := []byte{
		0x81, // track number
		byte(uint16(relative) >> 8),
		byte(uint16(relative)),
		0x80, // keyframe
		0x00,
	}
	return ebmlElement([]byte{0xA3}, payload)
}

func buildBlockGroup(relative int16) []byte {
	payload := []byte{
		0x81, // track number
		byte(uint16(relative) >> 8),
		byte(uint16(relative)),
		0x00, // flags
		0x00,
	}
	return ebmlElement([]byte{0xA0}, ebmlElement([]byte{0xA1}, payload))
}

func buildWebM(duration *float64, clusterTimecode uint64, relative int16, withCluster bool) []byte {
	return buildWebMWithBlock(duration, clusterTimecode, withCluster, buildSimpleBlock(relative))
}

func buildWebMBlockGroup(duration *float64, clusterTimecode uint64, relative int16) []byte {
	return buildWebMWithBlock(duration, clusterTimecode, true, buildBlockGroup(relative))
}

// buildWebMUnknownCluster 构造最后一个 Cluster 使用未知大小的 WebM。
func buildWebMUnknownCluster(duration *float64, clusterTimecode uint64, relative int16) []byte {
	ebmlHeader := ebmlElement(
		[]byte{0x1A, 0x45, 0xDF, 0xA3},
		ebmlElement([]byte{0x42, 0x82}, []byte("webm")),
	)
	infoPayload := ebmlElement([]byte{0x2A, 0xD7, 0xB1}, encodeEBMLUint(1000000))
	if duration != nil {
		infoPayload = append(infoPayload, ebmlElement([]byte{0x44, 0x89}, encodeEBMLFloat64(*duration))...)
	}
	segmentPayload := ebmlElement([]byte{0x15, 0x49, 0xA9, 0x66}, infoPayload)
	cluster := []byte{0x1F, 0x43, 0xB6, 0x75, 0xFF} // 未知大小
	cluster = append(cluster, ebmlElement([]byte{0xE7}, encodeEBMLUint(clusterTimecode))...)
	cluster = append(cluster, buildSimpleBlock(relative)...)
	segmentPayload = append(segmentPayload, cluster...)
	segment := ebmlElement([]byte{0x18, 0x53, 0x80, 0x67}, segmentPayload)
	return append(ebmlHeader, segment...)
}

// buildWebMUnknownSegment 构造 Segment 大小未知的流式 WebM。
func buildWebMUnknownSegment(duration float64) []byte {
	ebmlHeader := ebmlElement(
		[]byte{0x1A, 0x45, 0xDF, 0xA3},
		ebmlElement([]byte{0x42, 0x82}, []byte("webm")),
	)
	infoPayload := ebmlElement([]byte{0x2A, 0xD7, 0xB1}, encodeEBMLUint(1000000))
	infoPayload = append(infoPayload, ebmlElement([]byte{0x44, 0x89}, encodeEBMLFloat64(duration))...)
	segment := []byte{0x18, 0x53, 0x80, 0x67, 0xFF} // 未知大小
	segment = append(segment, ebmlElement([]byte{0x15, 0x49, 0xA9, 0x66}, infoPayload)...)
	return append(ebmlHeader, segment...)
}

func buildWebMWithBlock(duration *float64, clusterTimecode uint64, withCluster bool, block []byte) []byte {
	ebmlHeader := ebmlElement(
		[]byte{0x1A, 0x45, 0xDF, 0xA3},
		ebmlElement([]byte{0x42, 0x82}, []byte("webm")),
	)
	infoPayload := ebmlElement([]byte{0x2A, 0xD7, 0xB1}, encodeEBMLUint(1000000))
	if duration != nil {
		infoPayload = append(infoPayload, ebmlElement([]byte{0x44, 0x89}, encodeEBMLFloat64(*duration))...)
	}
	segmentPayload := ebmlElement([]byte{0x15, 0x49, 0xA9, 0x66}, infoPayload)
	if withCluster {
		clusterPayload := ebmlElement([]byte{0xE7}, encodeEBMLUint(clusterTimecode))
		clusterPayload = append(clusterPayload, block...)
		segmentPayload = append(segmentPayload, ebmlElement([]byte{0x1F, 0x43, 0xB6, 0x75}, clusterPayload)...)
	}
	segment := ebmlElement([]byte{0x18, 0x53, 0x80, 0x67}, segmentPayload)
	return append(ebmlHeader, segment...)
}

// ---------- 解析用例 ----------

type parseCase struct {
	name    string
	data    []byte
	opts    Options
	format  Format
	seconds float64
}

func parseCases() []parseCase {
	duration2000 := 2000.0
	return []parseCase{
		{
			name:    "wav",
			data:    buildWAV(24000, 1, 16, make([]byte, 48000)),
			opts:    Options{},
			format:  FormatWAV,
			seconds: 1,
		},
		{
			name:    "wav-streaming-size",
			data:    buildWAVUnknownDataSize(24000, 1, 16, make([]byte, 48000)),
			opts:    Options{FormatHint: "wav"},
			format:  FormatWAV,
			seconds: 1,
		},
		{
			name:    "wav-zero-size",
			data:    buildWAVZeroDataSize(24000, 1, 16, make([]byte, 48000)),
			opts:    Options{FormatHint: "wav"},
			format:  FormatWAV,
			seconds: 1,
		},
		{
			name:    "pcm-options",
			data:    make([]byte, 48000),
			opts:    Options{FormatHint: "pcm", SampleRate: 24000, Channels: 1, BitsPerSample: 16},
			format:  FormatPCM,
			seconds: 1,
		},
		{
			name:    "pcm-content-type",
			data:    make([]byte, 48000),
			opts:    Options{ContentType: "audio/L16;rate=24000;channels=1"},
			format:  FormatPCM,
			seconds: 1,
		},
		{
			name:    "mp3",
			data:    buildMP3(40),
			opts:    Options{FormatHint: "mp3"},
			format:  FormatMP3,
			seconds: float64(40*mp3FixtureSamplesFrame) / mp3FixtureSampleRate,
		},
		{
			name:    "mp3-vbri",
			data:    buildMP3VBRI(40),
			opts:    Options{FormatHint: "mp3"},
			format:  FormatMP3,
			seconds: float64(40*mp3FixtureSamplesFrame) / mp3FixtureSampleRate,
		},
		{
			name:    "aac",
			data:    buildAAC(44),
			opts:    Options{FormatHint: "aac"},
			format:  FormatAAC,
			seconds: float64(44*1024) / 44100,
		},
		{
			name:    "aac-crc",
			data:    buildAACCRC(44),
			opts:    Options{FormatHint: "aac"},
			format:  FormatAAC,
			seconds: float64(44*1024) / 44100,
		},
		{
			name:    "flac",
			data:    buildFLAC(44100, 44100),
			opts:    Options{FormatHint: "flac"},
			format:  FormatFLAC,
			seconds: 1,
		},
		{
			name:    "ogg-vorbis",
			data:    buildOggVorbis(44100, 44100),
			opts:    Options{FormatHint: "ogg"},
			format:  FormatOGG,
			seconds: 1,
		},
		{
			name:    "ogg-opus",
			data:    buildOggOpus(312, 48312),
			opts:    Options{FormatHint: "opus"},
			format:  FormatOGG,
			seconds: 1,
		},
		{
			name:    "mp4-moov-first",
			data:    buildMP4(1000, 1500, true, 2048),
			opts:    Options{FormatHint: "mp4"},
			format:  FormatMP4,
			seconds: 1.5,
		},
		{
			name:    "mp4-moov-last",
			data:    buildMP4(1000, 1500, false, 2048),
			opts:    Options{FormatHint: "m4a"},
			format:  FormatMP4,
			seconds: 1.5,
		},
		{
			name:    "mp4-mvhd-v1",
			data:    buildMP4V1(1000, 2500, false, 2048),
			opts:    Options{FormatHint: "mp4"},
			format:  FormatMP4,
			seconds: 2.5,
		},
		{
			name:    "mov-content-type",
			data:    buildMP4(1000, 1500, false, 2048),
			opts:    Options{ContentType: "video/quicktime"},
			format:  FormatMP4,
			seconds: 1.5,
		},
		{
			name:    "webm-duration",
			data:    buildWebM(&duration2000, 1500, 500, true),
			opts:    Options{FormatHint: "webm"},
			format:  FormatWebM,
			seconds: 2,
		},
		{
			name:    "webm-cluster-fallback",
			data:    buildWebM(nil, 1500, 500, true),
			opts:    Options{ContentType: "video/webm"},
			format:  FormatWebM,
			seconds: 2,
		},
		{
			name:    "webm-blockgroup-fallback",
			data:    buildWebMBlockGroup(nil, 1000, 1000),
			opts:    Options{FormatHint: "webm"},
			format:  FormatWebM,
			seconds: 2,
		},
		{
			name:    "webm-unknown-cluster-fallback",
			data:    buildWebMUnknownCluster(nil, 1500, 500),
			opts:    Options{FormatHint: "webm"},
			format:  FormatWebM,
			seconds: 2,
		},
		{
			name:    "webm-unknown-segment-duration",
			data:    buildWebMUnknownSegment(2000),
			opts:    Options{FormatHint: "webm"},
			format:  FormatWebM,
			seconds: 2,
		},
	}
}

func TestParseFormats(t *testing.T) {
	for _, tc := range parseCases() {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]byte(nil), tc.data...)
			result, ok := Parse(tc.data, tc.opts)
			if !ok {
				t.Fatalf("Parse 失败: %s", tc.name)
			}
			if result.Format != tc.format {
				t.Fatalf("Format = %q，期望 %q", result.Format, tc.format)
			}
			if math.Abs(result.Seconds-tc.seconds) > 0.05 {
				t.Fatalf("Seconds = %.6f，期望 %.6f", result.Seconds, tc.seconds)
			}
			if !bytes.Equal(tc.data, before) {
				t.Fatal("Parse 修改了输入切片")
			}
		})
	}
}

func TestParseInvalid(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		opts Options
	}{
		{name: "nil", data: nil},
		{name: "empty", data: []byte{}},
		{name: "garbage", data: []byte("not a media file")},
		{name: "riff-not-wave", data: []byte("RIFFxxxxAVI ")},
		{name: "hint-wav-garbage", data: []byte("garbage"), opts: Options{FormatHint: "wav"}},
		{name: "hint-mp3-zeros", data: make([]byte, 128), opts: Options{FormatHint: "mp3"}},
		{name: "hint-aac-garbage", data: []byte("garbage"), opts: Options{FormatHint: "aac"}},
		{name: "hint-flac-magic-only", data: []byte("fLaC"), opts: Options{FormatHint: "flac"}},
		{name: "hint-ogg-magic-only", data: []byte("OggS"), opts: Options{FormatHint: "ogg"}},
		{name: "hint-mp4-truncated-ftyp", data: []byte("\x00\x00\x00\x08ftyp"), opts: Options{FormatHint: "mp4"}},
		{name: "hint-webm-magic-only", data: []byte{0x1A, 0x45, 0xDF, 0xA3}, opts: Options{FormatHint: "webm"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if result, ok := Parse(tc.data, tc.opts); ok {
				t.Fatalf("非法输入返回成功: %+v", result)
			}
		})
	}
}

func TestParseTruncated(t *testing.T) {
	wavData := buildWAV(24000, 1, 16, make([]byte, 48000))
	mp3Data := buildMP3(40)
	mp3VBRI := buildMP3VBRI(40)
	aacData := buildAAC(44)
	aacCRCData := buildAACCRC(44)
	flacData := buildFLAC(44100, 44100)
	oggData := buildOggVorbis(44100, 44100)
	mp4First := buildMP4(1000, 1500, true, 2048)
	mp4Last := buildMP4(1000, 1500, false, 2048)
	mp4V1 := buildMP4V1(1000, 2500, false, 2048)
	defaultScale := 1000000.0
	webmData := buildWebM(&defaultScale, 1500, 500, true)

	cases := []struct {
		name string
		data []byte
		opts Options
	}{
		{name: "wav-data", data: wavData[:len(wavData)/2], opts: Options{FormatHint: "wav"}},
		{name: "wav-fmt", data: wavData[:20], opts: Options{FormatHint: "wav"}},
		{name: "mp3", data: mp3Data[:len(mp3Data)/2], opts: Options{FormatHint: "mp3"}},
		{name: "mp3-vbri", data: mp3VBRI[:len(mp3VBRI)/2], opts: Options{FormatHint: "mp3"}},
		{name: "aac", data: aacData[:len(aacData)/2+7], opts: Options{FormatHint: "aac"}},
		{name: "aac-crc", data: aacCRCData[:len(aacCRCData)/2+7], opts: Options{FormatHint: "aac"}},
		{name: "flac", data: flacData[:20], opts: Options{FormatHint: "flac"}},
		{name: "ogg", data: oggData[:len(oggData)-1], opts: Options{FormatHint: "ogg"}},
		{name: "mp4-moov-first", data: mp4First[:len(mp4First)/2], opts: Options{FormatHint: "mp4"}},
		{name: "mp4-moov-last", data: mp4Last[:len(mp4Last)/2], opts: Options{FormatHint: "mp4"}},
		{name: "mp4-mvhd-v1", data: mp4V1[:len(mp4V1)/2], opts: Options{FormatHint: "mp4"}},
		{name: "webm", data: webmData[:len(webmData)/2], opts: Options{FormatHint: "webm"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if result, ok := Parse(tc.data, tc.opts); ok {
				t.Fatalf("截断输入返回成功: %+v", result)
			}
		})
	}
}

// ---------- Meter ----------

func writeChunks(t *testing.T, writer io.Writer, data []byte, base int) {
	t.Helper()
	for offset := 0; offset < len(data); {
		size := base + offset%17
		end := min(offset+size, len(data))
		n, err := writer.Write(data[offset:end])
		if err != nil {
			t.Fatalf("Write 返回错误: %v", err)
		}
		if n != end-offset {
			t.Fatalf("Write 返回 %d，期望 %d", n, end-offset)
		}
		offset = end
	}
}

func TestMeterMatchesParse(t *testing.T) {
	for _, tc := range parseCases() {
		t.Run(tc.name, func(t *testing.T) {
			want, ok := Parse(tc.data, tc.opts)
			if !ok {
				t.Fatalf("Parse 失败: %s", tc.name)
			}
			meter := NewMeter(tc.opts)
			writeChunks(t, meter, tc.data, 13)
			got, ok := meter.Duration()
			if !ok {
				t.Fatalf("Meter.Duration 失败: %s", tc.name)
			}
			if got.Format != want.Format {
				t.Fatalf("Meter Format = %q，期望 %q", got.Format, want.Format)
			}
			if math.Abs(got.Seconds-want.Seconds) > 0.05 {
				t.Fatalf("Meter Seconds = %.6f，Parse = %.6f", got.Seconds, want.Seconds)
			}
		})
	}
}

func TestMeterSniffWithoutHint(t *testing.T) {
	defaultScale := 1000000.0
	cases := []struct {
		name   string
		data   []byte
		format Format
	}{
		{name: "wav", data: buildWAV(24000, 1, 16, make([]byte, 48000)), format: FormatWAV},
		{name: "flac", data: buildFLAC(44100, 44100), format: FormatFLAC},
		{name: "ogg", data: buildOggVorbis(44100, 44100), format: FormatOGG},
		{name: "mp4-moov-last", data: buildMP4(1000, 1500, false, 2048), format: FormatMP4},
		{name: "webm", data: buildWebM(&defaultScale, 1500, 500, true), format: FormatWebM},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, ok := Parse(tc.data, Options{})
			if !ok {
				t.Fatalf("Parse 失败: %s", tc.name)
			}
			meter := NewMeter(Options{})
			writeChunks(t, meter, tc.data, 7)
			got, ok := meter.Duration()
			if !ok {
				t.Fatalf("Meter.Duration 失败: %s", tc.name)
			}
			if got.Format != tc.format {
				t.Fatalf("Format = %q，期望 %q", got.Format, tc.format)
			}
			if math.Abs(got.Seconds-want.Seconds) > 0.05 {
				t.Fatalf("Meter Seconds = %.6f，Parse = %.6f", got.Seconds, want.Seconds)
			}
		})
	}
}

func TestMeterBoundedBuffers(t *testing.T) {
	t.Run("pcm", func(t *testing.T) {
		meter := NewMeter(Options{FormatHint: "pcm", SampleRate: 24000, Channels: 1, BitsPerSample: 16})
		chunk := make([]byte, 64<<10)
		const total = 16 << 20
		for written := 0; written < total; written += len(chunk) {
			if _, err := meter.Write(chunk); err != nil {
				t.Fatalf("Write 返回错误: %v", err)
			}
		}
		if len(meter.head) != 0 {
			t.Fatalf("已判定格式仍保留头部 %d 字节", len(meter.head))
		}
		if meter.tail != nil {
			t.Fatal("PCM 不应创建尾环缓冲")
		}
		result, ok := meter.Duration()
		if !ok {
			t.Fatal("Meter.Duration 失败")
		}
		want := float64(total) * 8 / (24000 * 16)
		if math.Abs(result.Seconds-want) > 0.05 {
			t.Fatalf("Seconds = %.6f，期望 %.6f", result.Seconds, want)
		}
	})

	t.Run("mp4-tail-ring", func(t *testing.T) {
		data := buildMP4(1000, 1500, false, 8<<20)
		meter := NewMeter(Options{FormatHint: "mp4"})
		writeChunks(t, meter, data, 64<<10)
		if meter.tail == nil {
			t.Fatal("MP4 未创建尾环缓冲")
		}
		if meter.tail.size > maxTailBytes {
			t.Fatalf("尾环缓冲 %d 超过上限 %d", meter.tail.size, maxTailBytes)
		}
		if len(meter.head) > maxHeaderBytes {
			t.Fatalf("头部缓冲 %d 超过上限 %d", len(meter.head), maxHeaderBytes)
		}
		result, ok := meter.Duration()
		if !ok {
			t.Fatal("Meter.Duration 失败")
		}
		if math.Abs(result.Seconds-1.5) > 0.05 {
			t.Fatalf("Seconds = %.6f，期望 1.5", result.Seconds)
		}
	})

	t.Run("mp4-single-write", func(t *testing.T) {
		data := buildMP4(1000, 1500, false, 8<<20)
		meter := NewMeter(Options{FormatHint: "mp4"})
		n, err := meter.Write(data)
		if err != nil || n != len(data) {
			t.Fatalf("Write = (%d, %v)，期望 (%d, nil)", n, err, len(data))
		}
		if meter.tail == nil || meter.tail.size > maxTailBytes {
			t.Fatalf("尾环缓冲异常: %+v", meter.tail)
		}
		result, ok := meter.Duration()
		if !ok || math.Abs(result.Seconds-1.5) > 0.05 {
			t.Fatalf("Duration = (%+v, %v)，期望 1.5", result, ok)
		}
	})
}

func TestMeterDoesNotRetainInput(t *testing.T) {
	data := buildMP4(1000, 1500, false, 4096)
	meter := NewMeter(Options{FormatHint: "mp4"})
	if _, err := meter.Write(data); err != nil {
		t.Fatalf("Write 返回错误: %v", err)
	}
	first, ok := meter.Duration()
	if !ok {
		t.Fatal("首次 Duration 失败")
	}
	for i := range data {
		data[i] = 0
	}
	second, ok := meter.Duration()
	if !ok {
		t.Fatal("二次 Duration 失败")
	}
	if first.Seconds != second.Seconds {
		t.Fatalf("Duration 受输入切片修改影响: %.6f vs %.6f", first.Seconds, second.Seconds)
	}
}

func TestMeterTruncated(t *testing.T) {
	wavData := buildWAV(24000, 1, 16, make([]byte, 48000))
	mp3Data := buildMP3(40)
	aacData := buildAAC(44)
	flacData := buildFLAC(44100, 44100)
	oggData := buildOggVorbis(44100, 44100)
	mp4First := buildMP4(1000, 1500, true, 2048)
	mp4Last := buildMP4(1000, 1500, false, 2048)
	scale := 2000.0
	webmData := buildWebM(&scale, 1500, 500, true)

	cases := []struct {
		name string
		data []byte
		opts Options
	}{
		{name: "wav", data: wavData[:len(wavData)/2], opts: Options{FormatHint: "wav"}},
		{name: "mp3", data: mp3Data[:len(mp3Data)/2], opts: Options{FormatHint: "mp3"}},
		{name: "aac", data: aacData[:len(aacData)/2+7], opts: Options{FormatHint: "aac"}},
		{name: "flac", data: flacData[:20], opts: Options{FormatHint: "flac"}},
		{name: "ogg", data: oggData[:len(oggData)-1], opts: Options{FormatHint: "ogg"}},
		{name: "mp4-moov-first", data: mp4First[:len(mp4First)/2], opts: Options{FormatHint: "mp4"}},
		{name: "mp4-moov-last", data: mp4Last[:len(mp4Last)/2], opts: Options{FormatHint: "mp4"}},
		{name: "webm", data: webmData[:len(webmData)/2], opts: Options{FormatHint: "webm"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meter := NewMeter(tc.opts)
			writeChunks(t, meter, tc.data, 11)
			if result, ok := meter.Duration(); ok {
				t.Fatalf("截断流返回成功: %+v", result)
			}
		})
	}
}

func TestMeterNoData(t *testing.T) {
	options := []Options{
		{},
		{FormatHint: "wav"},
		{FormatHint: "mp3"},
		{FormatHint: "mp4"},
		{ContentType: "audio/L16;rate=24000;channels=1"},
	}
	for _, opts := range options {
		meter := NewMeter(opts)
		if result, ok := meter.Duration(); ok {
			t.Fatalf("空流返回成功: opts=%+v result=%+v", opts, result)
		}
	}
}

func TestMeterWriteAfterFailure(t *testing.T) {
	meter := NewMeter(Options{ContentType: "audio/L16;rate=bad;channels=1"})
	payload := []byte("0123456789")
	n, err := meter.Write(payload)
	if err != nil {
		t.Fatalf("Write 返回错误: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("Write 返回 %d，期望 %d", n, len(payload))
	}
	if result, ok := meter.Duration(); ok {
		t.Fatalf("非法参数返回成功: %+v", result)
	}
}

func TestRandomInputsDoNotPanic(t *testing.T) {
	rng := rand.New(rand.NewSource(20261119))
	hints := []string{"", "wav", "pcm", "mp3", "aac", "flac", "ogg", "mp4", "webm", "mov", "opus"}
	for i := 0; i < 200; i++ {
		data := make([]byte, rng.Intn(4096))
		rng.Read(data)
		for _, hint := range hints {
			result, ok := Parse(data, Options{FormatHint: hint})
			if ok && (!validSeconds(result.Seconds) || result.Format == FormatUnknown) {
				t.Fatalf("随机输入得到非法结果: hint=%q result=%+v", hint, result)
			}
		}
		meter := NewMeter(Options{FormatHint: "mp3"})
		for offset := 0; offset < len(data); {
			end := min(offset+1+rng.Intn(31), len(data))
			if _, err := meter.Write(data[offset:end]); err != nil {
				t.Fatalf("Write 返回错误: %v", err)
			}
			offset = end
		}
		if result, ok := meter.Duration(); ok && !validSeconds(result.Seconds) {
			t.Fatalf("Meter 随机输入得到非法结果: %+v", result)
		}
	}
}

// TestTruncatedAndMutatedInputsDoNotPanic 验证解析器对任意截断/字节篡改都不越界。
func TestTruncatedAndMutatedInputsDoNotPanic(t *testing.T) {
	for _, tc := range parseCases() {
		t.Run(tc.name, func(t *testing.T) {
			step := 1
			if len(tc.data) > 4096 {
				step = 97
			}
			for cut := 0; cut <= len(tc.data); cut += step {
				Parse(tc.data[:cut], tc.opts)
			}
			mutated := append([]byte(nil), tc.data...)
			for i := 0; i < len(mutated) && i < 128; i++ {
				original := mutated[i]
				for _, value := range []byte{0x00, 0xFF, 0x7F, 0x80, original ^ 0x5A} {
					mutated[i] = value
					Parse(mutated, tc.opts)
				}
				mutated[i] = original
			}
		})
	}
}
