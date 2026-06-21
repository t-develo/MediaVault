package server

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/t-develo/mediavault/internal/auth"
	"github.com/t-develo/mediavault/internal/config"
	"github.com/t-develo/mediavault/internal/convert"
	"github.com/t-develo/mediavault/internal/media"
	"github.com/t-develo/mediavault/internal/store"
	"github.com/t-develo/mediavault/internal/thumb"
	"github.com/t-develo/mediavault/web"
)

const sessionCookie = "mv_session"

// Server は HTTP ハンドラ群と依存をまとめる。
type Server struct {
	cfg   *config.Config
	auth  *auth.Authenticator
	store *store.Store
	lib   *media.Library
	thumb *thumb.Generator
	conv  *convert.Converter
}

func New(cfg *config.Config, st *store.Store, au *auth.Authenticator, lib *media.Library, tg *thumb.Generator, cv *convert.Converter) *Server {
	return &Server{cfg: cfg, auth: au, store: st, lib: lib, thumb: tg, conv: cv}
}

// Handler は全ルートを登録した http.Handler を返す。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 静的アセット（認証不要：ログイン画面のため）
	staticFS := http.FileServerFS(web.Assets)
	mux.Handle("GET /app.js", staticFS)
	mux.Handle("GET /style.css", staticFS)
	mux.HandleFunc("GET /", s.handleIndex)

	// 認証エンドポイント
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)

	// 保護されたAPI
	mux.Handle("GET /api/me", s.requireAuth(http.HandlerFunc(s.handleMe)))
	mux.Handle("GET /api/list", s.requireAuth(http.HandlerFunc(s.handleList)))
	mux.Handle("GET /api/tree", s.requireAuth(http.HandlerFunc(s.handleTree)))
	mux.Handle("GET /api/images", s.requireAuth(http.HandlerFunc(s.handleImages)))
	mux.Handle("GET /api/thumb", s.requireAuth(http.HandlerFunc(s.handleThumb)))
	mux.Handle("GET /api/media", s.requireAuth(http.HandlerFunc(s.handleMedia)))
	mux.Handle("GET /api/convert", s.requireAuth(http.HandlerFunc(s.handleConvertStatus)))
	mux.Handle("GET /api/converted", s.requireAuth(http.HandlerFunc(s.handleConverted)))
	mux.Handle("GET /api/search", s.requireAuth(http.HandlerFunc(s.handleSearch)))
	mux.Handle("GET /api/favorites", s.requireAuth(http.HandlerFunc(s.handleListFavorites)))
	mux.Handle("POST /api/favorites", s.requireAuth(http.HandlerFunc(s.handleAddFavorite)))
	mux.Handle("DELETE /api/favorites", s.requireAuth(http.HandlerFunc(s.handleRemoveFavorite)))

	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// "/" 以外のパスは404（SPAだが未知ルートは弾く）
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := web.Assets.ReadFile("index.html")
	if err != nil {
		http.Error(w, "index not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

// requireAuth は有効なセッションを要求するミドルウェア。
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || !s.auth.Valid(c.Value) {
			http.Error(w, "認証が必要です", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP はリクエスト元IPを返す（trust_proxy時はXFFの先頭を採用）。
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   s.cfg.SessionTTLDays * 24 * 60 * 60,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// StartSessionGC は定期的に失効セッションを掃除する。
func (s *Server) StartSessionGC() {
	go func() {
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for range t.C {
			s.store.PurgeExpiredSessions()
		}
	}()
}
