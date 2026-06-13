package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/t-develo/mediavault/internal/media"
	"github.com/t-develo/mediavault/internal/store"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ----- 認証 -----

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if blocked, remaining := s.auth.Blocked(ip); blocked {
		w.Header().Set("Retry-After", strings.TrimSuffix((remaining.Round(time.Second)).String(), "0s"))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":            "試行回数が上限に達しました。しばらくしてから再度お試しください。",
			"retry_after_secs": int(remaining.Seconds()) + 1,
		})
		return
	}

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不正なリクエストです"})
		return
	}

	token, ok := s.auth.Login(ip, body.Username, body.Password)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "ユーザー名またはパスワードが違います"})
		return
	}
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.auth.Logout(c.Value)
	}
	s.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"username":  s.cfg.Auth.Username,
		"ffmpeg":    s.thumb.HasFFmpeg(),
	})
}

// ----- 一覧・ツリー -----

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	entries, err := s.lib.List(p)
	if err != nil {
		s.mediaError(w, err)
		return
	}
	favSet, _ := s.store.FavoriteSet()
	type item struct {
		media.Entry
		Favorite bool `json:"favorite"`
	}
	out := make([]item, 0, len(entries))
	for _, e := range entries {
		out = append(out, item{Entry: e, Favorite: favSet[e.RelPath]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "entries": out})
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		p = "/"
	}
	folders, err := s.lib.Folders(p)
	if err != nil {
		s.mediaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "folders": folders})
}

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	images, err := s.lib.Images(p)
	if err != nil {
		s.mediaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "images": images})
}

// ----- サムネ -----

func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	abs, err := s.lib.Resolve(p)
	if err != nil {
		s.mediaError(w, err)
		return
	}
	isVideo := s.lib.IsVideo(abs)
	if !isVideo && !s.lib.IsImage(abs) {
		http.Error(w, "サムネ対象外です", http.StatusBadRequest)
		return
	}
	thumbPath, err := s.thumb.Image(abs, isVideo)
	if err != nil {
		// 動画でffmpeg無し等：プレースホルダ代わりに204
		http.Error(w, "サムネを生成できません", http.StatusNoContent)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, thumbPath)
}

// ----- メディア本体（Range対応） -----

func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	abs, err := s.lib.Resolve(p)
	if err != nil {
		s.mediaError(w, err)
		return
	}
	f, err := os.Open(abs)
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
	// http.ServeContent が Range / 206 / If-Modified-Since を処理する
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

// ----- 検索 -----

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	results, err := s.lib.Search(q, 200)
	if err != nil {
		http.Error(w, "検索に失敗しました", http.StatusInternalServerError)
		return
	}
	if results == nil {
		results = []media.SearchResult{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": q, "results": results})
}

// ----- お気に入り -----

func (s *Server) handleListFavorites(w http.ResponseWriter, r *http.Request) {
	favs, err := s.store.ListFavorites()
	if err != nil {
		http.Error(w, "取得に失敗しました", http.StatusInternalServerError)
		return
	}
	if favs == nil {
		favs = []store.Favorite{} // null ではなく [] を返す
	}
	writeJSON(w, http.StatusOK, map[string]any{"favorites": favs})
}

func (s *Server) handleAddFavorite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind string `json:"kind"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "不正なリクエスト", http.StatusBadRequest)
		return
	}
	if body.Kind != "folder" && body.Kind != "file" {
		http.Error(w, "kindが不正です", http.StatusBadRequest)
		return
	}
	// 実在パスのみ許可
	if _, err := s.lib.Resolve(body.Path); err != nil {
		s.mediaError(w, err)
		return
	}
	if err := s.store.AddFavorite(body.Kind, body.Path); err != nil {
		http.Error(w, "登録に失敗しました", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleRemoveFavorite(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		http.Error(w, "pathが必要です", http.StatusBadRequest)
		return
	}
	if err := s.store.RemoveFavorite(p); err != nil {
		http.Error(w, "解除に失敗しました", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// mediaError はメディア操作のエラーを適切なHTTPステータスへ変換する。
func (s *Server) mediaError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, media.ErrOutsideRoot):
		http.Error(w, "アクセスが拒否されました", http.StatusForbidden)
	case errors.Is(err, os.ErrNotExist):
		http.Error(w, "見つかりません", http.StatusNotFound)
	default:
		http.Error(w, "エラーが発生しました", http.StatusInternalServerError)
	}
}
