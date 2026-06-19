package media

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ErrOutsideRoot はメディアルート外へのアクセス試行を表す。
var ErrOutsideRoot = errors.New("メディアルート外へのアクセスは許可されていません")

// Kind はエントリの種別。
type Kind string

const (
	KindFolder Kind = "folder"
	KindImage  Kind = "image"
	KindVideo  Kind = "video"
)

// Library はメディアルートと拡張子設定を保持する。
type Library struct {
	root      string
	imageExts map[string]bool
	videoExts map[string]bool
}

func NewLibrary(root string, imageExts, videoExts []string) *Library {
	l := &Library{
		root:      root,
		imageExts: toSet(imageExts),
		videoExts: toSet(videoExts),
	}
	return l
}

func toSet(exts []string) map[string]bool {
	m := make(map[string]bool, len(exts))
	for _, e := range exts {
		m["."+strings.ToLower(strings.TrimPrefix(e, "."))] = true
	}
	return m
}

// Resolve は相対パスを絶対パスへ変換し、ルート外を拒否する（トラバーサル防御）。
func (l *Library) Resolve(relPath string) (string, error) {
	// URLパスとして正規化してからOSパスへ
	clean := path.Clean("/" + strings.TrimPrefix(relPath, "/"))
	abs := filepath.Join(l.root, filepath.FromSlash(clean))
	// ルート配下であることを保証
	rel, err := filepath.Rel(l.root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrOutsideRoot
	}
	return abs, nil
}

// Root はメディアルートの絶対パスを返す。
func (l *Library) Root() string { return l.root }

func (l *Library) classify(name string, isDir bool) (Kind, bool) {
	if isDir {
		return KindFolder, true
	}
	ext := strings.ToLower(filepath.Ext(name))
	if l.imageExts[ext] {
		return KindImage, true
	}
	if l.videoExts[ext] {
		return KindVideo, true
	}
	return "", false
}

// browserNativeVideo はブラウザの <video> がそのまま再生できる拡張子。
// これ以外の動画は再生時に ffmpeg でトランスコードして配信する。
var browserNativeVideo = map[string]bool{
	".mp4": true, ".m4v": true, ".webm": true, ".ogg": true, ".ogv": true, ".mov": true,
}

// IsImage / IsVideo は拡張子から判定する。
func (l *Library) IsImage(name string) bool { return l.imageExts[strings.ToLower(filepath.Ext(name))] }
func (l *Library) IsVideo(name string) bool { return l.videoExts[strings.ToLower(filepath.Ext(name))] }

// NeedsTranscode は、対象動画がブラウザ非対応形式で再生時に
// トランスコードが必要かを返す（avi / wmv / mkv など）。
func (l *Library) NeedsTranscode(name string) bool {
	return l.IsVideo(name) && !browserNativeVideo[strings.ToLower(filepath.Ext(name))]
}

// Entry は一覧表示用のエントリ。
type Entry struct {
	Name    string `json:"name"`
	RelPath string `json:"path"`
	Kind    Kind   `json:"kind"`
	// Transcode は動画再生時にトランスコードが必要な場合のみ true。
	Transcode bool `json:"transcode,omitempty"`
}

// List は指定フォルダ直下のサブフォルダとメディアを返す（遅延読込）。
func (l *Library) List(relPath string) ([]Entry, error) {
	abs, err := l.Resolve(relPath)
	if err != nil {
		return nil, err
	}
	dirEntries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	clean := path.Clean("/" + strings.TrimPrefix(relPath, "/"))
	var entries []Entry
	for _, de := range dirEntries {
		name := de.Name()
		if strings.HasPrefix(name, ".") {
			continue // 隠しファイルは除外
		}
		kind, ok := l.classify(name, de.IsDir())
		if !ok {
			continue
		}
		entries = append(entries, Entry{
			Name:      name,
			RelPath:   path.Join(clean, name),
			Kind:      kind,
			Transcode: kind == KindVideo && l.NeedsTranscode(name),
		})
	}
	sortEntries(entries)
	return entries, nil
}

// Folders は指定フォルダ直下のサブフォルダのみ返す（左ツリー用）。
func (l *Library) Folders(relPath string) ([]Entry, error) {
	abs, err := l.Resolve(relPath)
	if err != nil {
		return nil, err
	}
	dirEntries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	clean := path.Clean("/" + strings.TrimPrefix(relPath, "/"))
	var entries []Entry
	for _, de := range dirEntries {
		if !de.IsDir() || strings.HasPrefix(de.Name(), ".") {
			continue
		}
		entries = append(entries, Entry{
			Name:    de.Name(),
			RelPath: path.Join(clean, de.Name()),
			Kind:    KindFolder,
		})
	}
	sortEntries(entries)
	return entries, nil
}

// Images は指定フォルダ直下の画像を並び順で返す（漫画ビューア用）。
func (l *Library) Images(relPath string) ([]Entry, error) {
	all, err := l.List(relPath)
	if err != nil {
		return nil, err
	}
	images := all[:0]
	for _, e := range all {
		if e.Kind == KindImage {
			images = append(images, e)
		}
	}
	return images, nil
}

// sortEntries はフォルダ優先＋名前の自然順で並べる。
func sortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if (a.Kind == KindFolder) != (b.Kind == KindFolder) {
			return a.Kind == KindFolder
		}
		return naturalLess(a.Name, b.Name)
	})
}

// naturalLess は数字を考慮した自然順比較（"img2" < "img10"）。
func naturalLess(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	i, j := 0, 0
	for i < len(la) && j < len(lb) {
		ca, cb := la[i], lb[j]
		if isDigit(ca) && isDigit(cb) {
			ni, si := readNum(la, i)
			nj, sj := readNum(lb, j)
			if ni != nj {
				return ni < nj
			}
			i, j = si, sj
			continue
		}
		if ca != cb {
			return ca < cb
		}
		i++
		j++
	}
	return len(la) < len(lb)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func readNum(s string, i int) (int64, int) {
	start := i
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	n, _ := strconv.ParseInt(s[start:i], 10, 64)
	return n, i
}
