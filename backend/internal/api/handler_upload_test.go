package api

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 图片上传的测试。
//
// 重点是安全边界：伪造类型、超大图、路径穿越这三类问题
// 一旦漏掉就是真实风险，所以每条都固定成用例。

// ---------------------------------------------------------------- 构造测试图片

// makePNG 造一个头部合法的 PNG（只够读出尺寸，不含真实像素数据）
func makePNG(w, h uint32) []byte {
	var b bytes.Buffer
	b.Write([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A})
	b.Write([]byte{0, 0, 0, 13}) // IHDR 长度
	b.Write([]byte("IHDR"))      // 类型
	binary.Write(&b, binary.BigEndian, w)
	binary.Write(&b, binary.BigEndian, h)
	b.Write([]byte{8, 6, 0, 0, 0}) // 位深/颜色类型等
	b.Write([]byte{0, 0, 0, 0})    // CRC 占位
	return b.Bytes()
}

// makeGIF 造一个头部合法的 GIF
func makeGIF(w, h uint16) []byte {
	var b bytes.Buffer
	b.WriteString("GIF89a")
	binary.Write(&b, binary.LittleEndian, w)
	binary.Write(&b, binary.LittleEndian, h)
	b.Write([]byte{0, 0, 0})
	return b.Bytes()
}

// makeWebPVP8X 造一个 VP8X 的 WebP 头
func makeWebPVP8X(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(30))
	b.WriteString("WEBP")
	b.WriteString("VP8X")
	binary.Write(&b, binary.LittleEndian, uint32(10))
	b.Write([]byte{0, 0, 0, 0}) // flags + reserved
	// 24 位宽高，减一存储
	b.Write([]byte{byte(w - 1), byte((w - 1) >> 8), byte((w - 1) >> 16)})
	b.Write([]byte{byte(h - 1), byte((h - 1) >> 8), byte((h - 1) >> 16)})
	return b.Bytes()
}

// makeJPEG 造一个带 SOF0 段的 JPEG
func makeJPEG(w, h uint16) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8}) // SOI
	// APP0 段（长度 16，内容随意）—— 用来验证扫描能正确跳过
	b.Write([]byte{0xFF, 0xE0, 0x00, 0x10})
	b.Write(make([]byte, 14))
	// SOF0：长度 17，精度 8，高 2 字节，宽 2 字节
	b.Write([]byte{0xFF, 0xC0, 0x00, 0x11, 0x08})
	binary.Write(&b, binary.BigEndian, h)
	binary.Write(&b, binary.BigEndian, w)
	b.Write([]byte{3, 1, 0x22, 0, 2, 0x11, 1, 3, 0x11, 1})
	return b.Bytes()
}

// ---------------------------------------------------------------- 尺寸解析

func TestImageSizePNG(t *testing.T) {
	w, h, err := imageSize(makePNG(640, 480))
	if err != nil {
		t.Fatal(err)
	}
	if w != 640 || h != 480 {
		t.Errorf("PNG 尺寸应为 640x480，got=%dx%d", w, h)
	}
}

func TestImageSizeGIF(t *testing.T) {
	w, h, err := imageSize(makeGIF(320, 200))
	if err != nil {
		t.Fatal(err)
	}
	if w != 320 || h != 200 {
		t.Errorf("GIF 尺寸应为 320x200，got=%dx%d", w, h)
	}
}

func TestImageSizeWebP(t *testing.T) {
	w, h, err := imageSize(makeWebPVP8X(800, 600))
	if err != nil {
		t.Fatal(err)
	}
	if w != 800 || h != 600 {
		t.Errorf("WebP 尺寸应为 800x600，got=%dx%d", w, h)
	}
}

func TestImageSizeJPEG(t *testing.T) {
	w, h, err := imageSize(makeJPEG(1024, 768))
	if err != nil {
		t.Fatal(err)
	}
	if w != 1024 || h != 768 {
		t.Errorf("JPEG 尺寸应为 1024x768，got=%dx%d", w, h)
	}
}

// 非图片内容应解析失败
func TestImageSizeRejectsGarbage(t *testing.T) {
	if _, _, err := imageSize([]byte("this is definitely not an image at all")); err == nil {
		t.Error("非图片内容应报错")
	}
}

// ---------------------------------------------------------------- 保存流程

func tmpUploadDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// 这是最关键的用例：落盘内容必须与上传内容**逐字节一致**。
// 早期实现为了嗅探类型先读了 512 字节，保存时从那之后开始写，
// 导致文件丢掉开头 512 字节（图片损坏）。这条用例就是为了锁住它。
func TestSaveImageSuccess(t *testing.T) {
	dir := tmpUploadDir(t)
	data := makePNG(100, 100)

	saved, err := saveImage(bytes.NewReader(data), int64(len(data)), dir, 5)
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	if saved.ContentType != "image/png" {
		t.Errorf("类型应为 image/png，got=%s", saved.ContentType)
	}
	if saved.Width != 100 || saved.Height != 100 {
		t.Errorf("尺寸应为 100x100，got=%dx%d", saved.Width, saved.Height)
	}
	if !strings.HasSuffix(saved.Name, ".png") {
		t.Errorf("文件名应以 .png 结尾，got=%s", saved.Name)
	}
	if saved.Size != int64(len(data)) {
		t.Errorf("记录大小应为 %d，got=%d", len(data), saved.Size)
	}

	got, err := os.ReadFile(filepath.Join(dir, saved.Name))
	if err != nil {
		t.Fatalf("读取落盘文件失败: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("落盘内容与上传内容不一致：上传 %d 字节，落盘 %d 字节（文件被截断）",
			len(data), len(got))
	}

	// 不应残留 .part 临时文件
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".part") {
			t.Errorf("残留临时文件: %s", e.Name())
		}
	}
}

// 大文件也要完整落盘（覆盖跨越多字节的读写路径）
func TestSaveImageLargeFileIntact(t *testing.T) {
	dir := tmpUploadDir(t)

	// 造一个 2MB 的 PNG：头部合法 + 大量填充
	head := makePNG(100, 100)
	payload := make([]byte, 2*1024*1024)
	copy(payload, head)
	// 填充内容带规律，便于发现错位
	for i := len(head); i < len(payload); i++ {
		payload[i] = byte(i % 251)
	}

	saved, err := saveImage(bytes.NewReader(payload), int64(len(payload)), dir, 5)
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, saved.Name))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("大文件落盘内容不一致：上传 %d，落盘 %d", len(payload), len(got))
	}
}

// 文件名必须由服务端生成，不能用客户端传来的（防路径穿越）
func TestSaveImageIgnoresClientFilename(t *testing.T) {
	dir := tmpUploadDir(t)
	data := makePNG(10, 10)

	saved, err := saveImage(bytes.NewReader(data), int64(len(data)), dir, 5)
	if err != nil {
		t.Fatal(err)
	}

	base := strings.TrimSuffix(saved.Name, ".png")
	if len(base) != 36 || strings.Count(base, "-") != 4 {
		t.Errorf("文件名应是 UUID，got=%s", saved.Name)
	}
	if strings.ContainsAny(saved.Name, "/\\") {
		t.Errorf("文件名不应含路径分隔符，got=%s", saved.Name)
	}
}

// 伪装成图片的文本文件必须被拒（最常见的一类上传攻击）
func TestSaveImageRejectsFakeImage(t *testing.T) {
	dir := tmpUploadDir(t)
	fake := []byte("#!/bin/sh\necho pwned\n")

	_, err := saveImage(bytes.NewReader(fake), int64(len(fake)), dir, 5)
	if err == nil {
		t.Fatal("伪装成图片的文件必须被拒绝")
	}
	var ve *validationError
	if !asValidation(err, &ve) {
		t.Errorf("应返回校验错误（400），got=%v", err)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("被拒的上传不应落盘，实际有 %d 个文件", len(entries))
	}
}

// 声明大小撒谎也要拦住（实际内容超限）
func TestSaveImageRejectsOversize(t *testing.T) {
	dir := tmpUploadDir(t)

	// 声明 10MB，上限 1MB
	if _, err := saveImage(bytes.NewReader(makePNG(10, 10)), 10*1024*1024, dir, 1); err == nil {
		t.Fatal("声明超限应被拒绝")
	}

	// 声明很小，但实际内容 2MB
	big := make([]byte, 2*1024*1024)
	copy(big, makePNG(10, 10))
	if _, err := saveImage(bytes.NewReader(big), 1024, dir, 1); err == nil {
		t.Fatal("实际内容超限也应被拒绝")
	}
}

// 超过像素上限必须被拒（上游 8000 像素限制）
func TestSaveImageRejectsHugeDimension(t *testing.T) {
	dir := tmpUploadDir(t)
	data := makePNG(9000, 100)

	_, err := saveImage(bytes.NewReader(data), int64(len(data)), dir, 5)
	if err == nil {
		t.Fatal("超过 8000 像素应被拒绝")
	}
	if !strings.Contains(err.Error(), "8000") {
		t.Errorf("错误信息应说明像素上限，got=%v", err)
	}
}

// 边界：正好 8000 像素应放行
func TestSaveImageAcceptsExactlyMaxDimension(t *testing.T) {
	dir := tmpUploadDir(t)
	data := makePNG(8000, 8000)

	if _, err := saveImage(bytes.NewReader(data), int64(len(data)), dir, 5); err != nil {
		t.Errorf("正好 8000 像素应放行，got=%v", err)
	}
}

func TestSaveImageRejectsEmpty(t *testing.T) {
	dir := tmpUploadDir(t)
	if _, err := saveImage(bytes.NewReader(nil), 0, dir, 5); err == nil {
		t.Error("空文件应被拒绝")
	}
}

func TestSaveImageAllFormats(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
		ext  string
	}{
		{"png", makePNG(50, 50), "image/png", ".png"},
		{"gif", makeGIF(50, 50), "image/gif", ".gif"},
		{"jpeg", makeJPEG(50, 50), "image/jpeg", ".jpg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := tmpUploadDir(t)
			saved, err := saveImage(bytes.NewReader(c.data), int64(len(c.data)), dir, 5)
			if err != nil {
				t.Fatalf("保存 %s 失败: %v", c.name, err)
			}
			if saved.ContentType != c.want {
				t.Errorf("类型应为 %s，got=%s", c.want, saved.ContentType)
			}
			if !strings.HasSuffix(saved.Name, c.ext) {
				t.Errorf("扩展名应为 %s，got=%s", c.ext, saved.Name)
			}
			// 各格式都要验证内容完整
			got, _ := os.ReadFile(filepath.Join(dir, saved.Name))
			if !bytes.Equal(got, c.data) {
				t.Errorf("%s 落盘内容不一致", c.name)
			}
		})
	}
}

// 目录不存在时应自动创建
func TestSaveImageCreatesDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "nested", "uploads")
	data := makePNG(10, 10)

	if _, err := saveImage(bytes.NewReader(data), int64(len(data)), dir, 5); err != nil {
		t.Fatalf("应自动创建目录，got=%v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Error("目录应已创建")
	}
}

// 连续上传应得到不同文件名（不会互相覆盖）
func TestSaveImageUniqueNames(t *testing.T) {
	dir := tmpUploadDir(t)
	seen := map[string]bool{}
	data := makePNG(10, 10)

	for i := 0; i < 20; i++ {
		saved, err := saveImage(bytes.NewReader(data), int64(len(data)), dir, 5)
		if err != nil {
			t.Fatal(err)
		}
		if seen[saved.Name] {
			t.Fatalf("文件名重复: %s", saved.Name)
		}
		seen[saved.Name] = true
	}
}

func TestIsWebP(t *testing.T) {
	if !isWebP(makeWebPVP8X(10, 10)) {
		t.Error("应识别为 WebP")
	}
	if isWebP(makePNG(10, 10)) {
		t.Error("PNG 不应被识别为 WebP")
	}
}

// asValidation 判断错误是否为校验类
func asValidation(err error, target **validationError) bool {
	if ve, ok := err.(*validationError); ok {
		*target = ve
		return true
	}
	return false
}
