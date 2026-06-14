#!/usr/bin/env bash
#
# MediaVault 自動デプロイスクリプト（プル型ポーリング）
#
# GitHub の対象ブランチ（既定 main）に更新があれば pull → build → 再起動します。
# systemd timer から定期実行することを想定。更新が無ければ何もせず終了します。
#
# 安全策:
#   - flock で多重実行を防止
#   - 一時バイナリにビルドし、成功した時だけ差し替え
#   - 再起動後にヘルスチェック。失敗したら旧バイナリへロールバック
#
# 使い方:
#   ./scripts/deploy.sh                      # origin/main を対象に1回実行
#   ./scripts/deploy.sh --branch main        # 対象ブランチを指定
#   ./scripts/deploy.sh --service mediavault # 再起動する systemd サービス名
#   ./scripts/deploy.sh --no-restart         # ビルドまで（再起動しない）
#   ./scripts/deploy.sh --force              # 差分が無くてもビルド・再起動
#   ./scripts/deploy.sh --help
#
set -euo pipefail

# ---- 設定（既定値） ----
BRANCH="main"
SERVICE="mediavault"
BINARY_NAME="mediavault"
DO_RESTART=1
FORCE=0
HEALTH_RETRIES=15        # ヘルスチェックの試行回数
HEALTH_INTERVAL=1        # 試行間隔（秒）

# ---- フラグ ----
while [ $# -gt 0 ]; do
  case "$1" in
    --branch)  BRANCH="${2:?--branch には値が必要です}"; shift ;;
    --service) SERVICE="${2:?--service には値が必要です}"; shift ;;
    --no-restart) DO_RESTART=0 ;;
    --force) FORCE=1 ;;
    -h|--help)
      awk 'NR==1{next} /^#/{sub(/^#[[:space:]]?/,"");print;next} {exit}' "$0"; exit 0 ;;
    *) echo "不明なオプション: $1" >&2; exit 1 ;;
  esac
  shift
done

# ---- 表示ヘルパ（タイムスタンプ付き。journalctl で追いやすく） ----
log()  { printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*"; }
err()  { log "ERROR: $*" >&2; }
die()  { err "$*"; exit 1; }

# ---- リポジトリのルートへ移動 ----
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_DIR"
[ -f "go.mod" ] || die "go.mod が見つかりません。MediaVault のリポジトリ内で実行してください。"

# ---- 多重実行防止（flock）。timer 実行が重ならないように ----
LOCK_FILE="$REPO_DIR/.deploy.lock"
exec 9>"$LOCK_FILE"
if ! flock -n 9; then
  log "別のデプロイが実行中のためスキップします。"
  exit 0
fi

# ---- root 権限が必要なコマンド（systemctl）に sudo を付与 ----
SUDO=""
if [ "$(id -u)" -ne 0 ] && command -v sudo >/dev/null 2>&1; then
  SUDO="sudo"
fi

# ---- go コマンドの解決（PATH に無くても /usr/local/go を見る） ----
GO_BIN=""
if command -v go >/dev/null 2>&1; then
  GO_BIN="$(command -v go)"
elif [ -x "/usr/local/go/bin/go" ]; then
  GO_BIN="/usr/local/go/bin/go"
fi
[ -n "$GO_BIN" ] || die "go コマンドが見つかりません。"

# ---- 更新の検知 ----
log "origin/$BRANCH の更新を確認します..."
git fetch --quiet origin "$BRANCH" || die "git fetch に失敗しました。"

LOCAL="$(git rev-parse HEAD)"
REMOTE="$(git rev-parse "origin/$BRANCH")"

if [ "$LOCAL" = "$REMOTE" ] && [ "$FORCE" != "1" ]; then
  log "更新はありません（$LOCAL）。終了します。"
  exit 0
fi

if [ "$FORCE" = "1" ]; then
  log "--force 指定のため、差分有無に関わらず実行します。"
else
  log "更新を検知: $LOCAL -> $REMOTE"
fi

# ---- ソース更新（デプロイ専用なのでリモートに強制同期） ----
# config.yaml / *.db / cache/ は .gitignore 済みのため失われません。
git reset --hard "origin/$BRANCH" || die "git reset に失敗しました。"
log "ソースを origin/$BRANCH に同期しました。"

# ---- 一時バイナリにビルド（既存バイナリは壊さない） ----
log "ビルドします..."
if ! "$GO_BIN" build -o "${BINARY_NAME}.new" ./cmd/mediavault; then
  die "ビルドに失敗しました。稼働中のバイナリは変更していません。"
fi
log "ビルド成功。"

# ---- バイナリ差し替え（旧バイナリは .bak に退避） ----
if [ -f "$BINARY_NAME" ]; then
  cp -f "$BINARY_NAME" "${BINARY_NAME}.bak"
fi
mv -f "${BINARY_NAME}.new" "$BINARY_NAME"
log "バイナリを差し替えました。"

if [ "$DO_RESTART" != "1" ]; then
  log "--no-restart 指定のため再起動しません。完了。"
  exit 0
fi

# ---- ヘルスチェック用の URL を config.yaml の listen から組み立て ----
health_url() {
  local listen port
  listen="$(grep -oE '^[[:space:]]*listen:[[:space:]]*"?[^"#]+' config.yaml 2>/dev/null \
            | head -n1 | sed -E 's/.*listen:[[:space:]]*"?//; s/[[:space:]]*$//')"
  port="${listen##*:}"
  [ -n "$port" ] || port="8080"
  echo "http://127.0.0.1:${port}/"
}

# ---- 再起動 ----
restart_service() {
  log "サービスを再起動します: $SERVICE"
  $SUDO systemctl restart "$SERVICE"
}

# ---- ヘルスチェック（HTTP 応答が返れば成功。401/302 等でも稼働とみなす） ----
check_health() {
  command -v curl >/dev/null 2>&1 || { log "curl が無いためヘルスチェックを省略します。"; return 0; }
  local url; url="$(health_url)"
  local i
  for ((i=1; i<=HEALTH_RETRIES; i++)); do
    if curl -fsS -o /dev/null --max-time 3 "$url" \
       || [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$url")" -ge 200 ] 2>/dev/null; then
      log "ヘルスチェック OK（$url, 試行 $i）"
      return 0
    fi
    sleep "$HEALTH_INTERVAL"
  done
  err "ヘルスチェック失敗（$url, ${HEALTH_RETRIES}回）"
  return 1
}

# ---- ロールバック ----
rollback() {
  if [ ! -f "${BINARY_NAME}.bak" ]; then
    err "ロールバック用の旧バイナリ（${BINARY_NAME}.bak）がありません。"
    return 1
  fi
  err "ロールバックします。"
  mv -f "${BINARY_NAME}.bak" "$BINARY_NAME"
  $SUDO systemctl restart "$SERVICE" || true
  if check_health; then
    log "ロールバック後のヘルスチェック OK。"
  else
    err "ロールバック後もヘルスチェックに失敗しました。手動確認が必要です。"
  fi
}

restart_service
if check_health; then
  rm -f "${BINARY_NAME}.bak"
  log "デプロイ完了: $REMOTE"
else
  rollback
  exit 1
fi
