package main

// chain_chat_images.go 基础纯逻辑测试：格式识别、尺寸计算、位写入器。

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	xwebp "golang.org/x/image/webp"
)

// ---------------------------------------------------------------------------
// 辅助构造器
// ---------------------------------------------------------------------------

func w1dJPEGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.SetNRGBA(0, 0, color.NRGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, nil)
	return buf.Bytes()
}

func w1dPNGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x + y), G: uint8(x), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func w1dBuildGIF(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 7, G: 8, B: 9, A: 255})
	var buf bytes.Buffer
	_ = gif.Encode(&buf, img, nil)
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// 格式识别
// ---------------------------------------------------------------------------

func TestW1DFormatSniff(t *testing.T) {
	for _, tc := range []struct {
		format string
		sup    bool
	}{
		{"jpeg", true}, {"png", true}, {"webp", true}, {"gif", true},
		{"bmp", false}, {"tiff", false}, {"svg", false}, {"", false},
	} {
		if got := isChatSupportedFormat(tc.format); got != tc.sup {
			t.Fatalf("isChatSupportedFormat(%q) = %v want %v", tc.format, got, tc.sup)
		}
	}
	for _, tc := range []struct {
		format, want string
	}{
		{"jpeg", "image/jpeg"}, {"png", "image/png"}, {"webp", "image/webp"},
		{"gif", "image/gif"}, {"bmp", ""}, {"", ""},
	} {
		if got := chatOriginalMimeType(tc.format); got != tc.want {
			t.Fatalf("chatOriginalMimeType(%q) = %q want %q", tc.format, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 解码 round-trip
// ---------------------------------------------------------------------------

func TestW1DDecodeRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		data func(t *testing.T) []byte
		want string
	}{
		{"jpeg", w1dJPEGBytes, "jpeg"},
		{"png", func(t *testing.T) []byte { return w1dPNGBytes(t, 8, 8) }, "png"},
		{"gif", w1dBuildGIF, "gif"},
		{"webp", func(t *testing.T) []byte {
			img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
			img.SetNRGBA(0, 0, color.NRGBA{R: 4, G: 5, B: 6, A: 255})
			out, _ := chainVP8LEncode(img)
			return out
		}, "webp"},
	}
	for _, tc := range cases {
		data := tc.data(t)
		src, gotFmt, err := decodeChatImage(data)
		if err != nil {
			t.Fatalf("%s decodeChatImage: %v", tc.name, err)
		}
		if gotFmt != tc.want {
			t.Fatalf("%s format = %q want %q", tc.name, gotFmt, tc.want)
		}
		if src == nil {
			t.Fatalf("%s decoded image is nil", tc.name)
		}
	}
	// 不支持格式
	_, _, err := decodeChatImage([]byte{0x00, 0x01, 0x02, 0x03})
	if err == nil {
		t.Fatal("unsupported format should fail")
	}
}

// ---------------------------------------------------------------------------
// 纯算术
// ---------------------------------------------------------------------------

func TestW1DBasicMath(t *testing.T) {
	cases := []struct {
		name string
		fn   func(int, int) int
		a, b, want int
	}{
		{"ceilDiv/10/3", ceilDiv, 10, 3, 4},
		{"ceilDiv/9/3", ceilDiv, 9, 3, 3},
		{"ceilDiv/0/32", ceilDiv, 0, 32, 0},
		{"maxOf/3/5", maxOf, 3, 5, 5},
		{"maxOf/5/3", maxOf, 5, 3, 5},
		{"minOf/3/5", minOf, 3, 5, 3},
		{"minOf/5/3", minOf, 5, 3, 3},
		{"minOf/4/4", minOf, 4, 4, 4},
	}
	for _, tc := range cases {
		if got := tc.fn(tc.a, tc.b); got != tc.want {
			t.Fatalf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// VP8L 编码器
// ---------------------------------------------------------------------------

func TestW1DVP8LBitWriter(t *testing.T) {
	w := newVP8LBitWriter()
	w.writeBits(1, 1)
	if len(w.buf) != 0 {
		t.Fatalf("partial: buf=%v", w.buf)
	}
	w.writeBits(0x7F, 7)
	if len(w.buf) != 1 || w.buf[0] != 0xFF {
		t.Fatalf("8-bit fill: buf=%v", w.buf)
	}
	w.writeBits(0, 1)
	if len(w.buf) != 1 {
		t.Fatalf("partial flush: buf=%v", w.buf)
	}
	all := w.bytes()
	if len(all) != 2 || all[0] != 0xFF || all[1] != 0x00 {
		t.Fatalf("bytes flush: %v", all)
	}

	// MSB-first 写入
	w2 := newVP8LBitWriter()
	w2.writeBitsMSBFirst(0xA0, 8)
	all2 := w2.bytes()
	if len(all2) != 1 || all2[0] != 0x05 {
		t.Fatalf("MSB-first: %v", all2)
	}

	// 多字节连续写入
	w3 := newVP8LBitWriter()
	w3.writeBits(0xAA, 8)
	w3.writeBits(0x55, 8)
	all3 := w3.bytes()
	if len(all3) != 2 || all3[0] != 0xAA || all3[1] != 0x55 {
		t.Fatalf("multi-byte: %v", all3)
	}
}

func TestW1DBuildVP8LFile(t *testing.T) {
	odd := []byte{0x01, 0x02, 0x03}
	out := buildVP8LFile(2, 2, odd)
	if string(out[:4]) != "RIFF" || string(out[8:12]) != "WEBP" || string(out[12:16]) != "VP8L" {
		t.Fatal("container header wrong")
	}
}

func TestW1DChainVP8LEncodeError(t *testing.T) {
	huge := image.NewNRGBA(image.Rect(0, 0, 16385, 1))
	_, err := chainVP8LEncode(huge)
	if err == nil {
		t.Fatal("chainVP8LEncode huge accepted")
	}
}

// ---------------------------------------------------------------------------
// VP8L round-trip
// ---------------------------------------------------------------------------

func TestW1DVP8LRoundTrip(t *testing.T) {
	sizes := []image.Rectangle{
		image.Rect(0, 0, 3, 3), image.Rect(0, 0, 5, 5), image.Rect(0, 0, 65, 65),
	}
	for _, b := range sizes {
		src := image.NewNRGBA(b)
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				src.SetNRGBA(x, y, color.NRGBA{R: uint8(x + y), G: uint8(x), B: 255, A: 255})
			}
		}
		encoded, err := chainVP8LEncode(src)
		if err != nil { t.Fatalf("%v encode: %v", b, err) }
		if len(encoded) == 0 { t.Fatalf("%v empty output", b) }
		decoded, err := xwebp.Decode(bytes.NewReader(encoded))
		if err != nil { t.Fatalf("%v decode: %v", b, err) }
		if got := decoded.Bounds(); got.Dx() != b.Dx() || got.Dy() != b.Dy() {
			t.Fatalf("%v decoded bounds %v", b, got)
		}
	}
}

// ---------------------------------------------------------------------------
// 处理器 ProcessUpload
// ---------------------------------------------------------------------------

func TestW1DProcessUploadFormats(t *testing.T) {
	processor := newChatImageProcessor()
	formatCases := []struct {
		name string
		data func(t *testing.T) []byte
	}{
		{"jpeg", w1dJPEGBytes},
		{"png", func(t *testing.T) []byte { return w1dPNGBytes(t, 64, 48) }},
		{"gif", w1dBuildGIF},
	}
	for _, tc := range formatCases {
		data := tc.data(t)
		processed, err := processor.ProcessUpload(data, "")
		if err != nil {
			t.Fatalf("%s ProcessUpload: %v", tc.name, err)
		}
		if processed.OriginalWidth <= 0 || processed.OriginalHeight <= 0 {
			t.Fatalf("%s dims = %dx%d", tc.name, processed.OriginalWidth, processed.OriginalHeight)
		}
		if processed.MimeType != "image/webp" {
			t.Fatalf("%s mime = %q", tc.name, processed.MimeType)
		}
	}
	// 不支持格式
	bmpData := []byte{0x42, 0x4D, 0x00, 0x00}
	_, err := processor.ProcessUpload(bmpData, "image/bmp")
	if err == nil {
		t.Fatal("ProcessUpload unsupported format accepted")
	}
}

// ---------------------------------------------------------------------------
// EXIF orientation
// ---------------------------------------------------------------------------

func TestW1DChatEXIFOrientation(t *testing.T) {
	for _, orient := range []int{1, 2, 3, 4, 5, 6, 7, 8} {
		// 构造最小 IFD: byte order(4) + magic(2) + offset(4) + tag count(2) + tag(2+2+2+4+2)
		ifd := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x01, 0x00,
			0x12, 0x01, 0x03, 0x00, 0x01, 0x00, 0x00, 0x00, byte(orient), 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00}
		if got := chatEXIFOrientation(ifd); got != orient {
			t.Fatalf("chatEXIFOrientation(%d) = %d", orient, got)
		}
	}
	if got := chatEXIFOrientation([]byte("II")); got != 0 {
		t.Fatalf("short: got %d", got)
	}
}

// ---------------------------------------------------------------------------
// orientedChatDimensions / boundedChatImageDimensions
// ---------------------------------------------------------------------------

func TestW1DOrientedDimensions(t *testing.T) {
	for _, tc := range []struct {
		orientation, wantW, wantH int
	}{
		{0, 100, 200}, {1, 100, 200}, {5, 200, 100}, {6, 200, 100},
	} {
		dims, ok := orientedChatDimensions(image.Rect(0, 0, 100, 200), tc.orientation)
		if !ok { t.Fatalf("orientation %d: not ok", tc.orientation) }
		if dims.width != tc.wantW || dims.height != tc.wantH {
			t.Fatalf("orientation %d dims = %dx%d want %dx%d", tc.orientation, dims.width, dims.height, tc.wantW, tc.wantH)
		}
	}
}

// ---------------------------------------------------------------------------
// toNRGBA / nrgbaAt
// ---------------------------------------------------------------------------

func TestW1DToNRGBA(t *testing.T) {
	nrgba := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	if got := toNRGBA(nrgba); got != nrgba {
		t.Fatal("toNRGBA of NRGBA must return same")
	}
	rgba := image.NewRGBA(image.Rect(0, 0, 4, 4))
	rgba.SetRGBA(1, 1, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	converted := toNRGBA(rgba)
	if converted.Bounds().Dx() != 4 {
		t.Fatalf("toNRGBA width = %d", converted.Bounds().Dx())
	}
}

func TestW1DNrgbaAt(t *testing.T) {
	rgba := image.NewRGBA(image.Rect(0, 0, 2, 2))
	rgba.SetRGBA(0, 0, color.RGBA{R: 100, G: 200, B: 50, A: 128})
	got := nrgbaAt(rgba, 0, 0)
	if got.R != 100 || got.G != 200 || got.B != 50 || got.A != 128 {
		t.Fatalf("nrgbaAt = %v", got)
	}
}
