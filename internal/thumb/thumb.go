package thumb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/gif"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const maxDim = 320 // サムネ長辺(px)

// Generator はサムネ生成とディスクキャッシュを管理する。
type Generator struct {
	cacheDir   string
	sem        chan struct{} // 同時生成数の制限（CPUスパイク抑制）
	ffmpegPath string        // 空なら動画サムネ無効
}

func NewGenerator(cacheDir string, concurrency int) (*Generator, error) {
	dir := filepath.Join(cacheDir, "thumbs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("サムネキャッシュ作成に失敗: %w", err)
	}
	if concurrency < 1 {
		concurrency = 1
	}
	ffmpeg, _ := exec.LookPath("ffmpeg")
	return &Generator{
		cacheDir:   dir,
		sem:        make(chan struct{}, concurrency),
		ffmpegPath: ffmpeg,
	}, nil
}

// HasFFmpeg は動画サムネが利用可能かを返す。
func (g *Generator) HasFFmpeg() bool { return g.ffmpegPath != "" }

// FFmpegPath は検出した ffmpeg の実行パスを返す（無ければ空文字）。
func (g *Generator) FFmpegPath() string { return g.ffmpegPath }

// cacheKey は絶対パスと更新時刻からキャッシュファイル名を作る。
func (g *Generator) cachePath(absPath string, modTime time.Time) string {
	h := sha256.Sum256([]byte(absPath + "|" + strconv.FormatInt(modTime.UnixNano(), 10)))
	return filepath.Join(g.cacheDir, hex.EncodeToString(h[:16])+".jpg")
}

// Image は画像/動画のサムネJPEGのパスを返す（無ければ生成）。
// isVideo=true の場合 ffmpeg でフレーム抽出する。
func (g *Generator) Image(absPath string, isVideo bool) (string, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return "", err
	}
	out := g.cachePath(absPath, info.ModTime())
	if _, err := os.Stat(out); err == nil {
		return out, nil // キャッシュヒット
	}

	g.sem <- struct{}{}
	defer func() { <-g.sem }()

	// 競合での二重生成を避けるため再チェック
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}

	if isVideo {
		if g.ffmpegPath == "" {
			return "", fmt.Errorf("ffmpegが無いため動画サムネを生成できません")
		}
		return out, g.videoThumb(absPath, out)
	}
	return out, g.imageThumb(absPath, out)
}

func (g *Generator) imageThumb(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return err
	}
	return writeThumb(img, dst)
}

func (g *Generator) videoThumb(src, dst string) error {
	// ffmpeg の出力は一旦 tmp に書き、非空を確認できた時のみ rename で確定する。
	// avi/wmv 等で抽出が失敗・部分出力になっても、壊れたファイルをキャッシュに残さない。
	tmp := dst + ".tmp"
	defer os.Remove(tmp)

	// まず3秒地点の代表フレーム、ダメなら先頭フレームで再試行（短い動画対策）。
	for _, ss := range []string{"3", "0"} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		args := []string{
			"-ss", ss, "-i", src,
			"-frames:v", "1", "-an",
			"-vf", fmt.Sprintf("scale=%d:-1:force_original_aspect_ratio=decrease", maxDim),
			"-y", tmp,
		}
		err := exec.CommandContext(ctx, g.ffmpegPath, args...).Run()
		cancel()
		if err == nil {
			if fi, e := os.Stat(tmp); e == nil && fi.Size() > 0 {
				return os.Rename(tmp, dst) // アトミックに確定
			}
		}
	}
	return fmt.Errorf("ffmpegサムネ生成に失敗")
}

func writeThumb(img image.Image, dst string) error {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	nw, nh := scaledSize(w, h)
	dstImg := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dstImg, dstImg.Bounds(), img, b, draw.Over, nil)

	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := jpeg.Encode(f, dstImg, &jpeg.Options{Quality: 80}); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst) // アトミックに確定
}

func scaledSize(w, h int) (int, int) {
	if w <= maxDim && h <= maxDim {
		return w, h
	}
	if w >= h {
		return maxDim, int(float64(h) * float64(maxDim) / float64(w))
	}
	return int(float64(w) * float64(maxDim) / float64(h)), maxDim
}
