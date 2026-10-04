package mockupstream

import "encoding/binary"

// Minimal MP4 container for the M2 video binary channel (contract
// docs/functions/媒体上游协议契约与Mock上游规格.md §3.2.3). Like the mp3/ogg
// sentinels in payload_audio.go this is a structural container, not
// decodable video: there is no moov/trak structure, only the ISO-BMFF
// header boxes needed for stable assertions.
const (
	mp4FTYPSize = 32 // 8 (box header) + 4 major brand + 4 minor version + 16 compatible brands
	mp4MDATBody = 32
)

// mp4Payload builds one ftyp box (major brand "isom", minor version 0,
// compatible brands "isom"/"iso2"/"avc1"/"mp41") followed by one mdat box
// carrying a deterministic 32-byte payload. Assertion basis (magic bytes):
// a big-endian size-prefixed "ftyp" box at offset 0 — bytes[4:8] == "ftyp",
// ftyp box size 32 (bytes[0:4]), the brand block bytes[8:32] — then the mdat
// marker at bytes[36:40] with size 40 (bytes[32:36]); total length 72.
func mp4Payload() []byte {
	b := make([]byte, 0, mp4FTYPSize+8+mp4MDATBody)
	b = binary.BigEndian.AppendUint32(b, mp4FTYPSize)
	b = append(b, "ftyp"...)
	b = append(b, "isom"...)
	b = binary.BigEndian.AppendUint32(b, 0) // minor version
	b = append(b, "isom"...)
	b = append(b, "iso2"...)
	b = append(b, "avc1"...)
	b = append(b, "mp41"...)
	b = binary.BigEndian.AppendUint32(b, uint32(8+mp4MDATBody))
	b = append(b, "mdat"...)
	body := make([]byte, mp4MDATBody)
	for i := range body {
		body[i] = byte(i) // deterministic, non-zero filler
	}
	return append(b, body...)
}
