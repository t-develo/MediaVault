package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/t-develo/mediavault/internal/auth"
	"github.com/t-develo/mediavault/internal/config"
	"github.com/t-develo/mediavault/internal/convert"
	"github.com/t-develo/mediavault/internal/media"
	"github.com/t-develo/mediavault/internal/server"
	"github.com/t-develo/mediavault/internal/store"
	"github.com/t-develo/mediavault/internal/thumb"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "serve":
		runServe(os.Args[2:])
	case "setuser":
		runSetUser(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "不明なコマンド: %s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `MediaVault — HDDメディア閲覧サーバ

使い方:
  mediavault serve [-config config.yaml]   サーバを起動
  mediavault setuser [-u ユーザー名]         パスワードハッシュを生成して表示

`)
}

func runServe(args []string) {
	configPath := "config.yaml"
	for i := 0; i < len(args); i++ {
		if args[i] == "-config" && i+1 < len(args) {
			configPath = args[i+1]
			i++
		}
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fatal(err)
	}
	if cfg.Auth.Username == "" || cfg.Auth.PasswordHash == "" {
		fatal(fmt.Errorf("auth.username / auth.password_hash が未設定です。`mediavault setuser` で生成してください"))
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fatal(err)
	}
	defer st.Close()

	lib := media.NewLibrary(cfg.MediaRoot, cfg.Extensions.Image, cfg.Extensions.Video)
	au := auth.New(cfg, st)

	// サムネ生成の同時実行数はCPU数を上限に（ラズパイ配慮）
	tg, err := thumb.NewGenerator(cfg.CacheDir, runtime.NumCPU())
	if err != nil {
		fatal(err)
	}

	// avi/wmv 等を H.264/AAC の MP4 へ事前変換しキャッシュする変換器。
	cv, err := convert.NewConverter(cfg.CacheDir, convert.Options{
		Concurrency: cfg.Convert.Concurrency,
		MaxBytes:    int64(cfg.Convert.MaxCacheMB) * 1024 * 1024,
		Width:       cfg.Convert.Width,
		Preset:      cfg.Convert.Preset,
	})
	if err != nil {
		fatal(err)
	}
	cv.Start()
	// 事前変換スイープ（低優先）: 起動後にライブラリを走査して順次変換する。
	if cfg.Convert.PreconvertEnabled() && cv.HasFFmpeg() {
		go func() {
			_ = lib.WalkVideosNeedingTranscode(cv.Enqueue)
		}()
	}

	srv := server.New(cfg, st, au, lib, tg, cv)
	srv.StartSessionGC()

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	fmt.Printf("MediaVault 起動: http://%s （メディア: %s）\n", cfg.Listen, cfg.MediaRoot)
	if !tg.HasFFmpeg() {
		fmt.Println("注意: ffmpeg が見つかりません。動画サムネ生成と avi/wmv 等の変換再生は無効です（apt install ffmpeg を推奨）。")
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		fatal(err)
	}
}

func runSetUser(args []string) {
	username := "admin"
	for i := 0; i < len(args); i++ {
		if args[i] == "-u" && i+1 < len(args) {
			username = args[i+1]
			i++
		}
	}

	fmt.Fprint(os.Stderr, "パスワードを入力: ")
	pw1, err := readPassword()
	if err != nil {
		fatal(err)
	}
	fmt.Fprint(os.Stderr, "もう一度入力: ")
	pw2, err := readPassword()
	if err != nil {
		fatal(err)
	}
	if pw1 != pw2 {
		fatal(fmt.Errorf("パスワードが一致しません"))
	}
	if len(pw1) < 4 {
		fatal(fmt.Errorf("パスワードが短すぎます"))
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(pw1), bcrypt.DefaultCost)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("\nconfig.yaml の auth セクションに以下を設定してください:\n\n")
	fmt.Printf("auth:\n  username: %q\n  password_hash: %q\n", username, string(hash))
}

// stdinReader はパイプ入力時に複数行を取りこぼさないための共有リーダー。
var stdinReader = bufio.NewReader(os.Stdin)

// readPassword は端末ならエコー無しで、非端末なら1行読み取る。
func readPassword() (string, error) {
	if term.IsTerminal(int(syscall.Stdin)) {
		b, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	line, err := stdinReader.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "エラー: %v\n", err)
	os.Exit(1)
}
