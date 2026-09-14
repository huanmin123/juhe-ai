package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"testing"

	xwebp "golang.org/x/image/webp"
)

// Helpers for EXIF handling - no NUL bytes in strings
func w1iBuildEXIFIFD(orientation int) []byte {
	ifd := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00}
	if orientation >= 1 && orientation <= 8 {
		ifd = append(ifd, 0x01, 0x00, 0x12, 0x01, 0x03, 0x00,
			0x01, 0x00, 0x00, 0x00, byte(orientation), 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00)
	} else {
		ifd = append(ifd, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	}
	return ifd
}

func w1iBuildEXIFJPEG(orientation int) []byte {
	ifd := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x12, 0x01, 0x03, 0x00, 0x01, 0x00, 0x00, 0x00,
		byte(orientation), 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	// Use string concatenation to avoid NUL in source
	app1 := append([]byte("Exif\x00\x00"), ifd...)
	segLen := len(app1) + 2
	buf := []byte{0xFF, 0xD8, 0xFF, 0xE1, byte(segLen >> 8), byte(segLen)}
	buf = append(buf, app1...)
	buf = append(buf, 0xFF, 0xD9)
	return buf
}

func TestW1iArmsMathFunctions(t *testing.T) {
	if ceilDiv(10, 3) != 4 { t.Fatal("ceilDiv(10,3)") }
	if ceilDiv(9, 3) != 3 { t.Fatal("ceilDiv(9,3)") }
	if ceilDiv(0, 32) != 0 { t.Fatal("ceilDiv(0,32)") }
	if maxOf(3, 5) != 5 { t.Fatal("maxOf(3,5)") }
	if minOf(5, 3) != 3 { t.Fatal("minOf(5,3)") }
	if sqrtOf(256) < 15.9 || sqrtOf(256) > 16.1 { t.Fatal("sqrtOf(256)") }
}

func TestW1iArmsChatJPEGOrientation(t *testing.T) {
	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x41, 0x4B, 0x47, 0x49, 0x46, 0x00, 0x01}
	if got := chatJPEGOrientation(jpegData, "jpeg"); got != 0 {
		t.Fatalf("no exif jpeg: got %d", got)
	}
	for _, o := range []int{1, 2, 3, 4, 5, 6, 7, 8} {
		if got := chatEXIFOrientation(w1iBuildEXIFIFD(o)); got != o {
			t.Fatalf("EXIF orientation %d -> got %d", o, got)
		}
	}
}

func TestW1iArmsDecodeUnsupported(t *testing.T) {
	_, _, err := decodeChatImage([]byte{0x00, 0x01, 0x02, 0x03})
	if !errors.Is(err, errChatImageDecode) {
		t.Fatalf("expected errChatImageDecode, got %v", err)
	}
}

func TestW1iArmsApplyOrientation(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	for _, o := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8} {
		dst := applyChatOrientation(src, o)
		if dst == nil {
			t.Fatalf("applyChatOrientation(%d) returned nil", o)
		}
	}
}

func TestW1iArmsVP8LEncodeError(t *testing.T) {
	huge := image.NewNRGBA(image.Rect(0, 0, 16385, 1))
	_, err := chainVP8LEncode(huge)
	if err == nil {
		t.Fatal("huge image should error")
	}
}

func TestW1iArmsVP8LBitWriter(t *testing.T) {
	w := newVP8LBitWriter()
	w.writeBits(1, 1)
	if len(w.buf) != 0 {
		t.Fatalf("partial write buffer: %v", w.buf)
	}
	w.writeBits(0x7F, 7)
	if w.buf[0] != 0xFF {
		t.Fatalf("7-bit fill: 0x%02X", w.buf[0])
	}
	w.writeBits(0, 1)
	all := w.bytes()
	if len(all) != 2 || all[0] != 0xFF || all[1] != 0x00 {
		t.Fatalf("after partial: %v", all)
	}
}

func TestW1iArmsBuildVP8LFile(t *testing.T) {
	payload := []byte{0x01, 0x02, 0x03}
	out := buildVP8LFile(2, 2, payload)
	if string(out[:4]) != "RIFF" { t.Fatal("RIFF missing") }
	if string(out[8:12]) != "WEBP" { t.Fatal("WEBP missing") }
	if string(out[12:16]) != "VP8L" { t.Fatal("VP8L missing") }
	sz := int(binary.LittleEndian.Uint32(out[16:20]))
	if sz != 4 { t.Fatalf("vp8l size = %d", sz) }
}

func TestW1iArmsVP8LRoundTrip(t *testing.T) {
	sizes := []image.Rectangle{
		image.Rect(0, 0, 3, 3), image.Rect(0, 0, 5, 5),
		image.Rect(0, 0, 65, 65), image.Rect(0, 0, 256, 1),
	}
	for _, b := range sizes {
		src := image.NewNRGBA(b)
		enc, err := chainVP8LEncode(src)
		if err != nil { t.Fatalf("%v encode: %v", b, err) }
		if string(enc[:4]) != "RIFF" || string(enc[12:16]) != "VP8L" {
			t.Fatalf("%v header wrong", b)
		}
		dec, err := xwebp.Decode(bytes.NewReader(enc))
		if err != nil { t.Fatalf("%v decode: %v", b, err) }
		if dec.Bounds().Dx() != b.Dx() || dec.Bounds().Dy() != b.Dy() {
			t.Fatalf("%v bounds mismatch", b)
		}
	}
}
