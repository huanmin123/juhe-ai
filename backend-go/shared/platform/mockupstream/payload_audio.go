package mockupstream

import (
	"bytes"
	"encoding/binary"
)

// Built-in synthetic audio payloads for the media binary channel (contract
// docs/functions/媒体上游协议契约与Mock上游规格.md §3.2.3). WAV and raw PCM are
// fully legal: a RIFF/WAVE header over silent 16-bit PCM frames (24 kHz
// mono, ~1 second). The remaining formats are magic-bytes sentinels —
// structural headers sufficient for content-type / magic-byte assertions,
// not decodable audio (noted per builder).

const (
	pcmSampleRateHz = 24000
	pcmChannels     = 1
	pcmBits         = 16
	pcmSeconds      = 1
)

// silencePCM returns the shared silent raw PCM frame bytes: 24 kHz, 16-bit,
// mono, ~1 second (all zero-amplitude frames). It is the payload for
// response_format=pcm and the base64 body of the Gemini inlineData part.
func silencePCM() []byte {
	return make([]byte, pcmSampleRateHz*pcmChannels*(pcmBits/8)*pcmSeconds)
}

// wavPayload builds a legal RIFF/WAVE file: canonical 44-byte header (PCM
// fmt, 24 kHz / 16-bit / mono) followed by the silent data chunk.
// Assertions use the RIFF/WAVE/fmt/data structure plus chunk size
// consistency (TestMediaWAVPayloadStructure).
func wavPayload() []byte {
	data := silencePCM()
	blockAlign := pcmChannels * pcmBits / 8
	b := make([]byte, 0, 44+len(data))
	b = append(b, "RIFF"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(36+len(data)))
	b = append(b, "WAVE"...)
	b = append(b, "fmt "...)
	b = binary.LittleEndian.AppendUint32(b, 16)
	b = binary.LittleEndian.AppendUint16(b, 1) // audio format: PCM
	b = binary.LittleEndian.AppendUint16(b, pcmChannels)
	b = binary.LittleEndian.AppendUint32(b, pcmSampleRateHz)
	b = binary.LittleEndian.AppendUint32(b, pcmSampleRateHz*uint32(blockAlign))
	b = binary.LittleEndian.AppendUint16(b, uint16(blockAlign))
	b = binary.LittleEndian.AppendUint16(b, pcmBits)
	b = append(b, "data"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(data)))
	b = append(b, data...)
	return b
}

// mp3Payload synthesizes three MPEG-1 Layer III frames (128 kbps, 44.1 kHz,
// mono, 417 bytes each). Assertion basis is the magic bytes — every frame
// starts with the 0xFF 0xFB MPEG frame sync — the frame payloads are not
// decodable audio.
func mp3Payload() []byte {
	frame := make([]byte, 417) // 144 * 128000 / 44100, padding bit 0
	copy(frame, []byte{0xFF, 0xFB, 0x90, 0xC0})
	return bytes.Repeat(frame, 3)
}

// oggOpusPayload emits one Ogg page carrying an OpusHead. Assertion basis is
// the magic bytes: the "OggS" capture pattern plus the "OpusHead" magic
// (the page CRC is not computed and the stream carries no Opus packets).
func oggOpusPayload() []byte {
	return []byte{
		'O', 'g', 'g', 'S', 0x00, 0x02, // capture pattern, version 0, BOS page
		0, 0, 0, 0, 0, 0, 0, 0, // granule position
		0x6d, 0x6f, 0x63, 0x6b, // stream serial ("mock", deterministic)
		0, 0, 0, 0, // page sequence number
		0, 0, 0, 0, // CRC checksum (not computed: magic-bytes basis)
		0x01, 0x13, // one lacing value: a 19-byte segment
		'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', // OpusHead magic
		0x01, 0x01, // version 1, channel count 1 (mono)
		0x00, 0x0c, // pre-skip
		0x80, 0xbb, 0x00, 0x00, // input sample rate 48000
		0x00, 0x00, // output gain
		0x00, // mapping family
	}
}

// aacPayload emits one ADTS frame header (MPEG-4 AAC-LC, 44.1 kHz, mono)
// with a 25-byte payload. Assertion basis is the magic bytes — the 0xFF 0xF1
// ADTS sync — the frame is not decodable audio.
func aacPayload() []byte {
	return append([]byte{0xFF, 0xF1, 0x50, 0x40, 0x04, 0x00, 0x00}, make([]byte, 25)...)
}

// flacPayload emits the "fLaC" stream marker plus the STREAMINFO metadata
// block header (last-block flag, type 0, length 34) over a zeroed info
// block. Assertion basis is the "fLaC" magic bytes.
func flacPayload() []byte {
	return append([]byte{
		'f', 'L', 'a', 'C',
		0x80, 0x00, 0x00, 0x22, // last metadata block, STREAMINFO, 34 bytes
	}, make([]byte, 34)...)
}

// audioResponseFor maps the OpenAI speech response_format to the response
// content type and the built-in payload: mp3→audio/mpeg, wav→audio/wav,
// pcm→audio/L16;rate=24000 (contract §4.1/§5.1); opus/aac/flac carry their
// standard container MIME types. An empty format is the OpenAI default mp3.
// ok=false means the value is outside the OpenAI word list — like the real
// upstream the mock 400s instead of guessing a payload.
func audioResponseFor(format string) (contentType string, payload []byte, ok bool) {
	switch format {
	case "", "mp3":
		return "audio/mpeg", mp3Payload(), true
	case "wav":
		return "audio/wav", wavPayload(), true
	case "pcm":
		return "audio/L16;rate=24000", silencePCM(), true
	case "opus":
		return "audio/ogg", oggOpusPayload(), true
	case "aac":
		return "audio/aac", aacPayload(), true
	case "flac":
		return "audio/flac", flacPayload(), true
	default:
		return "", nil, false
	}
}
