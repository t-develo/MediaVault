package web

import "embed"

// Assets はフロントエンドの静的ファイルをバイナリに同梱する。
//
//go:embed index.html app.js style.css
var Assets embed.FS
