# VRClog Companion 仕様書

作成日: 2026-08-19
仕様状態: Normative（`CLAUDE_IMPLEMENTATION_SPEC.md` に基づく全面刷新後の製品仕様）

このドキュメントは実装後の現行仕様である。旧 Event モデル・旧 SQLite スキーマ・旧 API との互換性はない。

---

## 0. プロジェクト識別子

- **GitHub リポジトリ名**: `vrclog-companion`
- **Go module path**: `github.com/vrclog/vrclog-companion`
- **配布バイナリ（Windows）**: `vrclog.exe`
- **アプリ表示名**: VRClog Companion（短縮: VRClog）

---

## 1. 概要

### 1.1 目的

VRClog Companion は VRChat のローカルログを受動的に監視し、`vrclog-go` の正規 `Observation` として **ユーザー PC 内の SQLite にのみ永続化** する。その Observation ストリームを World / Presence / Media の利用者向け状態へ投影（Projector）し、ローカル HTTP API + Web UI で提供する。ワールド内で再生できなかったメディアの元 URL を復元し、コピー・ブラウザ起動できることが主要なユースケースである。

### 1.2 基本方針

- 中央サーバー・クラウドアップロード・テレメトリは一切ない
- データはユーザー PC 内にのみ保存される
- LAN アクセスは提供するが、安全側デフォルト（loopback bind, LAN 時は Basic Auth 必須）で意図しない公開を防ぐ
- UI はブラウザのみ配布（Web UI を go:embed で同梱）
- ライブ更新は SSE
- VRChat プロセス・API とは一切連携しない（ログファイルの読み取りのみ）

### 1.3 対応 OS

- Windows 11（本番ターゲット）
- macOS（開発用）

---

## 2. 3リポジトリ契約

```text
vrclog-go          canonical Event, Record/Cursor, Engine, Follow/ReadFile
  ← vrclog-adapters community Adapter（YamaPlayer, iwaSync3）
    ← vrclog-companion（本リポジトリ）
```

Companion だけが所有する責務:

- Adapter 構成（compile-time）
- Record 単位 ingest supervision
- SQLite スキーマと CommitRecord トランザクション
- Projector（World/Presence/Media）と通知ポリシー
- HTTP API / SSE / Web UI

Companion は `vrclog-go` / `vrclog-adapters` の公開契約のみを使用する。独自 Parser・独自 Event 型・独自 Adapter interface は持たない。

---

## 3. データフロー

```text
VRChat output_log
        │
        ▼
vrclog.Follow(ctx, FollowConfig{Cursor}) → iter.Seq2[Record, error]
        │
        ▼
Engine.Process(record) → Result{ Observations, Diagnostics }
        │
        ▼
Store.CommitRecord（単一 SQLite トランザクション）
  ├─ observations INSERT（重複検知）
  ├─ diagnostics INSERT OR IGNORE
  └─ ingest_cursors UPSERT
        │  COMMIT
        ▼ （新規挿入された Observation のみ）
Projector Manager.Apply(obs)
  ├─ WorldProjector
  ├─ PresenceProjector
  └─ MediaProjector
        │
        ├─ SSE Broadcaster（generic `observation` event）
        └─ Notifier（World/Player の Change のみ Discord へ）
```

---

## 4. Adapter 構成

`internal/adapter.BuildEngine()` がコンパイル時に Engine を構成する。

```go
core := vrclog.NewVRChatAdapter()
community := []vrclog.Adapter{
    yamaplayer.New(), // github.com/vrclog/vrclog-adapters/yamaplayer
    iwasync3.New(),   // github.com/vrclog/vrclog-adapters/iwasync3
}
all := append([]vrclog.Adapter{core}, community...)
engine, err := vrclog.NewEngine(all...)
```

- built-in（`vrchat.core`）を先頭に固定
- community adapter は `yamaplayer` → `iwasync3` の順に明示的に列挙する（vrclog-adapters にルート集約 API はない）
- global init registry・実行時プラグイン読み込み・YAML パターン設定は存在しない

現在ロードされる Adapter:

| ID | Origin |
|----|--------|
| `vrchat.core` | core |
| `community.yamaplayer` | community |
| `community.iwasync3` | community |

`GET /api/v1/adapters` で参照可能。

---

## 5. Ingest パイプライン

### 5.1 RecordSource

```go
type RecordSource interface {
    Records(ctx context.Context) iter.Seq2[vrclog.Record, error]
}

type RecordSourceFactory interface {
    NewSource(ctx context.Context, cursor *vrclog.Cursor) (RecordSource, error)
}
```

`VRChatSourceFactory` は `vrclog.Follow` を薄くラップする。カーソル付きで `ErrCursorSourceMissing` が発生した場合、警告を一度だけログ出力し、カーソルなしで再開始する（ループしない）。

### 5.2 Runner

`internal/ingest.Runner` が per-Record トランザクションループを駆動する。

```text
for record, err := range source.Records(ctx) {
    result := engine.Process(record)
    for {
        commitResult, err := store.CommitRecord(ctx, RecordCommit{record, result, now})
        if err == nil { break }
        // bounded backoff (1s〜30s) でリトライ。次 Record へは進まない。
    }
    for obs := range commitResult.InsertedObservations {
        onInsert(ctx, obs) // Projector.Apply → SSE broadcast → 通知
    }
}
```

- **不変条件**: Observation/Diagnostic の保存と cursor 更新は同一トランザクションでコミットされる
- 0 Observation の Record でも cursor は前進する
- DB エラー時は同じ Record を bounded backoff でリトライし、次 Record を消費しない
- Source 致命的エラー時は、最後にコミットされた cursor から `RecordSourceFactory` 経由で source を再構築する（bounded backoff, 1s〜30s）

### 5.3 Duplicate 判定

Observation identity は `vrclog.ObservationID` のみで判定する。raw line ハッシュや URL 正規化による重複排除は行わない。

同一 ID が既に存在する場合、以下 9 フィールドを比較する:

`occurred_at, type, payload_json, adapter_id, rule_id, record_id, source_id, source_offset, source_line`

（`sequence`, `ingested_at` は除外）

- 完全一致 → 既知の重複として無視（cursor は前進）
- 不一致 → `ErrObservationConflict` でトランザクション全体をロールバック（cursor は前進しない）

---

## 6. SQLite スキーマ（version 3）

`PRAGMA user_version` で管理する。**自動マイグレーションはない。**

version 3 はテーブル構造の変更ではなく、vrclog-go の Observation payload 契約が厳格化された（`MediaTarget.Backend` が必須化）ことを示す。version 2 の DB は旧契約で保存された payload を含む可能性があり、そのまま使うと Projector rebuild 時にデコードエラーで fatal クラッシュしうるため、起動時に明示的に拒否する。

| 検出状態 | 挙動 |
|---------|------|
| `user_version == 3` | テーブル存在検証後に利用 |
| `user_version == 0`、旧テーブルなし | schema 3 を新規作成 |
| `user_version == 0`、旧テーブルあり（`events`/`ingest_cursor`/`parse_failures`） | fatal `ErrUnsupportedSchema` |
| `user_version == 2`（旧 payload 契約） | fatal `ErrUnsupportedSchema` |
| それ以外のバージョン | fatal `ErrUnsupportedSchema` |

fatal 時はアプリを停止し、DB ファイルをリネームまたは削除して再作成する。

```sql
CREATE TABLE observations (
    sequence       INTEGER PRIMARY KEY AUTOINCREMENT,
    id             TEXT NOT NULL UNIQUE,
    occurred_at    TEXT NOT NULL,
    type           TEXT NOT NULL,
    payload_json   TEXT NOT NULL,
    adapter_id     TEXT NOT NULL,
    rule_id        TEXT NOT NULL,
    record_id      TEXT NOT NULL,
    source_id      TEXT NOT NULL,
    source_offset  INTEGER NOT NULL,
    source_line    INTEGER NOT NULL,
    ingested_at    TEXT NOT NULL
);

CREATE TABLE ingest_cursors (
    source_id     TEXT PRIMARY KEY,
    path          TEXT NOT NULL,
    byte_offset   INTEGER NOT NULL,
    line_number   INTEGER NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE TABLE diagnostics (
    id             TEXT PRIMARY KEY,
    record_id      TEXT NOT NULL,
    source_id      TEXT NOT NULL,
    source_offset  INTEGER NOT NULL,
    source_line    INTEGER NOT NULL,
    adapter_id     TEXT,
    rule_id        TEXT,
    code           TEXT NOT NULL,
    message        TEXT NOT NULL,
    created_at     TEXT NOT NULL
);
```

- `ingest_cursors.path` は再開処理専用であり、Observation API には一切出さない
- raw line は既定で保存しない（Diagnostic の message も URL 除去 + 512byte 上限で redact）
- WAL mode, busy_timeout 5s, `_txlock=immediate`（CommitRecord が確実に書き込みロックを取得する）

---

## 7. Projector

Observation は永続化された事実、Projector はそれを決定的に投影する派生状態。DB から常に再構築可能。

### 7.1 Manager 適用順序

`Manager.Apply(obs)` は以下の順序を厳守する:

1. World transition（`world.joining_observed` の場合）
2. Presence reset（definitive transition の場合のみ）
3. Media session reset（同上）
4. Change 一覧を返却

`Manager.Rebuild(ctx, allObservations)` は起動時に sequence 昇順で全 Observation を再生し、Change 発行を抑制する（通知・SSE を発生させない）。

### 7.2 WorldProjector

- `world.joining_observed` が definitive transition。同一 world ID + instance ID は重複として無視
- `world.entering_observed` の名前は pending として保持し、15 秒以内（`OccurredAt` 基準、wall clock ではない）の joining と merge する
- entering → joining, joining → entering のどちらの順序でも同じ最終状態になる
- definitive transition のみ `WorldChanged` を発行し、Presence をリセットする

### 7.3 PresenceProjector

- キーは Player.ID が非空ならそれ、空なら trim 済み DisplayName
- 重複 join は no-op、未知の left は no-op
- World transition による reset では `PlayerLeft` を発行しない（離脱通知はしない）

### 7.4 MediaProjector

初期 status は `observed` / `failed` のみ（`playing` は判定材料がないため作らない）。

**BestOpenableURL 優先順位**: `source` > `resolver_input` > `playback_input` > なし。`resolved`（signed CDN URL 等）は対象外。同一優先度内では最初に観測された URL を維持する。

**Correlation（相関付け）優先順位**:

1. exact target（component + key 完全一致。key が空の場合は対象外）
2. exact URL 遷移（`resource.resolved` の input/output URL）
3. exact resource URL 一致
4. 単一の曖昧でない直近候補（10 秒以内、同一 world session、target 競合なし。`<=` で境界を含む）
5. 新規 Attempt（0 件または複数曖昧 → 分離を優先）

World transition を跨いだ correlation は行わない（`currentWorldInstanceID` でスコープ）。直近履歴（最大 50 件）は世代を跨いで保持される。

`LatestOpenableMedia` は世代を問わず直近の BestOpenableURL 保持 Attempt を返す（failed でも対象）。

---

## 8. API

Base path: `/api/v1`

### 8.1 認証

| Endpoint | Loopback | LAN モード |
|----------|----------|-----------|
| `GET /health` | 不要 | 不要 |
| その他すべて | 不要 | Basic Auth 必須（`/stream` は SSE token も可） |

`/auth/token` は Basic Auth のみ受理する（SSE token での自己更新は不可）。

起動直後の Projector rebuild 中は `/health` 以外すべて `503 {"status":"rebuilding"}` を返す。

### 8.2 `GET /api/v1/health`

```json
{
  "status": "ok | degraded",
  "database": "ok | error",
  "ingest": "running | retrying | stopped | rebuilding",
  "last_ingest_error": "",
  "last_record_at": "",
  "loaded_adapters": 3
}
```

secret・path・URL は一切含まない。

### 8.3 `GET /api/v1/observations`

Query: `cursor`（sequence）, `limit`（1-500, default 100）, `type`（exact）, `adapter_id`（exact）, `since`, `until`（RFC3339）

デフォルト順序は sequence 降順。`type`/`adapter_id` はアローリストを使わず SQL bind parameter で照合する。

```json
{
  "items": [{
    "sequence": 42,
    "id": "...",
    "occurred_at": "...",
    "type": "resource.url_observed",
    "payload": {},
    "adapter_id": "community.yamaplayer",
    "rule_id": "youtube_resolve_url",
    "record": { "id": "...", "source_id": "...", "offset": 1234, "line": 52 },
    "ingested_at": "..."
  }],
  "next_cursor": 41
}
```

`next_cursor` は該当なしでも常にキーとして存在し、値は `null`（フィールド省略はしない — クライアントの `!== null` 判定を壊すため）。local path・raw line は含まない。

### 8.4 `GET /api/v1/state`

```json
{
  "world": { "id": "...", "name": "...", "instance_id": "...", "joined_at": "..." },
  "players": [{ "id": "...", "display_name": "...", "joined_at": "..." }],
  "latest_openable_media": { "attempt_id": "...", "url": "...", "status": "failed", "observed_at": "..." }
}
```

### 8.5 `GET /api/v1/media/recent`

Query: `limit`（1-50, default 20）。新しい順で MediaAttempt を返す。

### 8.6 `GET /api/v1/adapters`

```json
{ "adapters": [{ "id": "vrchat.core", "origin": "core" }] }
```

### 8.7 `GET /api/v1/stats/basic`, `GET/PUT /api/v1/config`, `POST /api/v1/auth/token`

既存パターンを維持。Stats は observations テーブルの集計と Projector Manager の直近メディア件数から算出する。

---

## 9. SSE

`GET /api/v1/stream` は単一イベント種別のみ送信する。

```text
id: <observation-id>
event: observation
data: <observation API JSON>
```

type ごとに SSE event 名は分けない。

### 9.1 Last-Event-ID recovery

1. Broadcaster に subscribe
2. 現在の high-water sequence を取得
3. Last-Event-ID から sequence を解決できなければ `event: reset`（`id:` 空）を送信して切断
4. `(lastSeq, highWater]` の Observation を DB から backlog 送信
5. 以降は live channel から `sequence > lastSent` のみ送信（dedup）

subscribe と high-water 取得の間にコミットされた Observation は backlog か live channel のいずれかで必ずカバーされる。

### 9.2 Backpressure

per-client バッファが溢れた場合は ingest をブロックせず切断する。クライアントは Last-Event-ID で再接続して復旧する。

---

## 10. 通知（Discord）

対象: definitive `WorldChanged`, `PlayerJoined`, `PlayerLeft` の Change のみ。

非対象: `WorldNameUpdated`, `MediaAttemptUpdated`, startup rebuild 中の Change, duplicate。

**サニタイズ**: 送信ペイロードは常に `allowed_mentions: {"parse": []}` を含める。プレイヤー名・ワールド名は Markdown 制御文字をエスケープし、`@everyone`/`@here`/`<@id>` のメンショントリガーと `http(s)://` の自動リンクをゼロ幅スペースで無害化する。

メディア URL は一切送信しない。

---

## 11. セキュリティ・プライバシー

- デフォルトは loopback bind。LAN モードのみ Basic Auth + rate limit + auth failure lockout + CSRF protection を有効化
- Basic Auth は TLS なしでは盗聴保護がないため、LAN モードは信頼できるネットワークでのみ使用する
- Diagnostics の message は DB 保存前・API 応答前の二重で redact する（URL 除去、512byte 上限）
- Media URL は Discord へ送信しない、外部メタデータを取得しない、自動で開かない
- ブラウザで開く操作は `http`/`https` スキームのみ許可
- raw log line は DB にも API にも出さない

---

## 12. テスト方針

- `internal/store`: schema 検証、CommitRecord 原子性、query
- `internal/ingest`: Runner の DB/source リトライ、cursor missing fallback、VRChatSource 統合
- `internal/projector`: World 二段階 merge、Presence、Media correlation（YamaPlayer/iwaSync3 シナリオ、境界値）
- `internal/api`, `internal/sse`: ルーティング、認証、SSE backlog/race
- `test/integration`: 実 SQLite + 実 HTTP サーバーでの統合テスト
- `test/e2e`: `vrclog-adapters` の実フィクスチャを通した media URL recovery E2E

---

## 13. ビルド

```bash
gofmt -w .
go test ./...
go test -race ./...
go vet ./...
GOOS=windows GOARCH=amd64 go build ./cmd/vrclog-companion/

cd web && npm ci && npm run lint && npm run build
```
