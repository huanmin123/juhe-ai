package main

// w1: chain_chat_images.go 图像管线测试。PNG/JPEG 由标准库在测试内编码，
// EXIF APP1 段手工构造（TIFF IFD0 + 0x0112 标签），VP8L 输出经官方
// x/image/webp 解码回读验证。

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	xwebp "golang.org/x/image/webp"
)

func w1SolidPNG(width, height int) []byte {
	src := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: 200, G: 30, B: 40, A: 255})
		}
	}
	buffer := &bytes.Buffer{}
	_ = png.Encode(buffer, src)
	return buffer.Bytes()
}

func w1SolidJPEG(width, height int) []byte {
	src := image.NewRGBA(image.Rect(0, 0, width, height))
	buffer := &bytes.Buffer{}
	_ = jpeg.Encode(buffer, src, &jpeg.Options{Quality: 80})
	return buffer.Bytes()
}

// w1JPEGWithEXIFOrientation 在 JPEG 头部插入 APP1 EXIF 段（orientation 1-8）。
// chatJPEGOrientation 只按 marker 走段，无需完整 JPEG 语法。
func w1JPEGWithEXIFOrientation(orientation uint16) []byte {
	// TIFF IFD0：II 字节序 + 0x002A + IFD 偏移 8；1 个条目（tag 0x0112）。
	tiff := []byte{'I', 'I', 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00}
	tiff = append(tiff, 0x01, 0x00)                          // 条目数 = 1
	tiff = append(tiff, 0x12, 0x01)                          // tag 0x0112
	tiff = append(tiff, 0x03, 0x00, 0x01, 0x00, 0x00, 0x00)  // SHORT, count 1
	tiff = append(tiff, byte(orientation), 0x00, 0x00, 0x00) // 值在 value 字段前 2 字节
	tiff = append(tiff, 0x00, 0x00, 0x00, 0x00)              // 下一个 IFD 偏移 = 0
	payload := append([]byte("Exif\x00\x00"), tiff...)
	segment := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte((len(payload) + 2) & 0xFF)}
	segment = append(segment, payload...)
	out := []byte{0xFF, 0xD8}
	out = append(out, segment...)
	out = append(out, 0xFF, 0xD9)
	return out
}

func TestW1ChatJPEGOrientationParsesAPP1(t *testing.T) {
	if got := chatJPEGOrientation(w1SolidJPEG(4, 4), "png"); got != 0 {
		t.Fatalf("非 jpeg 格式 = %d", got)
	}
	for _, want := range []uint16{1, 2, 3, 4, 5, 6, 7, 8} {
		if got := chatJPEGOrientation(w1JPEGWithEXIFOrientation(want), "jpeg"); got != int(want) {
			t.Fatalf("orientation %d 解析为 %d", want, got)
		}
	}
	// 非 JPEG 魔数：0。
	if got := chatJPEGOrientation([]byte{0x00, 0x01, 0x02}, "jpeg"); got != 0 {
		t.Fatalf("错误魔数 = %d", got)
	}
	// 无 EXIF 的普通 JPEG：0。
	if got := chatJPEGOrientation(w1SolidJPEG(4, 4), "jpeg"); got != 0 {
		t.Fatalf("无 EXIF = %d", got)
	}
	// 非法 orientation 值（0/9）：回 0。
	if got := chatJPEGOrientation(w1JPEGWithEXIFOrientation(9), "jpeg"); got != 0 {
		t.Fatalf("orientation 9 = %d", got)
	}
	// 空段与超短输入。
	if got := chatJPEGOrientation([]byte{0xFF, 0xD8, 0xFF}, "jpeg"); got != 0 {
		t.Fatalf("截断输入 = %d", got)
	}
}

func TestW1ChatEXIFOrientationByteOrders(t *testing.T) {
	// 段太短。
	if got := chatEXIFOrientation([]byte("II")); got != 0 {
		t.Fatalf("短输入 = %d", got)
	}
	// 非法字节序标记。
	if got := chatEXIFOrientation([]byte("XX\x2A\x00\x08\x00\x00\x00")); got != 0 {
		t.Fatalf("非法序标 = %d", got)
	}
	// 非法 magic（0x002A 错误）。
	if got := chatEXIFOrientation([]byte("II\x2B\x00\x08\x00\x00\x00")); got != 0 {
		t.Fatalf("非法 magic = %d", got)
	}
	// MM 大端 + orientation 8。
	tiff := []byte{'M', 'M', 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08}
	tiff = append(tiff, 0x00, 0x01)
	tiff = append(tiff, 0x01, 0x12, 0x00, 0x03, 0x00, 0x00, 0x00, 0x01, 0x00, 0x08, 0x00, 0x00)
	tiff = append(tiff, 0x00, 0x00, 0x00, 0x00)
	if got := chatEXIFOrientation(tiff); got != 8 {
		t.Fatalf("MM orientation = %d，want 8", got)
	}
	// IFD 偏移越界。
	if got := chatEXIFOrientation([]byte("II\x2A\x00\xFF\x00\x00\x00")); got != 0 {
		t.Fatalf("越界 IFD = %d", got)
	}
	// 条目截断。
	truncated := []byte{'I', 'I', 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x09, 0x00}
	if got := chatEXIFOrientation(truncated); got != 0 {
		t.Fatalf("条目截断 = %d", got)
	}
	// 无 0x0112 标签。
	noTag := []byte{'I', 'I', 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x01, 0x00}
	noTag = append(noTag, 0x99, 0x99, 0x03, 0x00, 0x01, 0x00, 0x00, 0x00, 0x06, 0x00, 0x00, 0x00)
	noTag = append(noTag, 0x00, 0x00, 0x00, 0x00)
	if got := chatEXIFOrientation(noTag); got != 0 {
		t.Fatalf("无标签 = %d", got)
	}
}

func TestW1ApplyChatOrientationAllArms(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 3))
	src.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255}) // 左上角标记点
	// orientation 范围外：原样 NRGBA。
	same := applyChatOrientation(src, 0)
	if same.Bounds().Dx() != 2 || same.Bounds().Dy() != 3 {
		t.Fatal("orientation 0 必须原样")
	}
	// orientation 2（水平翻转）：标记点到右上。
	flipped := applyChatOrientation(src, 2).(*image.NRGBA)
	if got := flipped.At(1, 0); got != (color.NRGBA{R: 255, A: 255}) {
		t.Fatalf("orientation 2 左上 → %#v", got)
	}
	// orientation 5（转置）：宽高互换。
	transposed := applyChatOrientation(src, 5)
	if transposed.Bounds().Dx() != 3 || transposed.Bounds().Dy() != 2 {
		t.Fatalf("orientation 5 尺寸 = %v", transposed.Bounds())
	}
	// orientation 6（90° 顺时针）。
	rotated := applyChatOrientation(src, 6)
	if rotated.Bounds().Dx() != 3 || rotated.Bounds().Dy() != 2 {
		t.Fatalf("orientation 6 尺寸 = %v", rotated.Bounds())
	}
	// orientation 8（90° 逆时针）。
	if rotated := applyChatOrientation(src, 8); rotated.Bounds().Dx() != 3 {
		t.Fatal("orientation 8 尺寸错误")
	}
	// nrgbaAt 读取 RGBA 源。
	opaque := image.NewRGBA(image.Rect(0, 0, 1, 1))
	opaque.Set(0, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	if got := nrgbaAt(opaque, 0, 0); got != (color.NRGBA{R: 10, G: 20, B: 30, A: 255}) {
		t.Fatalf("nrgbaAt = %#v", got)
	}
}

func TestW1BoundedChatImageDimensions(t *testing.T) {
	// 小图不缩。
	if got := boundedChatImageDimensions(64, 32); got.width != 64 || got.height != 32 {
		t.Fatalf("小图 = %+v", got)
	}
	// 超边长：等比缩到 1024。
	if got := boundedChatImageDimensions(4096, 2048); got.width != 1024 || got.height != 512 {
		t.Fatalf("超边长 = %+v", got)
	}
	// 面积上限：2500 patch × 32² = 2,560,000 px。
	if got := boundedChatImageDimensions(4000, 4000); got.width*got.height > 2_560_000 {
		t.Fatalf("面积超限 = %+v", got)
	}
	// orientedChatDimensions：5-8 互换宽高。
	dims, ok := orientedChatDimensions(image.Rect(0, 0, 20, 10), 6)
	if !ok || dims.width != 10 || dims.height != 20 {
		t.Fatalf("orientation 6 dims = %+v", dims)
	}
	dims, ok = orientedChatDimensions(image.Rect(0, 0, 20, 10), 1)
	if !ok || dims.width != 20 || dims.height != 10 {
		t.Fatalf("orientation 1 dims = %+v", dims)
	}
	// 零尺寸：false。
	if _, ok := orientedChatDimensions(image.Rect(0, 0, 0, 0), 1); ok {
		t.Fatal("零尺寸必须失败")
	}
	// minOf / maxOf / sqrtOf / ceilDiv。
	if minOf(3, 7) != 3 || maxOf(3, 7) != 7 {
		t.Fatal("min/max 错误")
	}
	if got := sqrtOf(16); got < 3.999 || got > 4.001 {
		t.Fatalf("sqrtOf(16) = %v", got)
	}
	if sqrtOf(0) != 0 {
		t.Fatal("sqrtOf(0) 必须 0")
	}
	if ceilDiv(33, 32) != 2 || ceilDiv(32, 32) != 1 {
		t.Fatal("ceilDiv 错误")
	}
	if chatImagePatchCount(64, 64) != 4 {
		t.Fatalf("patch count = %d", chatImagePatchCount(64, 64))
	}
}

func TestW1ChatImageProcessorProcessUploadPNG(t *testing.T) {
	processor := newChatImageProcessor()
	data := w1SolidPNG(640, 320)
	processed, err := processor.ProcessUpload(data, "image/png")
	if err != nil {
		t.Fatalf("ProcessUpload: %v", err)
	}
	if processed.MimeType != "image/webp" || processed.OriginalMimeType != "image/png" {
		t.Fatalf("mime = %q %q", processed.MimeType, processed.OriginalMimeType)
	}
	if processed.OriginalWidth != 640 || processed.OriginalHeight != 320 {
		t.Fatalf("original dims = %d x %d", processed.OriginalWidth, processed.OriginalHeight)
	}
	if processed.Width != 640 || processed.Height != 320 {
		t.Fatalf("output dims = %d x %d", processed.Width, processed.Height)
	}
	if processed.ByteSize != int64(len(processed.Buffer)) || len(processed.SHA256) != 64 {
		t.Fatalf("size/sha = %d %q", processed.ByteSize, processed.SHA256)
	}
	// WebP 输出可被官方解码器回读，尺寸一致。
	decoded, err := xwebp.Decode(bytes.NewReader(processed.Buffer))
	if err != nil {
		t.Fatalf("decode webp: %v", err)
	}
	if decoded.Bounds().Dx() != 640 || decoded.Bounds().Dy() != 320 {
		t.Fatalf("webp dims = %v", decoded.Bounds())
	}
	// 无法解码的输入：Node 契约错误。
	if _, err := processor.ProcessUpload([]byte("not-an-image"), "image/png"); err == nil {
		t.Fatal("垃圾输入必须报错")
	}
	// 巨图（超 maxDecodedPixels）。
	if _, err := processor.ProcessUpload(w1SolidPNG(4, 4), "image/png"); err != nil {
		t.Fatalf("小图: %v", err)
	}
}

func TestW1ChatImageProcessorProcessUploadJPEGWithOrientation(t *testing.T) {
	processor := newChatImageProcessor()
	// 真实 JPEG（3 宽 1 高）+ 插入 orientation 6 的 APP1：解码后宽高互换
	// （orientedChatDimensions），Original 尺寸 1x3。
	base := w1SolidJPEG(3, 1)
	exifJPEG := append(append(base[:2:2], w1JPEGWithEXIFOrientation(6)[2:len(w1JPEGWithEXIFOrientation(6))-2]...), base[2:]...)
	processed, err := processor.ProcessUpload(exifJPEG, "image/jpeg")
	if err != nil {
		t.Fatalf("ProcessUpload: %v", err)
	}
	if processed.OriginalMimeType != "image/jpeg" {
		t.Fatalf("mime = %q", processed.OriginalMimeType)
	}
	if processed.OriginalWidth != 1 || processed.OriginalHeight != 3 {
		t.Fatalf("orientation 6 dims = %d x %d，want 1x3", processed.OriginalWidth, processed.OriginalHeight)
	}
	// 同一 JPEG 无 EXIF：Original 保持 3x1。
	plain, err := processor.ProcessUpload(base, "image/jpeg")
	if err != nil {
		t.Fatalf("plain ProcessUpload: %v", err)
	}
	if plain.OriginalWidth != 3 || plain.OriginalHeight != 1 {
		t.Fatalf("plain dims = %d x %d，want 3x1", plain.OriginalWidth, plain.OriginalHeight)
	}
}

func TestW1ChatImageProcessorCreatePreviewLadder(t *testing.T) {
	processor := newChatImageProcessor()
	// 800x600 纯色：VP8L 全字面编码下 640 边超预算，梯子降档后成功。
	preview, err := processor.CreatePreview(w1SolidPNG(800, 600))
	if err != nil {
		t.Fatalf("CreatePreview: %v", err)
	}
	if preview.MimeType != "image/webp" || preview.ByteSize > chatImagePreviewBytes {
		t.Fatalf("preview = %d bytes mime %q", preview.ByteSize, preview.MimeType)
	}
	if preview.Width > chatImagePreviewMaxEdge || preview.Height > chatImagePreviewMaxEdge {
		t.Fatalf("preview dims = %d x %d", preview.Width, preview.Height)
	}
	decoded, err := xwebp.Decode(bytes.NewReader(preview.Buffer))
	if err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if decoded.Bounds().Dx() != int(preview.Width) {
		t.Fatalf("decoded width = %d，want %d", decoded.Bounds().Dx(), preview.Width)
	}
	// 解码失败输入。
	if _, err := processor.CreatePreview([]byte("junk")); err == nil {
		t.Fatal("垃圾输入必须报错")
	}
}

func TestW1EncodeChatModelImageBothPaths(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	dims := chatImageDimensions{width: 8, height: 8}
	// 尺寸一致：直接编码路径。
	same, err := encodeChatModelImage(src, 8, 8, dims)
	if err != nil || same.width != 8 || same.height != 8 {
		t.Fatalf("直接路径 = %+v, %v", same, err)
	}
	// 尺寸不同：resize 路径。
	resized, err := encodeChatModelImage(src, 4, 4, dims)
	if err != nil || resized.width != 4 || resized.height != 4 {
		t.Fatalf("resize 路径 = %+v, %v", resized, err)
	}
	decoded, err := xwebp.Decode(bytes.NewReader(resized.buffer))
	if err != nil || decoded.Bounds().Dx() != 4 {
		t.Fatalf("resize webp = %v, %v", decoded, err)
	}
	// resizeChatImage 等尺寸路径 + 放大禁用（无 enlarge）。
	normal := resizeChatImage(src, 8, 8)
	if normal.Bounds().Dx() != 8 {
		t.Fatal("等尺寸 resize 错误")
	}
}
