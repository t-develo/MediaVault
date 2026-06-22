package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store は SQLite データベースへのアクセスをまとめる。
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS favorites (
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL CHECK(kind IN ('folder','file')),
  rel_path TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS login_attempts (
  ip TEXT PRIMARY KEY,
  failed_count INTEGER NOT NULL DEFAULT 0,
  last_attempt_at INTEGER NOT NULL,
  blocked_until INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS history (
  rel_path TEXT PRIMARY KEY,
  viewed_at INTEGER NOT NULL
);
`

// Open はデータベースを開き（無ければ作成し）スキーマを適用する。
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("DBを開けません: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite の書き込み競合を避ける
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;"); err != nil {
		return nil, fmt.Errorf("PRAGMA設定に失敗: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("スキーマ適用に失敗: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// ----- セッション -----

func (s *Store) CreateSession(token string, ttl time.Duration) error {
	now := time.Now()
	_, err := s.db.Exec(
		"INSERT INTO sessions(token, created_at, expires_at) VALUES(?,?,?)",
		token, now.Unix(), now.Add(ttl).Unix(),
	)
	return err
}

// ValidSession はトークンが有効（存在し未失効）なら true を返す。
func (s *Store) ValidSession(token string) bool {
	if token == "" {
		return false
	}
	var expires int64
	err := s.db.QueryRow("SELECT expires_at FROM sessions WHERE token=?", token).Scan(&expires)
	if err != nil {
		return false
	}
	if time.Now().Unix() > expires {
		_, _ = s.db.Exec("DELETE FROM sessions WHERE token=?", token)
		return false
	}
	return true
}

func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE token=?", token)
	return err
}

// PurgeExpiredSessions は失効済みセッションを削除する。
func (s *Store) PurgeExpiredSessions() {
	_, _ = s.db.Exec("DELETE FROM sessions WHERE expires_at < ?", time.Now().Unix())
}

// ----- ログイン試行 / IPブロック -----

// Attempt は IP の現在の失敗回数とブロック解除時刻(unix)を返す。
func (s *Store) Attempt(ip string) (failed int, blockedUntil int64) {
	_ = s.db.QueryRow(
		"SELECT failed_count, blocked_until FROM login_attempts WHERE ip=?", ip,
	).Scan(&failed, &blockedUntil)
	return
}

// RecordFailure は失敗回数を1増やし、閾値到達でブロックする。
func (s *Store) RecordFailure(ip string, maxFailures, blockMinutes int) {
	now := time.Now()
	failed, _ := s.Attempt(ip)
	failed++
	var blockedUntil int64
	if failed >= maxFailures {
		blockedUntil = now.Add(time.Duration(blockMinutes) * time.Minute).Unix()
	}
	_, _ = s.db.Exec(`
		INSERT INTO login_attempts(ip, failed_count, last_attempt_at, blocked_until)
		VALUES(?,?,?,?)
		ON CONFLICT(ip) DO UPDATE SET
		  failed_count=excluded.failed_count,
		  last_attempt_at=excluded.last_attempt_at,
		  blocked_until=excluded.blocked_until
	`, ip, failed, now.Unix(), blockedUntil)
}

// ResetAttempts はログイン成功時に失敗履歴を消す。
func (s *Store) ResetAttempts(ip string) {
	_, _ = s.db.Exec("DELETE FROM login_attempts WHERE ip=?", ip)
}

// ----- お気に入り -----

type Favorite struct {
	Kind      string `json:"kind"`
	RelPath   string `json:"path"`
	CreatedAt int64  `json:"created_at"`
}

func (s *Store) AddFavorite(kind, relPath string) error {
	_, err := s.db.Exec(
		"INSERT OR IGNORE INTO favorites(kind, rel_path, created_at) VALUES(?,?,?)",
		kind, relPath, time.Now().Unix(),
	)
	return err
}

func (s *Store) RemoveFavorite(relPath string) error {
	_, err := s.db.Exec("DELETE FROM favorites WHERE rel_path=?", relPath)
	return err
}

func (s *Store) ListFavorites() ([]Favorite, error) {
	rows, err := s.db.Query("SELECT kind, rel_path, created_at FROM favorites ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var favs []Favorite
	for rows.Next() {
		var f Favorite
		if err := rows.Scan(&f.Kind, &f.RelPath, &f.CreatedAt); err != nil {
			return nil, err
		}
		favs = append(favs, f)
	}
	return favs, rows.Err()
}

// ----- 履歴（最近見たフォルダ） -----

type HistoryEntry struct {
	RelPath  string `json:"path"`
	ViewedAt int64  `json:"viewed_at"`
}

// AddHistory はフォルダの閲覧履歴を記録する（既存なら閲覧時刻を更新）。
func (s *Store) AddHistory(relPath string) error {
	_, err := s.db.Exec(`
		INSERT INTO history(rel_path, viewed_at) VALUES(?,?)
		ON CONFLICT(rel_path) DO UPDATE SET viewed_at=excluded.viewed_at
	`, relPath, time.Now().Unix())
	return err
}

// ListHistory は最近見たフォルダを新しい順に最大 limit 件返す。
func (s *Store) ListHistory(limit int) ([]HistoryEntry, error) {
	rows, err := s.db.Query(
		"SELECT rel_path, viewed_at FROM history ORDER BY viewed_at DESC LIMIT ?", limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hist []HistoryEntry
	for rows.Next() {
		var h HistoryEntry
		if err := rows.Scan(&h.RelPath, &h.ViewedAt); err != nil {
			return nil, err
		}
		hist = append(hist, h)
	}
	return hist, rows.Err()
}

// FavoriteSet は指定パス集合のうちお気に入り登録済みのものを返す。
func (s *Store) FavoriteSet() (map[string]bool, error) {
	rows, err := s.db.Query("SELECT rel_path FROM favorites")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := make(map[string]bool)
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		set[p] = true
	}
	return set, rows.Err()
}
