package auth

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/t-develo/mediavault/internal/config"
	"github.com/t-develo/mediavault/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// Authenticator は単一ユーザー認証・セッション・IPブロックを担う。
type Authenticator struct {
	cfg   *config.Config
	store *store.Store
}

func New(cfg *config.Config, st *store.Store) *Authenticator {
	return &Authenticator{cfg: cfg, store: st}
}

// Blocked は IP がブロック中なら残り時間とともに true を返す。
func (a *Authenticator) Blocked(ip string) (bool, time.Duration) {
	_, until := a.store.Attempt(ip)
	if until > time.Now().Unix() {
		return true, time.Until(time.Unix(until, 0))
	}
	return false, 0
}

// Login は資格情報を検証する。成功時にセッショントークンを返す。
// 失敗・成功に応じて IP の試行履歴を更新する。
func (a *Authenticator) Login(ip, username, password string) (token string, ok bool) {
	userMatch := subtleEqual(username, a.cfg.Auth.Username)
	passMatch := bcrypt.CompareHashAndPassword(
		[]byte(a.cfg.Auth.PasswordHash), []byte(password),
	) == nil

	if !userMatch || !passMatch {
		a.store.RecordFailure(ip, a.cfg.IPBlock.MaxFailures, a.cfg.IPBlock.BlockMinutes)
		return "", false
	}

	a.store.ResetAttempts(ip)
	token = newToken()
	ttl := time.Duration(a.cfg.SessionTTLDays) * 24 * time.Hour
	if err := a.store.CreateSession(token, ttl); err != nil {
		return "", false
	}
	return token, true
}

// Valid はセッショントークンが有効か判定する。
func (a *Authenticator) Valid(token string) bool {
	return a.store.ValidSession(token)
}

// Logout はセッションを破棄する。
func (a *Authenticator) Logout(token string) {
	_ = a.store.DeleteSession(token)
}

func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// subtleEqual はユーザー名比較（短いので長さ差はそのまま、定数時間は不要だが一応）。
func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
