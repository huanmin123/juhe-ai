package main

// G20 phase-3 chat image pipeline port (Node chat-image-processing.ts +
// chat-image-preview.ts, sharp-based). Go codec strategy (no new third-party
// dependency):
//
//   - decode: image/jpeg + image/png + image/gif (standard library) and
//     golang.org/x/image/webp (the decode-only official webp package),
//   - resize: golang.org/x/image/draw (CatmullRom, fit inside / no enlarge),
//   - encode: a built-in VP8L lossless WebP encoder (chainVP8LEncode).
//
// Documented divergence from sharp: the output is lossless WebP instead of
// lossy quality-82, so byte sizes run larger; the Node size gates
// (input 3 MiB / preview ladder) are enforced unchanged and surface the Node
// error messages when the lossless payload cannot fit. Swapping in a lossy
// libwebp encoder is a dependency decision kept out of this slice.
//
// JPEG EXIF orientation is honoured (Node sharp .rotate()): the APP1 EXIF
// orientation tag is parsed and applied before resizing.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"

	xdraw "golang.org/x/image/draw"
	xwebp "golang.org/x/image/webp"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
)

// chat image policy values (Node chat-image-policy.ts).
const (
	chatImageMaxEdge        = 1024 // chatImageInputPolicy.maxEdge
	chatImageMaxBytes       = 3 * 1024 * 1024
	chatImagePreviewMaxEdge = 640 // chatImagePreviewPolicy.maxEdge
	chatImagePreviewBytes   = 512 * 1024
	chatMaxDecodedPixels    = 40_000_000 // maxDecodedPixels
	chatMaxModelImagePatch  = 2_500      // maxModelImagePatches
	chatImagePatchEdge      = 32         // patchEdge
)

// chatImageProcessor implements chat.ImageProcessor.
type chatImageProcessor struct{}

func newChatImageProcessor() *chatImageProcessor { return &chatImageProcessor{} }

// ProcessUpload mirrors processChatImageFile.
func (p *chatImageProcessor) ProcessUpload(data []byte, _ string) (*chat.ProcessedImage, error) {
	src, format, err := decodeChatImage(data)
	if err != nil {
		if errors.Is(err, errChatImageDecode) {
			return nil, &chat.ImageProcessingError{Message: "图片无法解码、像素过大或文件已损坏"}
		}
		return nil, err
	}
	if !isChatSupportedFormat(format) {
		return nil, &chat.ImageProcessingError{Message: "仅支持 JPEG、PNG、WebP 或 GIF 图片"}
	}
	orientation := chatJPEGOrientation(data, format)
	dimensions, ok := orientedChatDimensions(src.Bounds(), orientation)
	if !ok {
		return nil, &chat.ImageProcessingError{Message: "无法读取图片尺寸"}
	}
	target := boundedChatImageDimensions(dimensions.width, dimensions.height)
	normalized := applyChatOrientation(src, orientation)
	output, err := encodeChatModelImage(normalized, target.width, target.height, dimensions)
	if err != nil {
		return nil, err
	}
	if int64(len(output.buffer)) > chatImageMaxBytes {
		return nil, &chat.ImageProcessingError{Message: "图片按 WebP 82 处理后仍超过 3 MiB，请裁剪图片后重试"}
	}
	sum := sha256.Sum256(output.buffer)
	return &chat.ProcessedImage{
		Buffer:           output.buffer,
		OriginalMimeType: chatOriginalMimeType(format),
		OriginalWidth:    int64(dimensions.width),
		OriginalHeight:   int64(dimensions.height),
		MimeType:         "image/webp",
		Width:            int64(output.width),
		Height:           int64(output.height),
		ByteSize:         int64(len(output.buffer)),
		SHA256:           hex.EncodeToString(sum[:]),
	}, nil
}

// chatPreviewAttempt mirrors the Node preview ladder (chatImagePreviewPolicy
// attempts; the quality axis only exists for lossy codecs and is dropped).
var chatPreviewAttempts = []struct{ maxEdge int }{
	{chatImagePreviewMaxEdge},
	{560},
	{480},
	{384},
	{256},
}

// CreatePreview mirrors createChatImagePreview.
func (p *chatImageProcessor) CreatePreview(data []byte) (*chat.ProcessedImage, error) {
	src, _, err := decodeChatImage(data)
	if err != nil {
		return nil, err
	}
	var lastBytes int
	for _, attempt := range chatPreviewAttempts {
		bounds := src.Bounds()
		width, height := bounds.Dx(), bounds.Dy()
		targetWidth, targetHeight := width, height
		if width > attempt.maxEdge || height > attempt.maxEdge {
			if width >= height {
				targetHeight = maxInt(1, height*attempt.maxEdge/width)
				targetWidth = attempt.maxEdge
			} else {
				targetWidth = maxInt(1, width*attempt.maxEdge/height)
				targetHeight = attempt.maxEdge
			}
		}
		resized := resizeChatImage(src, targetWidth, targetHeight)
		buffer, err := chainVP8LEncode(resized)
		if err != nil {
			return nil, err
		}
		lastBytes = len(buffer)
		if len(buffer) > chatImagePreviewBytes {
			continue
		}
		sum := sha256.Sum256(buffer)
		return &chat.ProcessedImage{
			Buffer:   buffer,
			MimeType: "image/webp",
			Width:    int64(targetWidth),
			Height:   int64(targetHeight),
			ByteSize: int64(len(buffer)),
			SHA256:   hex.EncodeToString(sum[:]),
		}, nil
	}
	return nil, fmt.Errorf("预览图超过 %d 字节上限（最小尝试 %d 字节）", int64(chatImagePreviewBytes), lastBytes)
}

// encodeChatModelImage mirrors reuseCompliantWebp ?? encodeModelImage: the
// lossless encoder cannot reuse the source bytes, so every upload re-encodes.
func encodeChatModelImage(src image.Image, targetWidth, targetHeight int, dimensions chatImageDimensions) (*chatEncodedImage, error) {
	if src.Bounds().Dx() == targetWidth && src.Bounds().Dy() == targetHeight {
		buffer, err := chainVP8LEncode(src)
		if err != nil {
			return nil, err
		}
		return &chatEncodedImage{buffer: buffer, width: targetWidth, height: targetHeight}, nil
	}
	resized := resizeChatImage(src, targetWidth, targetHeight)
	buffer, err := chainVP8LEncode(resized)
	if err != nil {
		return nil, err
	}
	return &chatEncodedImage{buffer: buffer, width: targetWidth, height: targetHeight}, nil
}

type chatEncodedImage struct {
	buffer []byte
	width  int
	height int
}

type chatImageDimensions struct {
	width  int
	height int
}

var errChatImageDecode = errors.New("chat image decode failed")

// decodeChatImage sniffs the actual format (sharp reads the bytes, not the
// declared MIME) and guards the Node maxDecodedPixels gate.
func decodeChatImage(data []byte) (image.Image, string, error) {
	src, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", errChatImageDecode
	}
	bounds := src.Bounds()
	pixels := bounds.Dx() * bounds.Dy()
	if pixels <= 0 || int64(pixels) > chatMaxDecodedPixels {
		return nil, "", errChatImageDecode
	}
	return src, format, nil
}

func isChatSupportedFormat(format string) bool {
	return format == "jpeg" || format == "png" || format == "webp" || format == "gif"
}

func chatOriginalMimeType(format string) string {
	switch format {
	case "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	}
	return ""
}

func init() {
	// Keep the decoders registered (image.Decode sniffs the format).
	_ = jpeg.Decode
	_ = png.Decode
	_ = gif.Decode
	_ = xwebp.Decode
}

// orientedChatDimensions mirrors orientedDimensions (orientation >= 5 swaps
// width/height).
func orientedChatDimensions(bounds image.Rectangle, orientation int) (chatImageDimensions, bool) {
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return chatImageDimensions{}, false
	}
	if orientation >= 5 && orientation <= 8 {
		return chatImageDimensions{width: height, height: width}, true
	}
	return chatImageDimensions{width: width, height: height}, true
}

// boundedChatImageDimensions mirrors boundedImageDimensions (maxEdge cap +
// the 2500-patch area cap with the 0.98 shrink loop).
func boundedChatImageDimensions(width, height int) chatImageDimensions {
	scale := 1.0
	if edgeScale := float64(chatImageMaxEdge) / float64(maxOf(width, height)); edgeScale < scale {
		scale = edgeScale
	}
	if areaScale := sqrtOf(float64(chatMaxModelImagePatch*chatImagePatchEdge*chatImagePatchEdge) / float64(width*height)); areaScale < scale {
		scale = areaScale
	}
	targetWidth := maxInt(1, int(float64(width)*scale))
	targetHeight := maxInt(1, int(float64(height)*scale))
	for chatImagePatchCount(targetWidth, targetHeight) > chatMaxModelImagePatch {
		targetWidth = maxInt(1, int(float64(targetWidth)*0.98))
		targetHeight = maxInt(1, int(float64(targetHeight)*0.98))
	}
	return chatImageDimensions{width: targetWidth, height: targetHeight}
}

func chatImagePatchCount(width, height int) int {
	return ceilDiv(width, chatImagePatchEdge) * ceilDiv(height, chatImagePatchEdge)
}

func ceilDiv(value, divisor int) int {
	return (value + divisor - 1) / divisor
}

func maxOf(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func sqrtOf(value float64) float64 {
	if value <= 0 {
		return 0
	}
	// Newton iterations keep the dependency surface at the standard library.
	estimate := value
	for i := 0; i < 64; i++ {
		next := (estimate + value/estimate) / 2
		if next >= estimate && next-estimate < 1e-12 {
			return next
		}
		if estimate-next >= 0 && estimate-next < 1e-12 {
			return estimate
		}
		estimate = next
	}
	return estimate
}

// resizeChatImage scales with CatmullRom into an NRGBA canvas.
func resizeChatImage(src image.Image, width, height int) image.Image {
	bounds := src.Bounds()
	if bounds.Dx() == width && bounds.Dy() == height {
		return toNRGBA(src)
	}
	dst := image.NewNRGBA(image.Rect(0, 0, width, height))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, xdraw.Over, nil)
	return dst
}

// toNRGBA normalizes any decoded image into straight-alpha NRGBA pixels.
func toNRGBA(src image.Image) *image.NRGBA {
	if nrgba, ok := src.(*image.NRGBA); ok && nrgba.Bounds().Min.Eq(image.Point{}) {
		return nrgba
	}
	dst := image.NewNRGBA(image.Rect(0, 0, src.Bounds().Dx(), src.Bounds().Dy()))
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	return dst
}

// ---------------------------------------------------------------------------
// JPEG EXIF orientation (Node sharp .rotate())
// ---------------------------------------------------------------------------

// chatJPEGOrientation parses the JPEG APP1 EXIF orientation tag (1-8; 0 when
// absent or not a JPEG).
func chatJPEGOrientation(data []byte, format string) int {
	if format != "jpeg" || len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 0
	}
	offset := 2
	for offset+4 <= len(data) {
		if data[offset] != 0xFF {
			return 0
		}
		marker := data[offset+1]
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			offset += 2
			continue
		}
		if offset+4 > len(data) {
			return 0
		}
		segmentLength := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		if segmentLength < 2 {
			return 0
		}
		if marker == 0xE1 && offset+10 <= len(data) && string(data[offset+4:offset+10]) == "Exif\x00\x00" {
			return chatEXIFOrientation(data[offset+10 : minOf(offset+2+segmentLength, len(data))])
		}
		offset += 2 + segmentLength
		if marker == 0xDA {
			break
		}
	}
	return 0
}

func minOf(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// chatEXIFOrientation reads tag 0x0112 from IFD0.
func chatEXIFOrientation(exif []byte) int {
	if len(exif) < 8 {
		return 0
	}
	var byteOrder binary.ByteOrder = binary.BigEndian
	switch string(exif[0:2]) {
	case "II":
		byteOrder = binary.LittleEndian
	case "MM":
		byteOrder = binary.BigEndian
	default:
		return 0
	}
	if byteOrder.Uint16(exif[2:4]) != 0x002A {
		return 0
	}
	ifdOffset := int(byteOrder.Uint32(exif[4:8]))
	if ifdOffset+2 > len(exif) {
		return 0
	}
	entries := int(byteOrder.Uint16(exif[ifdOffset : ifdOffset+2]))
	for index := 0; index < entries; index++ {
		entry := ifdOffset + 2 + index*12
		if entry+12 > len(exif) {
			return 0
		}
		tag := byteOrder.Uint16(exif[entry : entry+2])
		if tag != 0x0112 {
			continue
		}
		value := byteOrder.Uint16(exif[entry+8 : entry+10])
		if value >= 1 && value <= 8 {
			return int(value)
		}
		return 0
	}
	return 0
}

// applyChatOrientation transforms the decoded pixels so orientation 1 holds.
func applyChatOrientation(src image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return toNRGBA(src)
	}
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	swap := orientation >= 5 && orientation <= 8
	dstWidth, dstHeight := width, height
	if swap {
		dstWidth, dstHeight = height, width
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dstWidth, dstHeight))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			var dx, dy int
			switch orientation {
			case 2:
				dx, dy = width-1-x, y
			case 3:
				dx, dy = width-1-x, height-1-y
			case 4:
				dx, dy = x, height-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = height-1-y, x
			case 7:
				dx, dy = height-1-y, width-1-x
			case 8:
				dx, dy = y, width-1-x
			default:
				dx, dy = x, y
			}
			dst.SetNRGBA(dx, dy, nrgbaAt(src, bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	return dst
}

func nrgbaAt(src image.Image, x, y int) color.NRGBA {
	r, g, b, a := src.At(x, y).RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
}

// ---------------------------------------------------------------------------
// VP8L lossless WebP encoder (built-in, no new dependency)
// ---------------------------------------------------------------------------

// chainVP8LEncode encodes an image as a lossless VP8L WebP: all-literal
// coding (no LZ77 / color cache / transforms), 8-bit literal codes. The
// output decodes through golang.org/x/image/webp (asserted in tests).
func chainVP8LEncode(src image.Image) ([]byte, error) {
	nrgba := toNRGBA(src)
	bounds := nrgba.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 || width > 1<<14 || height > 1<<14 {
		return nil, errors.New("图片尺寸超出 WebP 无损编码范围")
	}

	bit := newVP8LBitWriter()
	// VP8L header: signature + dims + alpha flag + version.
	bit.writeBits(0x2f, 8)
	bit.writeBits(uint64(width-1), 14)
	bit.writeBits(uint64(height-1), 14)
	alphaUsed := uint64(0)
	for y := 0; y < height; y++ {
		row := nrgba.Pix[y*nrgba.Stride : y*nrgba.Stride+width*4]
		for x := 0; x < width; x++ {
			if row[x*4+3] != 0xff {
				alphaUsed = 1
				break
			}
		}
	}
	bit.writeBits(alphaUsed, 1)
	bit.writeBits(0, 3) // version
	// No transform.
	bit.writeBits(0, 1)
	// No color cache.
	bit.writeBits(0, 1)
	// Single meta huffman group.
	bit.writeBits(0, 1)

	// Huffman codes: green (256 literals + 24 length codes), red, blue,
	// alpha (256 literals each), distance (40 symbols, unused).
	writeLiteralHuffmanCode(bit, 24)
	writeLiteralHuffmanCode(bit, 0)
	writeLiteralHuffmanCode(bit, 0)
	writeLiteralHuffmanCode(bit, 0)
	writeDistanceHuffmanCode(bit)

	// Pixel data: green, red, blue, alpha literals (canonical 8-bit codes).
	for y := 0; y < height; y++ {
		row := nrgba.Pix[y*nrgba.Stride : y*nrgba.Stride+width*4]
		for x := 0; x < width; x++ {
			g, r, b, a := row[x*4+1], row[x*4+0], row[x*4+2], row[x*4+3]
			bit.writeBitsMSBFirst(uint64(g), 8)
			bit.writeBitsMSBFirst(uint64(r), 8)
			bit.writeBitsMSBFirst(uint64(b), 8)
			bit.writeBitsMSBFirst(uint64(a), 8)
		}
	}

	payload := bit.bytes()
	return buildVP8LFile(width, height, payload), nil
}

// buildVP8LFile wraps the VP8L payload into the RIFF container.
func buildVP8LFile(width, height int, payload []byte) []byte {
	if len(payload)%2 == 1 {
		payload = append(payload, 0)
	}
	out := make([]byte, 0, 12+8+len(payload))
	out = append(out, 'R', 'I', 'F', 'F')
	riffSize := uint32(4 + 8 + len(payload))
	out = binary.LittleEndian.AppendUint32(out, riffSize)
	out = append(out, 'W', 'E', 'B', 'P')
	out = append(out, 'V', 'P', '8', 'L')
	out = binary.LittleEndian.AppendUint32(out, uint32(len(payload)))
	out = append(out, payload...)
	return out
}

// vp8LBitWriter is an LSB-first bit writer (the VP8L bit order).
type vp8LBitWriter struct {
	buf     []byte
	current uint64
	bits    uint
}

func newVP8LBitWriter() *vp8LBitWriter { return &vp8LBitWriter{} }

func (w *vp8LBitWriter) writeBits(value uint64, count int) {
	w.current |= (value & ((1 << uint(count)) - 1)) << w.bits
	w.bits += uint(count)
	for w.bits >= 8 {
		w.buf = append(w.buf, byte(w.current&0xff))
		w.current >>= 8
		w.bits -= 8
	}
}

// writeBitsMSBFirst writes a huffman code: the code's most significant bit
// first into the LSB-first stream.
func (w *vp8LBitWriter) writeBitsMSBFirst(value uint64, count int) {
	reversed := uint64(0)
	for i := 0; i < count; i++ {
		reversed = (reversed << 1) | ((value >> uint(i)) & 1)
	}
	w.writeBits(reversed, count)
}

func (w *vp8LBitWriter) bytes() []byte {
	if w.bits > 0 {
		w.buf = append(w.buf, byte(w.current&0xff))
		w.current = 0
		w.bits = 0
	}
	return w.buf
}

// vp8LCodeLengthCodeOrder mirrors the VP8L codeLengthCodeOrder (symbol 16
// sits at index 8, not at the tail).
var vp8LCodeLengthCodeOrder = [19]int{17, 18, 0, 1, 2, 3, 4, 5, 16, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}

// writeLiteralHuffmanCode writes one 256-literal alphabet: a normal
// code-length code over CL symbols {0, 8} followed by the length sequence
// (256 '8's; the green alphabet appends its 24 unused length codes as '0's).
func writeLiteralHuffmanCode(bit *vp8LBitWriter, trailingZeros int) {
	bit.writeBits(0, 1) // normal code-length code
	const numCodeLengths = 12
	bit.writeBits(uint64(numCodeLengths-4), 4)
	lengths := make([]int, numCodeLengths) // zero for every symbol in the order prefix
	for index, symbol := range vp8LCodeLengthCodeOrder[:numCodeLengths] {
		switch symbol {
		case 0, 8:
			lengths[index] = 1 // canonical: 0 -> "0", 8 -> "1"
		}
	}
	for _, length := range lengths {
		bit.writeBits(uint64(length), 3)
	}
	// maxSymbol coding disabled (decodeCodeLengths' useLength bit).
	bit.writeBits(0, 1)
	// CL code: symbol 0 = "0", symbol 8 = "1".
	for i := 0; i < 256; i++ {
		bit.writeBits(1, 1)
	}
	for i := 0; i < trailingZeros; i++ {
		bit.writeBits(0, 1)
	}
}

// writeDistanceHuffmanCode writes the unused distance alphabet: a normal
// code-length code over CL symbols {0, 1} followed by '1' then 39 '0's
// (exactly one assigned distance symbol keeps the tree valid; it is never
// read because no LZ77 copies are emitted).
func writeDistanceHuffmanCode(bit *vp8LBitWriter) {
	bit.writeBits(0, 1) // normal code-length code
	const numCodeLengths = 4
	bit.writeBits(uint64(numCodeLengths-4), 4)
	for _, symbol := range vp8LCodeLengthCodeOrder[:numCodeLengths] {
		length := 0
		if symbol == 0 || symbol == 1 {
			length = 1 // canonical: 0 -> "0", 1 -> "1"
		}
		bit.writeBits(uint64(length), 3)
	}
	// maxSymbol coding disabled (decodeCodeLengths' useLength bit).
	bit.writeBits(0, 1)
	// Code length sequence: symbol 1 ("1") then 39 × symbol 0 ("0").
	bit.writeBits(1, 1)
	for i := 1; i < 40; i++ {
		bit.writeBits(0, 1)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
