package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	xwebp "golang.org/x/image/webp"
)

// TestChainVP8LEncodeRoundTrip asserts the built-in lossless WebP encoder
// produces a stream golang.org/x/image/webp decodes back to identical
// pixels (the codec safety net for the chat image pipeline).
func TestChainVP8LEncodeRoundTrip(t *testing.T) {
	cases := map[string]func(bounds image.Rectangle) *image.NRGBA{
		"solid": func(bounds image.Rectangle) *image.NRGBA {
			img := image.NewNRGBA(bounds)
			for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
				for x := bounds.Min.X; x < bounds.Max.X; x++ {
					img.SetNRGBA(x, y, color.NRGBA{R: 0x33, G: 0x66, B: 0x99, A: 0xff})
				}
			}
			return img
		},
		"random-noise": func(bounds image.Rectangle) *image.NRGBA {
			img := image.NewNRGBA(bounds)
			seed := uint32(0x12345678)
			next := func() uint8 {
				seed = seed*1664525 + 1013904223
				return uint8(seed >> 24)
			}
			for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
				for x := bounds.Min.X; x < bounds.Max.X; x++ {
					img.SetNRGBA(x, y, color.NRGBA{R: next(), G: next(), B: next(), A: next()})
				}
			}
			return img
		},
		"transparent-gradient": func(bounds image.Rectangle) *image.NRGBA {
			img := image.NewNRGBA(bounds)
			for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
				for x := bounds.Min.X; x < bounds.Max.X; x++ {
					img.SetNRGBA(x, y, color.NRGBA{
						R: uint8(x % 256), G: uint8(y % 256), B: uint8((x + y) % 256),
						A: uint8(uint32(x*255) / uint32(maxOf(bounds.Dx(), 1))),
					})
				}
			}
			return img
		},
		"single-pixel": func(bounds image.Rectangle) *image.NRGBA {
			img := image.NewNRGBA(bounds)
			img.SetNRGBA(0, 0, color.NRGBA{R: 1, G: 2, B: 3, A: 4})
			return img
		},
	}
	sizes := []image.Rectangle{
		image.Rect(0, 0, 1, 1),
		image.Rect(0, 0, 7, 5),
		image.Rect(0, 0, 64, 48),
	}
	for name, build := range cases {
		for _, bounds := range sizes {
			src := build(bounds)
			encoded, err := chainVP8LEncode(src)
			if err != nil {
				t.Fatalf("%s %v: encode: %v", name, bounds, err)
			}
			decoded, err := xwebp.Decode(bytes.NewReader(encoded))
			if err != nil {
				t.Fatalf("%s %v: decode round-trip: %v", name, bounds, err)
			}
			if got := decoded.Bounds(); got.Dx() != bounds.Dx() || got.Dy() != bounds.Dy() {
				t.Fatalf("%s %v: decoded bounds %v", name, bounds, got)
			}
			decodedNRGBA, ok := decoded.(*image.NRGBA)
			if !ok {
				t.Fatalf("%s %v: decoded image type %T", name, bounds, decoded)
			}
			for y := 0; y < bounds.Dy(); y++ {
				for x := 0; x < bounds.Dx(); x++ {
					want := src.NRGBAAt(x, y)
					got := decodedNRGBA.NRGBAAt(x, y)
					if got != want {
						t.Fatalf("%s %v: pixel (%d,%d) got %v want %v", name, bounds, x, y, got, want)
					}
				}
			}
		}
	}
}

// TestChatImageProcessorUploadAndPreview exercises the processor contract:
// format sniffing, dimension bounding, webp output and the sha256/size
// fields.
func TestChatImageProcessorUploadAndPreview(t *testing.T) {
	processor := newChatImageProcessor()
	src := image.NewNRGBA(image.Rect(0, 0, 300, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 300; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var pngBuffer bytes.Buffer
	if err := png.Encode(&pngBuffer, src); err != nil {
		t.Fatalf("encode png: %v", err)
	}

	processed, err := processor.ProcessUpload(pngBuffer.Bytes(), "image/png")
	if err != nil {
		t.Fatalf("process upload: %v", err)
	}
	if processed.OriginalMimeType != "image/png" {
		t.Fatalf("original mime %q", processed.OriginalMimeType)
	}
	if processed.OriginalWidth != 300 || processed.OriginalHeight != 200 {
		t.Fatalf("original dims %dx%d", processed.OriginalWidth, processed.OriginalHeight)
	}
	if processed.MimeType != "image/webp" {
		t.Fatalf("processed mime %q", processed.MimeType)
	}
	if processed.Width != 300 || processed.Height != 200 {
		t.Fatalf("processed dims %dx%d (no enlargement expected)", processed.Width, processed.Height)
	}
	if processed.ByteSize != int64(len(processed.Buffer)) || len(processed.SHA256) != 64 {
		t.Fatalf("size/sha contract: %d %q", processed.ByteSize, processed.SHA256)
	}

	preview, err := processor.CreatePreview(pngBuffer.Bytes())
	if err != nil {
		t.Fatalf("create preview: %v", err)
	}
	if preview.MimeType != "image/webp" || preview.ByteSize > chatImagePreviewBytes {
		t.Fatalf("preview contract: mime=%q bytes=%d", preview.MimeType, preview.ByteSize)
	}
	if preview.Width > chatImagePreviewMaxEdge || preview.Height > chatImagePreviewMaxEdge {
		t.Fatalf("preview dims %dx%d exceed the %d edge", preview.Width, preview.Height, chatImagePreviewMaxEdge)
	}
}
