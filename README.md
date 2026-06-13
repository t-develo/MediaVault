# MediaVault

HDDに保存した画像・動画を、ラズベリーパイなどで常駐させて **LAN内のブラウザから閲覧** するための軽量Webアプリです。

- **Go製の単一バイナリ**（フロントエンドも同梱）。ビルド工程なしで配布できます。
- **動画はトランスコードせずHTTP Range配信** — ブラウザのネイティブプレーヤーで再生・シーク。ラズパイのCPU負荷を最小化。
- **Web漫画ビューア** — フォルダ内の画像を ←/→ キー・タップ・スワイプでめくれます。
- **フォルダ/ファイルのお気に入り**、**ファイル名・フォルダ名検索**。
- **単一ユーザー認証**（ID＋パスワード）＋ **連続ログイン失敗のIPブロック**。
- フォルダは開いた都度読み込む遅延方式。事前フルスキャン不要（数千ファイル規模を想定）。

## 必要なもの

- Go 1.25 以上（ビルド時のみ）
- `ffmpeg`（**動画サムネイル生成に使用。無くても動作します** — その場合は動画にアイコンを表示）

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

### systemd で常駐させる例

`/etc/systemd/system/mediavault.service`:

```ini
[Unit]
Description=MediaVault
After=network.target

[Service]
WorkingDirectory=/home/pi/mediavault
ExecStart=/home/pi/mediavault/mediavault serve -config /home/pi/mediavault/config.yaml
Restart=on-failure
User=pi

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now mediavault
```

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
