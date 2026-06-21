package media

import (
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

// SearchResult は検索ヒット1件。
type SearchResult struct {
	Name    string `json:"name"`
	RelPath string `json:"path"`
	Kind    Kind   `json:"kind"`
}

// Search はファイル名/フォルダ名の部分一致（大小無視）で再帰検索する。
// limit 件で打ち切る。数千ファイル規模を想定した都度walk方式。
func (l *Library) Search(query string, limit int) ([]SearchResult, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, nil
	}
	var results []SearchResult
	err := filepath.WalkDir(l.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // アクセスできない箇所はスキップ
		}
		name := d.Name()
		if p == l.root {
			return nil
		}
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		kind, ok := l.classify(name, d.IsDir())
		if !ok {
			return nil
		}
		if strings.Contains(strings.ToLower(name), q) {
			rel, _ := filepath.Rel(l.root, p)
			results = append(results, SearchResult{
				Name:    name,
				RelPath: "/" + path.Clean(filepath.ToSlash(rel)),
				Kind:    kind,
			})
			if len(results) >= limit {
				return fs.SkipAll
			}
		}
		return nil
	})
	return results, err
}

// WalkVideosNeedingTranscode はルート配下を再帰走査し、トランスコードが必要な
// 動画（avi/wmv/mkv など）の絶対パスごとに fn を呼ぶ。事前変換スイープ用。
func (l *Library) WalkVideosNeedingTranscode(fn func(abs string)) error {
	return filepath.WalkDir(l.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // アクセスできない箇所はスキップ
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if l.NeedsTranscode(name) {
			fn(p)
		}
		return nil
	})
}
