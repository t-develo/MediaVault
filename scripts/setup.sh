#!/usr/bin/env bash
#
# MediaVault セットアップスクリプト（Raspberry Pi OS / Debian 系向け）
#
# Go と ffmpeg の有無を確認し、不足していればインストールしたうえで
# MediaVault をビルドします。任意で config.yaml の生成・パスワード設定・
# systemd サービス登録まで行います。
#
# 使い方:
#   ./scripts/setup.sh                 # 依存確認＋ビルド（対話あり）
#   ./scripts/setup.sh --yes           # 確認プロンプトを省略
#   ./scripts/setup.sh --install-service   # systemd サービスも登録
#   ./scripts/setup.sh --skip-go       # Go のインストールをスキップ
#   ./scripts/setup.sh --skip-ffmpeg   # ffmpeg のインストールをスキップ
#   ./scripts/setup.sh --go-version 1.25.11   # 入れる Go のバージョンを指定
#
set -euo pipefail

# ---- 設定 ----
MIN_GO_VERSION="1.25"          # go.mod が要求する最低バージョン
DEFAULT_GO_VERSION="1.25.11"   # 自前インストール時に入れるバージョン
GO_INSTALL_DIR="/usr/local"    # Go の展開先（/usr/local/go）
BINARY_NAME="mediavault"

# ---- フラグ ----
ASSUME_YES=0
SKIP_GO=0
SKIP_FFMPEG=0
INSTALL_SERVICE=0
GO_VERSION="$DEFAULT_GO_VERSION"

while [ $# -gt 0 ]; do
  case "$1" in
    --yes|-y) ASSUME_YES=1 ;;
    --skip-go) SKIP_GO=1 ;;
    --skip-ffmpeg) SKIP_FFMPEG=1 ;;
    --install-service) INSTALL_SERVICE=1 ;;
    --go-version) GO_VERSION="${2:?--go-version には値が必要です}"; shift ;;
    -h|--help)
      # 先頭の連続コメント（ドキュメントブロック）のみ表示
      awk 'NR==1{next} /^#/{sub(/^#[[:space:]]?/,"");print;next} {exit}' "$0"; exit 0 ;;
    *) echo "不明なオプション: $1" >&2; exit 1 ;;
  esac
  shift
done

# ---- 表示ヘルパ ----
c_info()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
c_ok()    { printf '\033[1;32m ✓\033[0m %s\n' "$*"; }
c_warn()  { printf '\033[1;33m !\033[0m %s\n' "$*"; }
c_err()   { printf '\033[1;31m ✗\033[0m %s\n' "$*" >&2; }
die()     { c_err "$*"; exit 1; }

confirm() {
  [ "$ASSUME_YES" = "1" ] && return 0
  local prompt="$1"
  read -r -p "$prompt [y/N]: " ans </dev/tty || return 1
  [[ "$ans" =~ ^[Yy]$ ]]
}

# root 権限が必要なコマンドの実行（必要なら sudo を付ける）
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
  if command -v sudo >/dev/null 2>&1; then
    SUDO="sudo"
  fi
fi
as_root() {
  if [ -n "$SUDO" ]; then $SUDO "$@"; else "$@"; fi
}

# ---- リポジトリのルートへ移動 ----
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_DIR"
[ -f "go.mod" ] || die "go.mod が見つかりません。MediaVault のリポジトリ内で実行してください。"

# ---- バージョン比較（$1 >= $2 なら true） ----
version_ge() {
  [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" = "$2" ]
}

# ---- アーキテクチャ判定 → Go の配布名 ----
detect_go_arch() {
  case "$(uname -m)" in
    aarch64|arm64) echo "arm64" ;;
    armv7l|armv6l|arm) echo "armv6l" ;;   # 32bit Pi。armv6l は armv7 でも動作
    x86_64|amd64) echo "amd64" ;;
    *) die "未対応のアーキテクチャ: $(uname -m)" ;;
  esac
}

# ---- OS チェック ----
check_os() {
  if [ "$(uname -s)" != "Linux" ]; then
    c_warn "Linux 以外で実行されています。Raspberry Pi OS / Debian 系を想定しています。"
  fi
  if ! command -v apt-get >/dev/null 2>&1; then
    c_warn "apt-get が見つかりません。ffmpeg の自動インストールは行えません。"
  fi
}

# ---- Go の確認とインストール ----
GO_BIN=""
locate_go() {
  if command -v go >/dev/null 2>&1; then
    GO_BIN="$(command -v go)"
  elif [ -x "$GO_INSTALL_DIR/go/bin/go" ]; then
    GO_BIN="$GO_INSTALL_DIR/go/bin/go"
    export PATH="$GO_INSTALL_DIR/go/bin:$PATH"
  fi
}

current_go_version() {
  "$GO_BIN" version 2>/dev/null | grep -oE 'go[0-9]+\.[0-9]+(\.[0-9]+)?' | head -n1 | sed 's/^go//'
}

install_go() {
  local arch tarball url tmp
  arch="$(detect_go_arch)"
  tarball="go${GO_VERSION}.linux-${arch}.tar.gz"
  url="https://go.dev/dl/${tarball}"
  tmp="$(mktemp -d)"

  c_info "Go ${GO_VERSION} (${arch}) をダウンロードします: $url"
  if command -v curl >/dev/null 2>&1; then
    curl -fL --retry 3 -o "$tmp/$tarball" "$url" || die "Go のダウンロードに失敗しました"
  elif command -v wget >/dev/null 2>&1; then
    wget -O "$tmp/$tarball" "$url" || die "Go のダウンロードに失敗しました"
  else
    die "curl または wget が必要です"
  fi

  c_info "Go を $GO_INSTALL_DIR/go に展開します（既存があれば置き換え）"
  as_root rm -rf "$GO_INSTALL_DIR/go"
  as_root tar -C "$GO_INSTALL_DIR" -xzf "$tmp/$tarball"
  rm -rf "$tmp"

  export PATH="$GO_INSTALL_DIR/go/bin:$PATH"
  GO_BIN="$GO_INSTALL_DIR/go/bin/go"

  # PATH を恒久化（未設定なら ~/.profile に追記）
  local profile="$HOME/.profile"
  if ! grep -qs "$GO_INSTALL_DIR/go/bin" "$profile" 2>/dev/null; then
    echo "export PATH=\"$GO_INSTALL_DIR/go/bin:\$PATH\"" >> "$profile"
    c_ok "PATH を $profile に追記しました（新しいシェルで有効）。"
  fi
}

ensure_go() {
  locate_go
  if [ -n "$GO_BIN" ]; then
    local v; v="$(current_go_version)"
    if [ -n "$v" ] && version_ge "$v" "$MIN_GO_VERSION"; then
      c_ok "Go $v を検出（要件: $MIN_GO_VERSION 以上）"
      return
    fi
    c_warn "Go $v は古すぎます（要件: $MIN_GO_VERSION 以上）。"
  else
    c_warn "Go が見つかりません。"
  fi

  if [ "$SKIP_GO" = "1" ]; then
    die "Go の要件を満たしていません（--skip-go 指定のため中断）。"
  fi
  if confirm "Go ${GO_VERSION} をインストールしますか？"; then
    install_go
    c_ok "Go $(current_go_version) をインストールしました。"
  else
    die "Go が必要です。インストールを中止しました。"
  fi
}

# ---- ffmpeg の確認とインストール ----
ensure_ffmpeg() {
  if command -v ffmpeg >/dev/null 2>&1; then
    c_ok "ffmpeg を検出: $(ffmpeg -version 2>/dev/null | head -n1)"
    return
  fi
  c_warn "ffmpeg が見つかりません（動画サムネイル生成に使用。無くても動作します）。"
  if [ "$SKIP_FFMPEG" = "1" ]; then
    c_warn "--skip-ffmpeg 指定のためスキップします。"
    return
  fi
  if ! command -v apt-get >/dev/null 2>&1; then
    c_warn "apt-get が無いため自動インストールできません。手動で導入してください。"
    return
  fi
  if confirm "ffmpeg を apt でインストールしますか？"; then
    as_root apt-get update
    as_root apt-get install -y ffmpeg
    c_ok "ffmpeg をインストールしました。"
  else
    c_warn "ffmpeg のインストールをスキップしました（動画サムネは無効になります）。"
  fi
}

# ---- ビルド ----
build_binary() {
  c_info "MediaVault をビルドします..."
  "$GO_BIN" build -o "$BINARY_NAME" ./cmd/mediavault
  c_ok "ビルド完了: $REPO_DIR/$BINARY_NAME"
}

# ---- 設定ファイル ----
setup_config() {
  if [ -f "config.yaml" ]; then
    c_ok "config.yaml は既に存在します（変更しません）。"
    return
  fi
  if confirm "config.yaml を config.example.yaml から作成しますか？"; then
    cp config.example.yaml config.yaml
    c_ok "config.yaml を作成しました。media_root を実際のメディアフォルダに編集してください。"

    if confirm "ログイン用パスワードを今すぐ設定しますか？"; then
      read -r -p "ユーザー名 [admin]: " uname </dev/tty || uname=""
      uname="${uname:-admin}"
      # setuser の出力からハッシュを取り出して config.yaml に反映
      local out hash
      out="$(./"$BINARY_NAME" setuser -u "$uname")" || die "パスワード設定に失敗しました"
      echo "$out"
      hash="$(echo "$out" | grep -oE 'password_hash: "[^"]+"' | sed -E 's/password_hash: "(.*)"/\1/')"
      if [ -n "$hash" ]; then
        # auth セクションを書き換え（username/password_hash 行を置換）
        python3 - "$uname" "$hash" <<'PY' 2>/dev/null || true
import re, sys
uname, h = sys.argv[1], sys.argv[2]
p = "config.yaml"
s = open(p, encoding="utf-8").read()
s = re.sub(r'(\n\s*username:\s*).*', r'\g<1>"%s"' % uname, s, count=1)
s = re.sub(r'(\n\s*password_hash:\s*).*', r'\g<1>"%s"' % h, s, count=1)
open(p, "w", encoding="utf-8").write(s)
PY
        c_ok "config.yaml に認証情報を書き込みました。"
      else
        c_warn "ハッシュを自動反映できませんでした。上記を手動で config.yaml に貼り付けてください。"
      fi
    fi
  fi
}

# ---- systemd サービス ----
install_systemd_service() {
  local unit="/etc/systemd/system/mediavault.service"
  local user; user="$(id -un)"
  c_info "systemd サービスを作成します: $unit"
  as_root tee "$unit" >/dev/null <<EOF
[Unit]
Description=MediaVault
After=network.target

[Service]
WorkingDirectory=$REPO_DIR
ExecStart=$REPO_DIR/$BINARY_NAME serve -config $REPO_DIR/config.yaml
Restart=on-failure
User=$user

[Install]
WantedBy=multi-user.target
EOF
  as_root systemctl daemon-reload
  as_root systemctl enable --now mediavault
  c_ok "mediavault サービスを有効化・起動しました（systemctl status mediavault で確認）。"
}

# ---- メイン ----
main() {
  c_info "MediaVault セットアップを開始します（$REPO_DIR）"
  check_os
  ensure_go
  ensure_ffmpeg
  build_binary
  setup_config

  if [ "$INSTALL_SERVICE" = "1" ]; then
    if [ ! -f "config.yaml" ]; then
      c_warn "config.yaml が無いためサービス登録をスキップします。先に作成してください。"
    elif confirm "systemd サービスとして常駐させますか？"; then
      install_systemd_service
    fi
  fi

  echo
  c_ok "セットアップ完了。"
  echo
  echo "起動方法:"
  echo "  cd $REPO_DIR"
  echo "  ./$BINARY_NAME serve -config config.yaml"
  echo
  echo "ブラウザで http://<このマシンのIP>:8080 を開いてください。"
}

main
