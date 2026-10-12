package api

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// allowedImageTypes 允许的图片类型。
//
// 键是「真实字节嗅探出来的 MIME」，不是客户端声明的 Content-Type ——
// 后者可以随便伪造。值是对应的扩展名。
var allowedImageTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

// maxImageDimension 像素上限。
// 16688 那边的限制是宽或高超过 8000 像素会拒绝，
// 这里统一按 8000 拦，避免传到上游才发现不行。
const maxImageDimension = 8000

// savedImage 一次成功保存的图片信息
type savedImage struct {
	Name        string
	ContentType string
	Width       int
	Height      int
	Size        int64
}

// uploadImage 上传商品图片（仅管理员）。
//
// 校验逻辑都放在 saveImage 里，这里只负责取文件、调保存、写审计。
func (h *Handlers) uploadImage(c *gin.Context) {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		BadRequest(c, "请选择要上传的图片（表单字段名应为 file）")
		return
	}

	src, err := fileHeader.Open()
	if err != nil {
		ServerError(c, "读取上传文件失败")
		return
	}
	defer src.Close()

	saved, err := saveImage(src, fileHeader.Size, h.cfg.Upload.Dir, h.cfg.Upload.MaxSizeMB)
	if err != nil {
		// 校验类错误回 400，IO 类错误回 500
		var ve *validationError
		if errors.As(err, &ve) {
			BadRequest(c, ve.Error())
			return
		}
		slog.Error("保存上传图片失败", "dir", h.cfg.Upload.Dir, "error", err.Error())
		ServerError(c, "保存文件失败")
		return
	}

	url := strings.TrimRight(h.cfg.Upload.URLPrefix, "/") + "/" + saved.Name
	slog.Info("上传图片成功",
		"name", saved.Name, "size", saved.Size, "type", saved.ContentType,
		"dimension", fmt.Sprintf("%dx%d", saved.Width, saved.Height))

	h.auditLog(c, "upload.image", "upload", saved.Name, gin.H{
		"size": saved.Size, "type": saved.ContentType,
		"width": saved.Width, "height": saved.Height,
	})

	OK(c, gin.H{
		"url":         url,
		"name":        saved.Name,
		"size":        saved.Size,
		"type":        saved.ContentType,
		"width":       saved.Width,
		"height":      saved.Height,
		"max_size_mb": h.cfg.Upload.MaxSizeMB,
	})
}

// validationError 表示「调用方的问题」，对应 HTTP 400。
// 与 IO 故障区分开，便于 handler 决定状态码。
type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &validationError{msg: fmt.Sprintf(format, args...)}
}

// saveImage 校验并保存一张图片。
//
// 几个刻意的设计：
//
//  1. **不信客户端的 Content-Type 与文件名**：类型靠读字节嗅探，
//     文件名用 UUID 重新生成。这样既防了伪装成图片的可执行文件，
//     也杜绝了用 ../../ 之类的文件名做路径穿越。
//  2. **先写临时文件再原子改名**：中途失败不会留下半个损坏的图片，
//     并发也不会读到写了一半的文件。
//  3. **校验真实像素尺寸**：超大图会拖慢买家页面，也会被部分上游拒绝。
//
// 实现上先把整张图读进内存再处理。这一点是刻意的：
// 早期版本为了嗅探类型先读了 512 字节，结果保存时从那之后开始写，
// 导致落盘文件**丢掉开头 512 字节**（图片损坏）。
// 上限只有几 MB，整读进内存最简单也最不容易出错。
func saveImage(src io.Reader, declaredSize int64, dir string, maxMB int) (*savedImage, error) {
	if maxMB < 1 {
		maxMB = 5
	}
	maxBytes := int64(maxMB) * 1024 * 1024

	if declaredSize == 0 {
		return nil, invalid("文件为空")
	}
	if declaredSize > maxBytes {
		return nil, invalid("图片不能超过 %d MB，当前 %.1f MB",
			maxMB, float64(declaredSize)/1024/1024)
	}

	// ---- 整读进内存 ----
	// 多读 1 字节：能读到说明实际内容超过上限（声明的大小不可信）
	raw, err := io.ReadAll(io.LimitReader(src, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取上传内容失败: %w", err)
	}
	if len(raw) == 0 {
		return nil, invalid("文件为空")
	}
	if int64(len(raw)) > maxBytes {
		return nil, invalid("图片不能超过 %d MB", maxMB)
	}

	// ---- 嗅探真实类型 ----
	contentType := http.DetectContentType(raw)
	// DetectContentType 对 webp 常返回 application/octet-stream，补一层兜底
	if contentType == "application/octet-stream" && isWebP(raw) {
		contentType = "image/webp"
	}
	ext, ok := allowedImageTypes[contentType]
	if !ok {
		return nil, invalid("只支持 JPG / PNG / WebP / GIF 格式的图片")
	}

	// ---- 校验像素尺寸 ----
	width, height, derr := imageSize(raw)
	if derr != nil {
		// 解析不出尺寸：多半是改了扩展名的伪装文件
		return nil, invalid("图片文件已损坏或格式不正确")
	}
	if width <= 0 || height <= 0 {
		return nil, invalid("图片尺寸非法")
	}
	if width > maxImageDimension || height > maxImageDimension {
		return nil, invalid("图片尺寸不能超过 %d 像素，当前 %dx%d",
			maxImageDimension, width, height)
	}

	// ---- 落盘 ----
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建上传目录失败: %w", err)
	}

	// 文件名完全由服务端生成，不采用客户端传来的名字
	name := uuid.NewString() + ext
	finalPath := filepath.Join(dir, name)
	tmpPath := finalPath + ".part"

	if err := writeFileAtomic(tmpPath, finalPath, raw); err != nil {
		return nil, err
	}

	return &savedImage{
		Name:        name,
		ContentType: contentType,
		Width:       width,
		Height:      height,
		Size:        int64(len(raw)),
	}, nil
}

// writeFileAtomic 先写临时文件再改名，保证读到的永远是完整文件。
func writeFileAtomic(tmpPath, finalPath string, data []byte) error {
	dst, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("创建文件失败: %w", err)
	}
	if _, err := dst.Write(data); err != nil {
		dst.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("写入文件失败: %w", err)
	}
	if err := dst.Sync(); err != nil {
		dst.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("刷盘失败: %w", err)
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("关闭文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("重命名上传文件失败: %w", err)
	}
	return nil
}

// isWebP 判断字节流是否是 WebP（RIFF....WEBP）
func isWebP(b []byte) bool {
	return len(b) >= 12 &&
		string(b[0:4]) == "RIFF" &&
		string(b[8:12]) == "WEBP"
}

// imageSize 解析图片的真实宽高。
//
// 只实现我们允许的四种格式，不引第三方图像库 ——
// 这里只需要读文件头，几十行足够，少一个依赖少一份风险。
// 传入完整的文件内容（上限几 MB），JPEG 的尺寸段位置不固定，
// 有完整 buffer 才能稳定扫描。
func imageSize(b []byte) (int, int, error) {
	switch {
	case len(b) >= 8 && string(b[0:8]) == "\x89PNG\r\n\x1a\n":
		return pngSize(b)
	case len(b) >= 4 && b[0] == 0xFF && b[1] == 0xD8:
		return jpegSize(b)
	case isWebP(b):
		return webpSize(b)
	case len(b) >= 6 && (string(b[0:6]) == "GIF87a" || string(b[0:6]) == "GIF89a"):
		return gifSize(b)
	}
	return 0, 0, errors.New("未知图片格式")
}

// pngSize 从 IHDR 读宽高（固定偏移 16..24，大端）
func pngSize(b []byte) (int, int, error) {
	if len(b) < 24 {
		return 0, 0, errors.New("png 头部不足")
	}
	w := int(uint32(b[16])<<24 | uint32(b[17])<<16 | uint32(b[18])<<8 | uint32(b[19]))
	h := int(uint32(b[20])<<24 | uint32(b[21])<<16 | uint32(b[22])<<8 | uint32(b[23]))
	return w, h, nil
}

// gifSize 从逻辑屏幕描述符读宽高（小端，偏移 6..10）
func gifSize(b []byte) (int, int, error) {
	if len(b) < 10 {
		return 0, 0, errors.New("gif 头部不足")
	}
	w := int(b[6]) | int(b[7])<<8
	h := int(b[8]) | int(b[9])<<8
	return w, h, nil
}

// webpSize 支持 VP8 / VP8L / VP8X 三种子格式
func webpSize(b []byte) (int, int, error) {
	if len(b) < 30 {
		return 0, 0, errors.New("webp 头部不足")
	}
	switch string(b[12:16]) {
	case "VP8 ":
		w := int(b[26]) | int(b[27])<<8
		h := int(b[28]) | int(b[29])<<8
		return w & 0x3FFF, h & 0x3FFF, nil
	case "VP8L":
		bits := uint32(b[21]) | uint32(b[22])<<8 | uint32(b[23])<<16 | uint32(b[24])<<24
		return int(bits&0x3FFF) + 1, int((bits>>14)&0x3FFF) + 1, nil
	case "VP8X":
		w := int(uint32(b[24])|uint32(b[25])<<8|uint32(b[26])<<16) + 1
		h := int(uint32(b[27])|uint32(b[28])<<8|uint32(b[29])<<16) + 1
		return w, h, nil
	}
	return 0, 0, errors.New("未知 webp 子格式")
}

// jpegSize 扫描 SOF 段取宽高。
//
// JPEG 是 [0xFF 标记][2字节长度][内容] 的段结构，需要逐个跳过，
// 直到遇到 SOF0~SOF3/SOF5~SOF7/SOF9~SOF11 中任意一个。
func jpegSize(b []byte) (int, int, error) {
	i := 2 // 跳过 SOI
	for i+9 < len(b) {
		if b[i] != 0xFF {
			i++
			continue
		}
		marker := b[i+1]
		if marker == 0xFF { // 填充字节
			i++
			continue
		}
		// 无长度字段的标记
		if marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		segLen := int(b[i+2])<<8 | int(b[i+3])
		if segLen < 2 {
			return 0, 0, errors.New("jpeg 段长度非法")
		}
		// SOF0..SOF15，排除 DHT(C4) / JPG(C8) / DAC(CC)
		isSOF := marker >= 0xC0 && marker <= 0xCF &&
			marker != 0xC4 && marker != 0xC8 && marker != 0xCC
		if isSOF {
			h := int(b[i+5])<<8 | int(b[i+6])
			w := int(b[i+7])<<8 | int(b[i+8])
			return w, h, nil
		}
		i += 2 + segLen
	}
	return 0, 0, errors.New("jpeg 未找到 SOF 段")
}
