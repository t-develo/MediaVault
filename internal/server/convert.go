package server

import (
	"net/http"
	"os"
)

// handleConvertStatus はブラウザ非対応動画の変換状態を返す。
// 未変換ならオンデマンド（高優先）で変換キューへ載せる。フロントはこれを
// ポーリングして進捗を表示し、ready になったら /api/converted を再生する。
func (s *Server) handleConvertStatus(w http.ResponseWriter, r *http.Request) {
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
	// ブラウザネイティブ形式は変換不要（そのまま ready 扱い）。
	if !s.lib.NeedsTranscode(abs) {
		writeJSON(w, http.StatusOK, map[string]any{"state": "ready", "progress": 100})
		return
	}
	st := s.conv.Request(abs)
	writeJSON(w, http.StatusOK, st)
}

// handleConverted は変換済みキャッシュ MP4 を Range 対応で配信する（シーク可能）。
// 未完成の場合は 404 を返す。
func (s *Server) handleConverted(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	abs, err := s.lib.Resolve(p)
	if err != nil {
		s.mediaError(w, err)
		return
	}
	cachePath, ok := s.conv.CachePathIfReady(abs)
	if !ok {
		http.Error(w, "変換が完了していません", http.StatusNotFound)
		return
	}
	f, err := os.Open(cachePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	// http.ServeContent が Range / 206 / If-Modified-Since を処理する。
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}
