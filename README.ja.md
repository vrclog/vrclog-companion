# VRClog Companion

VRChat のローカルログを受動的に読み取り、正規化された Observation ストリームを SQLite へ永続化し、それを World/Presence/Media の状態へ投影するローカル常駐アプリケーション。ワールド内で再生できなかったメディアの元 URL 復元機能を含む。

## アーキテクチャ

VRClog Companion は3つの協調するリポジトリのひとつです。

```text
vrclog-go          正規 Event モデル、Record/Cursor、Engine、ログ Follow/ReadFile
  ← vrclog-adapters コミュニティプロジェクト向け Adapter（YamaPlayer, iwaSync3 等）
    ← vrclog-companion（本リポジトリ）  Adapter 構成、SQLite 永続化、
                                        Projector、HTTP API/SSE、Web UI、通知
```

```text
VRChat output_log
        │
        ▼
vrclog-go Follow → Record
        │
        ▼
Engine（vrchat.core + community adapters）
        │
        ▼
Result { Observations, Diagnostics }
        │
        ▼
Record 単位 SQLite トランザクション（Store.CommitRecord）
  ├─ observations
  ├─ diagnostics
  └─ ingest cursor
        │
        ▼（新規挿入された Observation のみ）
Projector Manager
  ├─ WorldProjector
  ├─ PresenceProjector
  └─ MediaProjector
        │
        ├─ HTTP API / SSE
        ├─ Web UI
        └─ Discord 通知（World/Player のみ）
```

**常にローカルファースト。** クラウドアップロードなし、テレメトリなし。全データはユーザー自身のマシン上の SQLite に保存されます。

## このアプリの目的：メディア URL 復元

VRChat 内蔵の動画プレイヤー（YamaPlayer、iwaSync3 等）は、他の人には正常に再生されているのに自分だけ再生に失敗することがあります。そうした場合でも VRChat のログには元の URL が記録されており、本アプリはそれを抽出・相関付けして、推測や外部メタデータ取得なしにコピーやブラウザでの起動を可能にします。

## 検証済み Adapter

| Adapter | 提供元 | 状態 |
|---------|--------|------|
| `vrchat.core` | vrclog-go（組み込み） | 検証済み |
| `community.yamaplayer` | vrclog-adapters | 実ログフィクスチャで検証済み |
| `community.iwasync3` | vrclog-adapters | 実ログフィクスチャで検証済み |

Adapter はコンパイル時に構成されます（`internal/adapter`）。ランタイムプラグインロード、YAML パターン設定、リモート Adapter カタログはありません。

## 開発環境

- Go 1.25+
- Node.js 20+（Web UI ビルド用）
- Windows 11（ターゲット OS。macOS は開発用にサポート）

## ディレクトリ構成

```text
vrclog-companion/
├── cmd/
│   └── vrclog-companion/  # メインエントリポイント
├── internal/
│   ├── adapter/           # コンパイル時 Adapter 構成
│   ├── api/                # HTTP API サーバー（JSON + SSE + 認証 + レート制限）
│   ├── app/                # ユースケース層
│   ├── config/              # 設定・secrets 管理
│   ├── ingest/              # RecordSource、Record 単位 ingest Runner
│   ├── notify/              # Discord 通知（Change ベース）
│   ├── observation/         # Observation 永続化 DTO
│   ├── projector/           # World/Presence/Media 投影状態
│   ├── sse/                 # generic Observation SSE broadcaster
│   └── store/                # SQLite 永続化（schema v3）
├── web/                    # Web UI (React + Vite)
├── webembed/                # 埋め込み用 Web UI（go:embed）
├── test/
│   ├── integration/          # HTTP API 統合テスト
│   └── e2e/                   # 実フィクスチャによるメディア URL 復元 E2E テスト
├── go.mod
├── SPEC.md                  # 仕様書
├── LICENSE                   # MIT ライセンス
└── README.md
```

## ビルド・実行

### ビルド

```bash
# Web UI ビルド
cd web && npm install && npm run build && cd ..
mkdir -p webembed/dist && cp -r web/dist/* webembed/dist/

# クロスコンパイル（macOS/Linux から Windows 向け）
GOOS=windows GOARCH=amd64 go build -o vrclog.exe ./cmd/vrclog-companion

# ローカル環境向け
go build -o vrclog ./cmd/vrclog-companion
```

### 実行

```bash
# デフォルト（ポート 8080）
./vrclog

# ポート指定
./vrclog -port 9000
```

### 動作確認

```bash
curl http://127.0.0.1:8080/api/v1/health
# {"status":"ok","database":"ok","ingest":"running","last_ingest_error":"","last_record_at":"...","loaded_adapters":3}

# Web UI
# ブラウザで http://127.0.0.1:8080 を開く
```

## API エンドポイント

| Method | Path | 認証（LANモード時） | 説明 |
|--------|------|---------------------|------|
| GET | /api/v1/health | 不要 | ヘルスチェック（status/ingest/adapter数のみ） |
| GET | /api/v1/observations | 必要 | generic Observation 履歴、cursor pagination |
| GET | /api/v1/state | 必要 | 現在のワールド、プレイヤー、最新の再生可能メディア |
| GET | /api/v1/media/recent | 必要 | 最近のメディア再生試行 |
| GET | /api/v1/adapters | 必要 | ロード済み Adapter 一覧 |
| GET | /api/v1/stream | 必要 | generic SSE ストリーム（`event: observation`） |
| POST | /api/v1/auth/token | 必要（Basic Auth のみ） | SSE トークン発行（5分 TTL） |
| GET | /api/v1/config | 必要 | 設定取得（secrets 除外） |
| PUT | /api/v1/config | 必要 | 設定更新 |
| GET | /api/v1/stats/basic | 必要 | 本日の統計 |

これは破壊的刷新です：旧フラット `Event` モデル、`/api/v1/events`、`/api/v1/now`、旧 SQLite スキーマはすべて廃止されました。完全な契約は [SPEC.md](./SPEC.md)、変更点は [CHANGELOG.md](./CHANGELOG.md) を参照してください。

## データベーススキーマのリセット

SQLite スキーマは `PRAGMA user_version`（現在バージョン3）でバージョン管理されています。旧スキーマからの**自動マイグレーションはありません**。スキーマ不一致でアプリが起動を拒否した場合は、アプリを停止し、データベースファイル（アプリのデータディレクトリ内の `vrclog.sqlite`）をリネームまたは削除して新規に開始してください。履歴は失われますが、データ破損は発生しません。

## テスト

```bash
go test ./...
go test -tags=integration ./test/integration/...
go test -tags=e2e ./test/e2e/...
```

## CI

GitHub Actions で Windows / Linux runner 上のテストを自動実行。

- `push` / `pull_request` でトリガー
- Windows でビルド確認

## セキュリティとプライバシー

### ローカルモード（デフォルト）

`127.0.0.1` のみにバインドされます。マシン外から到達できないため、認証は不要かつ提供されません。

### LAN モード

`config.json` で `lan_enabled=true` を設定すると、ローカルネットワーク上の他のデバイスからアクセス可能になります。

- **Basic認証は必須**: LAN モード有効時、Basic認証が自動的に有効化されます
- **初回起動時にパスワード自動生成**: 認証情報未設定の場合、強力なランダムパスワードが生成されデータディレクトリの `generated_password.txt` に保存されます
- **レート制限と認証失敗ロックアウト**も自動的に有効化されます

> **警告**: Basic認証はTLSなしでは盗聴に対する保護がありません。信頼できるローカルネットワーク内でのみ LAN モードを使用してください。信頼できないネットワークで使う必要がある場合は TLS 終端リバースプロキシの利用を検討してください。インターネットへのポート開放は**サポート対象外**です。

### メディア URL は機密情報として扱われます

VRChat ログから復元されるメディア URL には、セッショントークン、プライベートインスタンス識別子、その他非公開コンテンツへのリンクが含まれる場合があります。本アプリは：

- メディア URL を Discord へ送信しません
- 外部メタデータを取得しません（oEmbed なし、サムネイル/タイトル取得なし）
- URL を自動で開きません — 明示的なユーザークリックのみでコピー・オープンが可能です
- ブラウザで開く操作は `http`/`https` スキームのみ許可します

### VRChat プロセス・API との連携なし

本アプリは VRChat のローカルログファイルを読み取るのみです。VRChat プロセスにアタッチせず、VRChat の API を呼び出さず、VRChat を一切変更しません。

## ライセンス

MIT License - 詳細は [LICENSE](./LICENSE) を参照。
