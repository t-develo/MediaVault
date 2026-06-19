package server

import (
	"io"
	"net/http"
	"os/exec"
)

// handleTranscode はブラウザ非対応形式（avi / wmv / mkv など）を
// ffmpeg で H.264/AAC の fragmented MP4 へ都度変換し、ストリーミング配信する。
//
// ライブ変換のため Range（シーク）には対応しない。先頭から順次再生する想定で、
// ラズパイの負荷を考え preset は veryfast、解像度は最大 1280px 幅に抑える。
func (s *Server) handleTranscode(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	abs, err := s.lib.Resolve(p)
	if err != nil {
		s.mediaError(w, err)
		return
	}
	if !s.lib.IsVideo(abs) {
		http.Error(w, "動画ではありません", http.StatusBadRequest)
		return
	}
	ffmpeg := s.thumb.FFmpegPath()
	if ffmpeg == "" {
		http.Error(w, "この形式の再生には ffmpeg が必要です", http.StatusNotImplemented)
		return
	}

	// クライアント切断（再生停止・ページ離脱）で ffmpeg も止めるためコンテキストを使う。
	ctx := r.Context()
	args := []string{
		"-i", abs,
		// 長辺が大きい動画は 1280px 幅へ縮小（2の倍数に丸め）。
		"-vf", "scale='min(1280,iw)':-2",
		"-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-ac", "2", "-b:a", "128k",
		// stdout へストリーム配信できる fragmented MP4。
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4",
		"-",
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "変換の開始に失敗しました", http.StatusInternalServerError)
		return
	}
	if err := cmd.Start(); err != nil {
		http.Error(w, "変換の開始に失敗しました", http.StatusInternalServerError)
		return
	}
	defer func() {
		// 残ったプロセスを確実に回収する。
		_ = cmd.Wait()
	}()

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	// ライブ変換のためシーク不可。ブラウザに Range 非対応を明示。
	w.Header().Set("Accept-Ranges", "none")

	// ffmpeg の出力をそのままレスポンスへ流す。
	_, _ = copyFlush(w, stdout)
}

// copyFlush は src を dst へ転送しつつ、可能ならこまめに flush して
// 再生開始の待ち時間を短くする。
func copyFlush(w http.ResponseWriter, src io.Reader) (int64, error) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 64*1024)
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			m, werr := w.Write(buf[:n])
			total += int64(m)
			if flusher != nil {
				flusher.Flush()
			}
			if werr != nil {
				return total, werr
			}
		}
		if err != nil {
			return total, err
		}
	}
}
