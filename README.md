# MediaVault

HDDに保存した画像・動画を、ラズベリーパイなどで常駐させて **LAN内のブラウザから閲覧** するための軽量Webアプリです。

- **Go製の単一バイナリ**（フロントエンドも同梱）。ビルド工程なしで配布できます。
- **動画はトランスコードせずHTTP Range配信** — mp4 / m4v / webm / mov(H.264) はブラウザのネイティブプレーヤーで再生・シーク。ラズパイのCPU負荷を最小化。
- **avi / wmv / mkv / flv も再生可能** — ブラウザ非対応の形式は再生時に `ffmpeg` で H.264/AAC へ都度トランスコードして配信します（`ffmpeg` が必要。ライブ変換のためシーク不可）。
- **Web漫画ビューア** — フォルダ内の画像を ←/→ キー・タップ・スワイプでめくれます。画面端からの右スワイプで前の画面へ戻れます。
- **フォルダ/ファイルのお気に入り**、**ファイル名・フォルダ名検索**。
- **単一ユーザー認証**（ID＋パスワード）＋ **連続ログイン失敗のIPブロック**。
- フォルダは開いた都度読み込む遅延方式。事前フルスキャン不要（数千ファイル規模を想定）。

## 必要なもの

- Go 1.25 以上（ビルド時のみ）
- `ffmpeg`（**動画サムネイル生成と avi/wmv 等の再生（トランスコード）に使用。無くても動作します** — その場合、動画サムネはアイコン表示になり、avi/wmv 等のブラウザ非対応形式は再生できません）

```bash
sudo apt install ffmpeg   # Raspberry Pi OS / Debian 系
```

## セットアップ

### かんたんセットアップ（ラズパイ推奨）

Go・ffmpeg の確認とインストール、ビルド、設定生成までをまとめて行うスクリプトを用意しています。

```bash
git clone <このリポジトリ> mediavault && cd mediavault
./scripts/setup.sh
```

スクリプトは以下を自動で行います。

- Go（1.25 以上）の有無を確認し、無ければ公式バイナリをアーキテクチャ（arm64/armv6l 等）に合わせてインストール
- `ffmpeg` の有無を確認し、無ければ `apt` でインストール（任意）
- MediaVault のビルド
- `config.yaml` の作成とログインパスワードの設定（任意）

主なオプション:

```bash
./scripts/setup.sh --install-service   # systemd サービスとして常駐登録まで行う
./scripts/setup.sh --yes               # 確認プロンプトを省略
./scripts/setup.sh --skip-go           # Go のインストールをスキップ
./scripts/setup.sh --help              # ヘルプ
```

### 手動セットアップ

```bash
# 1. ビルド
go build -o mediavault ./cmd/mediavault

# 2. 設定ファイルを用意
cp config.example.yaml config.yaml
#   media_root を実際のメディアフォルダに変更

# 3. パスワードを生成して config.yaml の auth に貼り付け
./mediavault setuser -u admin

# 4. 起動
./mediavault serve -config config.yaml
```

ブラウザで `http://<ラズパイのIP>:8080` を開きログインします。

## ラズベリーパイへ（クロスコンパイル）

開発マシンでビルドして転送できます。

```bash
# 64bit Raspberry Pi OS (arm64)
GOOS=linux GOARCH=arm64 go build -o mediavault-arm64 ./cmd/mediavault

# 32bit (armv7)
GOOS=linux GOARCH=arm GOARM=7 go build -o mediavault-armv7 ./cmd/mediavault
```

### ラズパイ起動時に自動で立ち上げる（systemd 常駐）

`./scripts/setup.sh --install-service` を使うと、下記の systemd サービスの作成と
`systemctl enable --now`（**ラズパイ起動時の自動起動**＋即時起動）まで自動で行います。

手動で設定する場合は `/etc/systemd/system/mediavault.service` を作成します。

```ini
[Unit]
Description=MediaVault
# ネットワークと（外付けHDD等の）メディアフォルダのマウント完了後に起動。
After=network-online.target
Wants=network-online.target
RequiresMountsFor=/mnt/hdd/media   # media_root が外付けHDD等にある場合に指定
# 起動時にHDDのマウントが遅れても、回数制限で諦めず再試行し続ける。
StartLimitIntervalSec=0

[Service]
WorkingDirectory=/home/pi/mediavault
ExecStart=/home/pi/mediavault/mediavault serve -config /home/pi/mediavault/config.yaml
Restart=on-failure
RestartSec=5
User=pi

[Install]
WantedBy=multi-user.target
```

```bash
# enable で起動時の自動起動を有効化、--now で今すぐ起動。
sudo systemctl enable --now mediavault
```

> `enable` 済みであれば、以後はラズパイの電源を入れるだけで MediaVault が自動起動します。
> 外付けHDDにメディアを置く場合は `RequiresMountsFor` にそのマウント先を指定すると、
> マウント完了を待ってから起動するため、起動直後の「media_root にアクセスできません」を防げます。

## 自動デプロイ（main の変更を自動反映）

GitHub の `main` ブランチに変更が入ったら、ラズパイ側で自動的に pull → ビルド → 再起動する仕組みを同梱しています。家庭内LAN（NAT配下）でも使えるよう、**ラズパイから定期的に GitHub を確認するプル型**です。

`scripts/deploy.sh` が次を行います。

- `git fetch` で `origin/main` との差分を確認。変更が無ければ即終了。
- 変更があれば `origin/main` に同期し、**一時バイナリにビルド**（失敗しても稼働中のバイナリは無傷）。
- 成功したらバイナリを差し替えてサービスを再起動。
- 再起動後に **ヘルスチェック**（`listen` のポートへHTTP応答確認）。失敗時は**旧バイナリへ自動ロールバック**。
- `flock` で多重実行を防止するため、timer 実行が重なっても安全です。

`config.yaml` / `*.db` / `cache/` は `.gitignore` 済みのため、同期で失われません。

### セットアップ（systemd timer で定期実行）

先に `--install-service` で常駐登録したうえで、自動デプロイを登録します。

```bash
./scripts/setup.sh --install-service --install-auto-deploy
```

オプション:

```bash
./scripts/setup.sh --install-auto-deploy --deploy-branch main --deploy-interval 2min
```

これにより以下の systemd ユニットが作られます。

- `mediavault-deploy.service`（oneshot で `deploy.sh` を実行）
- `mediavault-deploy.timer`（既定 2 分間隔で起動）

非 root 運用では、timer から無人で再起動できるよう `systemctl restart mediavault` のみ許可する sudoers（`/etc/sudoers.d/mediavault-deploy`）を任意で作成します。

```bash
# 状態確認
systemctl list-timers mediavault-deploy.timer
# ログ確認（デプロイの履歴・成否）
journalctl -u mediavault-deploy.service -f
# 手動で1回実行
./scripts/deploy.sh --branch main
```

> ポーリング間隔ぶんの反映遅延（既定で最大2分）があります。push と同時にデプロイしたい、ラズパイのビルド負荷を避けたい場合は、GitHub Actions でのクロスコンパイル配布やセルフホストランナー方式への拡張も可能です。

## コマンド

| コマンド | 説明 |
|---|---|
| `mediavault serve [-config config.yaml]` | サーバを起動 |
| `mediavault setuser [-u ユーザー名]` | パスワードを対話入力し bcrypt ハッシュを出力 |

## セキュリティについて

- LAN内利用を前提とした素のHTTPです。インターネット公開や暗号化が必要な場合は、Caddy / nginx などのリバースプロキシでHTTPS終端し、`trust_proxy: true` を設定してください。
- メディアルート外へのパスアクセス（ディレクトリトラバーサル）はサーバ側で拒否します。
- ログインに `ipblock.max_failures` 回連続で失敗したIPは `ipblock.block_minutes` 分ブロックされます。

## 構成

```
cmd/mediavault     エントリポイント（serve / setuser）
internal/config    設定読込
internal/store     SQLite（お気に入り・セッション・ログイン試行）
internal/auth      認証・セッション・IPブロック
internal/media     パス安全化・フォルダ列挙・検索
internal/thumb     サムネ生成（画像=Go / 動画=ffmpeg）＋ディスクキャッシュ
internal/server    HTTPルーティング・APIハンドラ
web                同梱フロントエンド（HTML/CSS/JS）
```
