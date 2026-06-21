// Package convert は avi/wmv などブラウザ非対応の動画を H.264/AAC の MP4 へ
// バックグラウンドで変換し、ディスクにキャッシュする。
//
// サムネ生成（internal/thumb）と同じく「ハッシュキー＋tmp→renameでアトミック確定＋
// セマフォで同時実行制限」の流儀を踏襲し、変換結果は完全な（moov先頭=faststart）MP4
// として保存する。これにより再生側は Range 配信でシーク可能になり、再変換も不要になる。
package convert

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// State は1ファイルの変換状態。
type State string

const (
	StateQueued     State = "queued"
	StateConverting State = "converting"
	StateReady      State = "ready"
	StateFailed     State = "failed"
)

// Status は変換状態のスナップショット。Progress は 0–100、不明時は -1。
type Status struct {
	State    State   `json:"state"`
	Progress float64 `json:"progress"`
	Message  string  `json:"message,omitempty"`
}

// Options は変換パラメータ。
type Options struct {
	Concurrency int   // 同時変換数
	MaxBytes    int64 // 変換キャッシュ上限（バイト）。0以下で無制限。
	Width       int   // 変換時の最大横幅
	Preset      string
}

type job struct {
	abs     string
	out     string // 確定先キャッシュパス
	state   State
	prog    float64
	message string
}

// Converter は変換ジョブの登録・実行・進捗・LRU を管理する。
type Converter struct {
	dir     string // cacheDir/converted
	ffmpeg  string // 空なら変換不可
	ffprobe string // 空なら進捗不明（変換は継続）
	opts    Options

	sem chan struct{} // 同時変換数の制限
	hiQ chan string   // オンデマンド要求（高優先）
	loQ chan string   // 事前スイープ（低優先）

	mu   sync.Mutex
	jobs map[string]*job // cacheKey -> job
}

// NewConverter は変換キャッシュ用ディレクトリを用意し Converter を返す。
func NewConverter(cacheDir string, opts Options) (*Converter, error) {
	dir := filepath.Join(cacheDir, "converted")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("変換キャッシュ作成に失敗: %w", err)
	}
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	if opts.Width < 1 {
		opts.Width = 1280
	}
	if opts.Preset == "" {
		opts.Preset = "veryfast"
	}
	ffmpeg, _ := exec.LookPath("ffmpeg")
	ffprobe, _ := exec.LookPath("ffprobe")
	return &Converter{
		dir:     dir,
		ffmpeg:  ffmpeg,
		ffprobe: ffprobe,
		opts:    opts,
		sem:     make(chan struct{}, opts.Concurrency),
		// バッファは大きめに。事前スイープで多数を一括投入してもブロックしにくくする。
		hiQ:  make(chan string, 1024),
		loQ:  make(chan string, 65536),
		jobs: make(map[string]*job),
	}, nil
}

// HasFFmpeg は変換が利用可能かを返す。
func (c *Converter) HasFFmpeg() bool { return c.ffmpeg != "" }

// Start はワーカープールを起動する。多重呼び出しは想定しない。
func (c *Converter) Start() {
	for i := 0; i < c.opts.Concurrency; i++ {
		go c.worker()
	}
}

// cachePath は絶対パスと更新時刻からキャッシュファイル名を作る（thumb と同方式）。
func (c *Converter) cachePath(abs string, mod time.Time) string {
	h := sha256.Sum256([]byte(abs + "|" + strconv.FormatInt(mod.UnixNano(), 10)))
	return filepath.Join(c.dir, hex.EncodeToString(h[:16])+".mp4")
}

// resolveOut は対象ファイルの stat からキャッシュパスを得る。
func (c *Converter) resolveOut(abs string) (string, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	return c.cachePath(abs, info.ModTime()), nil
}

// CachePathIfReady は変換済みキャッシュがあればそのパスと true を返す。
// 返す際に ModTime を現在時刻へ更新し、LRU の「最終利用時刻」を進める。
func (c *Converter) CachePathIfReady(abs string) (string, bool) {
	out, err := c.resolveOut(abs)
	if err != nil {
		return "", false
	}
	if fi, err := os.Stat(out); err == nil && fi.Size() > 0 {
		now := time.Now()
		_ = os.Chtimes(out, now, now)
		return out, true
	}
	return "", false
}

// Request はオンデマンド（高優先）で変換を要求し、現在の状態を返す。
// キャッシュ済みなら即 ready。未変換ならジョブを生成して高優先キューへ載せる。
func (c *Converter) Request(abs string) Status {
	return c.request(abs, true)
}

// Enqueue は事前スイープ（低優先）で変換を予約する。
func (c *Converter) Enqueue(abs string) {
	c.request(abs, false)
}

func (c *Converter) request(abs string, hi bool) Status {
	out, err := c.resolveOut(abs)
	if err != nil {
		return Status{State: StateFailed, Progress: -1, Message: "ファイルが見つかりません"}
	}
	// キャッシュ済みなら即 ready（LRU の最終利用時刻も更新）。
	if fi, err := os.Stat(out); err == nil && fi.Size() > 0 {
		now := time.Now()
		_ = os.Chtimes(out, now, now)
		return Status{State: StateReady, Progress: 100}
	}
	if c.ffmpeg == "" {
		return Status{State: StateFailed, Progress: -1, Message: "再生には ffmpeg が必要です"}
	}

	key := filepath.Base(out)
	c.mu.Lock()
	j, ok := c.jobs[key]
	if !ok {
		j = &job{abs: abs, out: out, state: StateQueued, prog: 0}
		c.jobs[key] = j
	}
	st := Status{State: j.state, Progress: j.prog, Message: j.message}
	c.mu.Unlock()

	if !ok {
		// 新規ジョブのみキューへ投入（重複投入を抑止）。
		if hi {
			c.enqueueNonBlocking(c.hiQ, key)
		} else {
			c.enqueueNonBlocking(c.loQ, key)
		}
	}
	return st
}

func (c *Converter) enqueueNonBlocking(q chan string, key string) {
	select {
	case q <- key:
	default:
		// キューが一杯：ジョブ登録だけ残し、次回 Request 時の再投入に委ねる。
		c.mu.Lock()
		delete(c.jobs, key)
		c.mu.Unlock()
	}
}

func (c *Converter) worker() {
	for {
		// 高優先（オンデマンド）を優先的に処理する。
		select {
		case key := <-c.hiQ:
			c.process(key)
			continue
		default:
		}
		select {
		case key := <-c.hiQ:
			c.process(key)
		case key := <-c.loQ:
			c.process(key)
		}
	}
}

func (c *Converter) process(key string) {
	c.mu.Lock()
	j := c.jobs[key]
	c.mu.Unlock()
	if j == nil {
		return
	}

	// キュー滞留中に他ワーカーが完了させている可能性を再チェック。
	if fi, err := os.Stat(j.out); err == nil && fi.Size() > 0 {
		c.finish(key, StateReady, 100, "")
		return
	}

	c.sem <- struct{}{}
	defer func() { <-c.sem }()

	c.setState(key, StateConverting, 0, "")
	if err := c.convert(j, key); err != nil {
		c.finish(key, StateFailed, -1, "変換に失敗しました")
		return
	}
	c.finish(key, StateReady, 100, "")
	c.enforceLRU()
}

// convert は ffmpeg で完全な（faststart）MP4 を生成する。
func (c *Converter) convert(j *job, key string) error {
	dur := c.probeDuration(j.abs) // 秒。0 なら進捗不明。

	tmp := j.out + ".tmp"
	defer os.Remove(tmp)

	ctx := context.Background()
	args := []string{
		"-nostdin",
		"-i", j.abs,
		"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", c.opts.Width),
		"-c:v", "libx264", "-preset", c.opts.Preset, "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-ac", "2", "-b:a", "128k",
		// moov を先頭へ（シーク可能な MP4）。
		"-movflags", "+faststart",
		// 進捗を stdout へ key=value で出力させる。
		"-progress", "pipe:1", "-nostats",
		"-f", "mp4", "-y", tmp,
	}
	cmd := exec.CommandContext(ctx, c.ffmpeg, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	c.readProgress(stdout, key, dur)
	if err := cmd.Wait(); err != nil {
		return err
	}
	if fi, err := os.Stat(tmp); err != nil || fi.Size() == 0 {
		return fmt.Errorf("変換出力が空です")
	}
	return os.Rename(tmp, j.out) // アトミックに確定
}

// readProgress は ffmpeg -progress の出力を読み、out_time_us から進捗(%)を更新する。
func (c *Converter) readProgress(r io.Reader, key string, dur float64) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if dur <= 0 {
			continue // 総尺不明：進捗は -1 のまま
		}
		v, ok := strings.CutPrefix(line, "out_time_us=")
		if !ok {
			continue
		}
		us, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			continue
		}
		p := float64(us) / 1e6 / dur * 100
		if p < 0 {
			p = 0
		}
		if p > 99.9 {
			p = 99.9 // 確定は完了時に 100 へ
		}
		c.setProgress(key, p)
	}
}

// probeDuration は ffprobe で総尺（秒）を返す。取得不能なら 0。
func (c *Converter) probeDuration(abs string) float64 {
	if c.ffprobe == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, c.ffprobe,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		abs,
	).Output()
	if err != nil {
		return 0
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || d < 0 {
		return 0
	}
	return d
}

// Status は対象ファイルの現在状態を返す（ジョブが無ければ ready/queued を推定）。
func (c *Converter) Status(abs string) Status {
	if _, ok := c.CachePathIfReady(abs); ok {
		return Status{State: StateReady, Progress: 100}
	}
	out, err := c.resolveOut(abs)
	if err != nil {
		return Status{State: StateFailed, Progress: -1, Message: "ファイルが見つかりません"}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if j, ok := c.jobs[filepath.Base(out)]; ok {
		return Status{State: j.state, Progress: j.prog, Message: j.message}
	}
	return Status{State: StateQueued, Progress: 0}
}

func (c *Converter) setState(key string, st State, prog float64, msg string) {
	c.mu.Lock()
	if j := c.jobs[key]; j != nil {
		j.state, j.prog, j.message = st, prog, msg
	}
	c.mu.Unlock()
}

func (c *Converter) setProgress(key string, prog float64) {
	c.mu.Lock()
	if j := c.jobs[key]; j != nil {
		j.prog = prog
	}
	c.mu.Unlock()
}

// finish は終了状態を記録する。ready/failed のジョブは map から消し、
// 状態は以後 Status()（キャッシュ有無）で判定させる。
func (c *Converter) finish(key string, st State, prog float64, msg string) {
	c.mu.Lock()
	if st == StateReady {
		delete(c.jobs, key)
	} else if j := c.jobs[key]; j != nil {
		j.state, j.prog, j.message = st, prog, msg
	}
	c.mu.Unlock()
}

// enforceLRU は変換キャッシュの合計サイズが上限を超えていたら、
// ModTime（最終利用時刻）が古いものから削除する。
func (c *Converter) enforceLRU() {
	if c.opts.MaxBytes <= 0 {
		return
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	type fent struct {
		path string
		size int64
		mod  time.Time
	}
	var files []fent
	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".mp4") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fent{filepath.Join(c.dir, e.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	if total <= c.opts.MaxBytes {
		return
	}
	// 古い順に削除して上限内へ。
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= c.opts.MaxBytes {
			break
		}
		if err := os.Remove(f.path); err == nil {
			total -= f.size
		}
	}
}
