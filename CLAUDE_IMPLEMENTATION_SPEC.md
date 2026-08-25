# vrclog-companion 全面刷新 実装仕様書

作成日: 2026-08-18  
対象リポジトリ: `github.com/vrclog/vrclog-companion`  
対象実装者: Claude Code  
仕様状態: **Normative / 全面刷新**

---

## 0. Claude Codeへの最上位指示

この文書を、対象リポジトリにおける最上位の実装仕様として扱うこと。

- 現行event model、現行ingest model、現行SQLite schema、現行API response、現行SSE event nameとの互換性は不要である。
- 旧DB migration、旧endpoint alias、旧JSON field、deprecated wrapper、dual-read/dual-writeを実装しないこと。
- 現行の有用なアプリ特性は残してよいが、旧event architectureを温存する理由にはしないこと。
- 現行README、SPEC、CLAUDE.md、ADRと本仕様が矛盾する場合、本仕様を優先し、実装完了時に古い文書を更新または削除すること。
- `vrclog-go` と `vrclog-adapters` の公開契約を使い、Companion内に代替Parser、代替Event型、独自Adapter interfaceを作らないこと。
- 実装をフェーズ単位で進め、各フェーズのtestと完了条件を満たすこと。
- 最後にGo全体test/race/vet、Windows build、frontend lint/build、E2Eを実行すること。

---

# 1. このリポジトリの役割

`vrclog-companion` は、`vrclog-go` と `vrclog-adapters` を組み立て、VRChatのlocal output logから得たcanonical ObservationをSQLiteへ保存し、それらをPlayer/World/Mediaの利用者向け状態へ投影し、HTTP API、SSE、Web UI、通知として提供するローカルアプリケーションである。

```text
VRChat output_log
        │
        ▼
vrclog-go Follow → Record
        │
        ▼
Engine
  ├─ vrchat.core
  ├─ community.yamaplayer
  └─ community.iwasync3
        │
        ▼
Result { Observations, Diagnostics }
        │
        ▼
per-Record SQLite transaction
  ├─ observations
  ├─ diagnostics
  └─ ingest cursor
        │
        ▼
Projector Manager
  ├─ WorldProjector
  ├─ PresenceProjector
  └─ MediaProjector
        │
        ├─ API
        ├─ SSE
        ├─ Web UI
        └─ Discord notifications（Player/Worldのみ）
```

---

# 2. 3リポジトリ契約

依存方向:

```text
vrclog-go ← vrclog-adapters ← vrclog-companion
      └───────────────────────────────↑
```

## 2.1 `vrclog-go`

所有:

- `Record`
- `Cursor`
- `ReadFile` / `Follow`
- canonical sealed `Event`
- event codec
- `Adapter`
- `Engine`
- `Observation`
- built-in `NewVRChatAdapter()`

## 2.2 `vrclog-adapters`

所有:

- community Adapter constructor（`yamaplayer.New()`, `iwasync3.New()` 等、サブパッケージ単位）
- YamaPlayer / iwaSync3等の実ログfixture

## 2.3 Companion

本リポジトリだけが所有:

- Adapter構成
- Cursor選択とingest supervision
- Record単位transaction
- SQLite schema
- Projectorとsemantic correlation
- API/SSE/UI
- notification policy
- browser link UX

## 2.4 ローカル同時開発

3repoを兄弟directoryで開発する場合、repo外の一時Go workspaceを使ってよい。

```bash
go work init ./vrclog-go ./vrclog-adapters ./vrclog-companion
```

commit禁止:

- local `go.work`
- local path `replace`
- 上流APIのcopy実装

正式commitではreleased module versionを参照する。

---

# 3. 維持する価値のある現行特性

旧event architectureを置換したうえで、次の製品特性は維持してよい。

- local-first
- cloud uploadを前提としない
- SQLite WAL
- local HTTP API
- embedded Web UI
- loopback mode
- LAN modeと認証
- single-instance behavior
- configuration/secrets handling
- Player/World Discord notifications
- health/status
- statistics UI

ただし、これらの現行実装が旧Event型へ強く結合している場合は、新Observation/Projector architectureへ作り直す。

互換性が不要であるため、現行config fieldやrouteを無理に残す必要はない。セキュリティ上有用な設定は、新しいclean schemaへ必要なものだけ移す。

---

# 4. 完全に廃止する現行概念

以下は削除する。

- Companion独自の旧flat `Event`
- `world_join` / `player_join` / `player_left` だけを前提にしたclosed type model
- `vrclog-go.Event.Data` を落とす変換層
- `RawLine` SHA-256だけによるevent dedupe
- event channel / error channelを包むingest source
- 時刻ベースのreplay option
- 現行`derive.State`の単一switch
- World Join行ごとに無条件でPlayerをclearする挙動
- event typeの3種類allowlist
- typeごとにSSE event nameを変える設計
- old `/events` responseのflat player/world fields
- old `/now` state contract
- old SQLite `events` table
- old `meta_json`
- old `parse_failures`
- 未使用・future扱いのcursor設計
- old DB migration
- old endpoint alias
- old module path `github.com/graaaaa/vrclog-companion`
- Parser/YAML/pattern設定
- CurrentVideo単一値だけのモデル
- media URLの自動通知・自動open

---

# 5. Moduleとpackage構成

## 5.1 Module path

`go.mod` を次へ変更する。

```text
module github.com/vrclog/vrclog-companion
```

内部importを全て更新する。旧module pathのaliasやreplaceを残さない。

## 5.2 推奨構成

既存のアプリ構造を調査しつつ、最終的な責務を次へ寄せる。

```text
vrclog-companion/
├── cmd/
│   └── vrclog-companion/
├── internal/
│   ├── app/
│   ├── source/
│   │   └── vrchat.go
│   ├── ingest/
│   │   ├── runner.go
│   │   └── status.go
│   ├── observation/
│   │   ├── model.go
│   │   └── convert.go
│   ├── store/
│   │   ├── store.go
│   │   ├── schema.go
│   │   ├── observations.go
│   │   ├── cursors.go
│   │   └── diagnostics.go
│   ├── projector/
│   │   ├── manager.go
│   │   ├── world.go
│   │   ├── presence.go
│   │   ├── media.go
│   │   └── types.go
│   ├── api/
│   ├── sse/
│   ├── notify/
│   ├── config/
│   ├── security/
│   └── stats/
├── web/
├── README.md
├── SPEC.md
├── CLAUDE.md
├── CHANGELOG.md
└── go.mod
```

既存package名を無理に全て合わせる必要はないが、次の責務境界を崩さない。

- source = Record stream
- ingest = orchestration
- observation = persistence DTO
- store = transaction/schema/query
- projector = derived state
- api/sse = transport
- notify = projected change policy

`internal/event` は最終的に削除する。canonical Eventは `vrclog-go` が所有する。

---

# 6. Adapter構成

アプリ起動時にcompile-timeで明示的に組み立てる。

```go
community := []vrclog.Adapter{
    yamaplayer.New(),
    iwasync3.New(),
}

all := make([]vrclog.Adapter, 0, 1+len(community))
all = append(all, vrclog.NewVRChatAdapter())
all = append(all, community...)

engine, err := vrclog.NewEngine(all...)
```

要件:

- built-inを先頭にする。
- community adapterは `yamaplayer` → `iwasync3` の順に明示的に列挙する（vrclog-adaptersにルート集約APIはない）。
- global `init()` registryを使わない。
- runtime plugin discoveryをしない。
- configからGo package名をロードしない。
- YAML/regex pattern directoryを読まない。
- loaded Adapter ID一覧をapp stateへ保持し、API/UIへ表示可能にする。

Adapter表示用metadataは最小限にする。

```go
type LoadedAdapter struct {
    ID     string `json:"id"`
    Origin string `json:"origin"` // "core" or "community"
}
```

Adapter version catalog、runtime compatibility判定、remote update状態は作らない。

---

# 7. Source契約

## 7.1 interface

Companion内のRecord source abstractionは次の程度にする。

```go
type RecordSource interface {
    Records(ctx context.Context) iter.Seq2[vrclog.Record, error]
}
```

旧EventSourceやchannel wrapperは削除する。

## 7.2 VRChatSource

`vrclog.Follow` を薄く包む。

入力:

- configured log directory（optional）
- Storeから取得したlatest cursor
- poll interval（必要ならconfig）

挙動:

1. Storeから最終更新cursorを一件取得する。
2. cursorありでFollowを開始する。
3. `vrclog.ErrCursorSourceMissing` がRecord前に返った場合、Diagnostic/health warningを記録する。
4. cursorなしでFollowを一度だけ再開始する。
5. それ以外のfatal source errorはingest supervisorへ返す。

Cursor missing fallbackをloopさせない。

## 7.3 Cursorの選択

rotationによりSourceIDはfileごとに変わるため、Storeは全source cursorのうち `updated_at` が最新のものを返す。

```go
func (s *Store) LatestCursor(ctx context.Context) (*vrclog.Cursor, error)
```

Pathはcursor tableに必要である。Observation tableへPathを保存しない。

## 7.4 Replay

- `last 5 minutes`
- `last 24 hours`
- `last N lines`

といったtime-based replayは廃止する。

Cursorが唯一のresume mechanismである。

初回cursorなしの場合、`vrclog.Follow` の仕様どおり最新fileをoffset 0から読む。

---

# 8. Ingest pipeline

## 8.1 Record単位処理

一つのRecordについて、次の順序を厳守する。

```text
Record
  ↓
Engine.Process
  ↓
Result { Observations, Diagnostics }
  ↓
ONE SQLite transaction
  ├─ observationsをinsert
  ├─ diagnosticsをinsert
  └─ Record.Cursor()をupsert
  ↓ COMMIT
newly inserted observations only
  ↓
Projectors.Apply
  ↓
SSE / notifications / in-memory state
```

## 8.2 Atomicity

最重要invariant:

> Observation/Diagnosticの保存とcursor更新は、同一Recordについて同じtransactionでcommitする。

禁止:

- cursorを先にupdateする
- observationsを保存後、別transactionでcursorをupdateする
- zero ObservationのRecordでcursorを進めない
- Adapter DiagnosticがあるRecordでcursorを進めない

正しい挙動:

- 0 observations / 0 diagnosticsでもcursorをcommit
- diagnosticsだけでもcursorをcommit
- duplicate observationsだけでもcursorをcommit
- DB errorならtransaction rollbackし、cursorを進めない

## 8.3 Store API

推奨contract:

```go
type RecordCommit struct {
    Record vrclog.Record
    Result vrclog.Result
}

type CommitResult struct {
    InsertedObservations []StoredObservation
    Cursor               vrclog.Cursor
}

func (s *Store) CommitRecord(
    ctx context.Context,
    commit RecordCommit,
) (CommitResult, error)
```

`InsertedObservations` は今回のtransactionで新規insertされたものだけを、EngineのObservation順で返す。

## 8.4 Duplicate

Observation identityは `vrclog.Observation.ID` を唯一のdedupe keyとする。

- raw line hashだけでdedupeしない。
- URL canonicalizationでdedupeしない。
- time windowでdedupeしない。
- EventKind + URLでdedupeしない。

既存IDがある場合:

1. stored canonical fieldsとincoming fieldsを比較する。
2. 完全一致なら既読duplicateとして無視する。
3. 同じIDでpayload/provenance/type/timeが異なる場合、`ErrObservationConflict` としてtransactionをrollbackする。

同一IDの意味が変わるAdapter変更では、RuleIDを変更するか、開発DBを明示的にresetする。silent overwriteしない。

## 8.5 Projector適用

- DB commit成功後だけ適用する。
- duplicate Observationは再適用しない。
- startup rebuild中はDBの全Observationを一度だけ適用する。
- commit後、Projector適用前にprocess crashした場合、次回startup rebuildでstateは復元される。
- notification deliveryのexactly-onceはこの刷新のscope外。durable outboxは作らない。

## 8.6 Error supervision

### Adapter Diagnostic

非fatal。DBへ保存し、ingest継続。

### Record Issue

非fatal。DBへDiagnosticとして保存し、cursorを進める。

### DB error

- 現Recordを保持したままbounded backoffでtransaction retryしてよい。
- 次Recordへ進まない。
- healthをdegradedへする。
- context cancellationで停止。

### Source fatal error

- healthをdegradedへする。
- persisted latest cursorからbounded exponential backoffでsourceを再構築する。
- backoffは小さな固定範囲、例1秒〜30秒。
- error loopで大量logを出さない。

### Cursor source missing

- warningを一度記録
- cursorなしへfallback
- fatal shutdownしない

---

# 9. Observation persistence model

## 9.1 StoredObservation

```go
type StoredObservation struct {
    Sequence     int64
    ID           vrclog.ObservationID
    OccurredAt   time.Time
    Type         vrclog.EventKind
    Payload      json.RawMessage
    AdapterID    vrclog.AdapterID
    RuleID       vrclog.RuleID
    RecordID     vrclog.RecordID
    SourceID     vrclog.SourceID
    SourceOffset int64
    SourceLine   uint64
    IngestedAt   time.Time
}
```

Companion内にPlayerName/WorldName等の重複flat fieldを持たない。

Eventのsource of truth:

```text
Type + Payload JSON
```

typed Eventが必要なときは `vrclog.DecodeEvent` を使う。

## 9.2 Timestamp

DB保存時刻はUTC RFC3339Nanoへ統一する。

- `OccurredAt`: Record/Eventの発生時刻
- `IngestedAt`: transaction時刻

UI/APIは必要に応じてclient localeへ表示する。

## 9.3 Raw line

raw lineは既定でDBへ保存しない。

理由:

- user ID
- instance情報
- private URL
- local path
- signed token

が含まれ得るため。

初期刷新でraw retention settingを追加しない。診断に必要ならfixtureまたはlocal logそのものを参照する。

---

# 10. SQLite schema

## 10.1 Schema version

新schemaは `PRAGMA user_version = 3` とする（version 3はテーブル構造変更ではなく、vrclog-goのMediaTarget.Backend必須化に伴うObservation payload契約の厳格化を示す。version 2のDBは旧契約payloadを含みうるため拒否する）。

起動時:

1. user_version 0かつapplication tableなし → schema 3を新規作成
2. user_version 3 → schema validation後に利用
3. user_version 1、2、またはその他 → fatal `ErrUnsupportedSchema`
4. user_version 0だが旧tableが存在 → fatal

error messageには次を含める。

- detected version
- expected version
- DB path
- automatic migrationを行わないこと
- userが停止後にDBをrename/deleteすべきこと

旧DBを自動削除しない。

## 10.2 `observations`

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
```

indexes:

```sql
CREATE INDEX observations_occurred_idx
    ON observations(occurred_at, sequence);

CREATE INDEX observations_type_idx
    ON observations(type, sequence);

CREATE INDEX observations_adapter_idx
    ON observations(adapter_id, sequence);

CREATE INDEX observations_record_idx
    ON observations(record_id, sequence);
```

`sequence` はDB内の安定したinsertion orderとpagination/SSE replayに使う。

## 10.3 `ingest_cursors`

```sql
CREATE TABLE ingest_cursors (
    source_id     TEXT PRIMARY KEY,
    path          TEXT NOT NULL,
    byte_offset   INTEGER NOT NULL,
    line_number   INTEGER NOT NULL,
    updated_at    TEXT NOT NULL
);
```

- cursor Offsetは次に読む位置。
- latest cursor query用に`updated_at` indexを作る。
- Pathはlocal persistenceに必要だがAPIへ返さない。

## 10.4 `diagnostics`

```sql
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

Diagnostic IDは決定的に生成する。

```text
SHA-256(
  record_id + NUL +
  adapter_id + NUL +
  rule_id + NUL +
  code + NUL +
  message
)
```

同じRecord再読込で同じDiagnosticを重複保存しない。

## 10.5 その他table

既存のconfig/secrets/stats tableが必要なら、新schemaの一部としてcleanに定義してよい。

ただし旧`events`、`meta_json`、`parse_failures`を残さない。

## 10.6 Transaction

`CommitRecord` transaction順:

1. begin immediateまたは通常transaction
2. 各Observationをinsert/duplicate verify
3. 各Diagnosticをinsert or ignore
4. cursor upsert
5. commit

いずれかが失敗したらrollback。

WAL、busy timeout、foreign keys等、現行の安全なSQLite設定は維持する。

---

# 11. Query API（Store内部）

最低限:

```go
func (s *Store) LatestCursor(ctx context.Context) (*vrclog.Cursor, error)

func (s *Store) ListObservations(
    ctx context.Context,
    query ObservationQuery,
) ([]StoredObservation, *int64, error)

func (s *Store) ObservationsAfterSequence(
    ctx context.Context,
    sequence int64,
    limit int,
) ([]StoredObservation, error)

func (s *Store) ObservationByID(
    ctx context.Context,
    id vrclog.ObservationID,
) (*StoredObservation, error)

func (s *Store) AllObservations(
    ctx context.Context,
) iter.Seq2[StoredObservation, error]
```

`AllObservations` はstartup projector rebuildでsequence昇順に返す。

`ObservationQuery` filters:

- after sequence cursor
- limit
- exact EventKind
- exact AdapterID
- occurred since/until

type allowlistを使わない。SQL bind parameterを使う。

---

# 12. Projector architecture

## 12.1 原則

Observationは永続化された観測事実。Projectorはそれを利用者向けstateへ決定的に投影する。

```text
Observation Store = source of truth
Projectors        = rebuildable derived state
```

Projector stateを初期MVPで別tableへ永続化しない。

## 12.2 Manager

```go
type Manager struct {
    // world, presence, media
}

func (m *Manager) Rebuild(
    ctx context.Context,
    observations iter.Seq2[StoredObservation, error],
) error

func (m *Manager) Apply(
    observation StoredObservation,
) ([]Change, error)

func (m *Manager) Snapshot() Snapshot
```

要件:

- `vrclog.DecodeEvent`でtyped Eventへ戻す。
- sequence昇順に適用する。
- stateをmutex等で安全にsnapshotできる。
- Applyはdeterministic。
- projector errorでprocessをpanicさせないが、canonical payload corruptionはhealthへ明示する。
- startup rebuild中はSSE/Discord通知を発生させない。
- liveの新規ObservationだけChangeを外部へ配信する。

## 12.3 Change

通知に必要なdomain changeだけを表すinternal typeを作る。

例:

```text
WorldChanged
WorldNameUpdated
PlayerJoined
PlayerLeft
MediaAttemptUpdated
```

canonical EventをそのままDiscordへ送らない。通知はprojected changeに基づく。

---

# 13. WorldProjector

## 13.1 入力

- `world.entering_observed`
- `world.joining_observed`

## 13.2 State

```go
type CurrentWorld struct {
    ID         string
    Name       string
    InstanceID string
    JoinedAt   time.Time
}
```

内部で短時間のpending observationを保持してよい。

```go
type pendingWorldName struct {
    Name string
    At   time.Time
}
```

## 13.3 Definitive transition

`world.joining_observed` のworld ID + instance IDを新しいinstance境界として扱う。

- currentと同じID/instanceなら、duplicate logical transitionとして扱い、Player clearやWorldChangedを再発火しない。
- 新しいID/instanceならcurrentを切り替える。
- nearest pending entering nameを15秒以内でmergeする。
- nameがなければemptyのまま許可する。
- `WorldChanged`を一度だけemitする。
- PresenceProjectorをresetする。
- MediaProjectorのcorrelation windowを新sessionへ切る。ただしrecent historyは保持する。

## 13.4 Entering observation

`world.entering_observed`:

- nameをpendingへ保持する。
- 15秒以内にcurrent joiningが既にあり、current nameが空または更新可能ならcurrent nameへmergeする。
- name mergeでは新しいWorldChangedをemitしない。
- 必要なら`WorldNameUpdated` internal changeだけをemitする。
- Discord World通知を二重送信しない。

## 13.5 順序

次の両方をtestする。

```text
entering → joining
joining  → entering
```

どちらも最終CurrentWorldが同じになること。

## 13.6 Pending cleanup

15秒より古いpending nameを新instanceへ誤mergeしない。

wall clockではなくObservation.OccurredAtで判定し、startup rebuildでも同じ結果になること。

---

# 14. PresenceProjector

## 14.1 入力

- `player.joined`
- `player.left`
- WorldProjectorからのdefinitive world transition reset

## 14.2 Player key

```text
Player.IDがnon-empty → ID
else                 → normalized DisplayName
```

DisplayName normalizationは重複判定に必要な最小限とする。

- Unicode文字を破壊しない。
- trim程度に留める。
- case-fold等を過剰に行わない。

## 14.3 Join

- 同じkeyが既に存在 → state変更なし、通知なし
- 新規 →追加し `PlayerJoined` change
- join timeと元Observation IDを保持してよい

## 14.4 Left

- 存在 →削除し `PlayerLeft` change
- 不在 → no-op
- unknown leftを通知しない

## 14.5 World transition

新しいworld instanceへのdefinitive transitionで全Playerをclearする。

- clearされた各playerについてDiscord leave通知を送らない。
- state resetとして扱う。
- entering name observationだけではclearしない。

この挙動により、現行の2種類のworld log行で二度clearする問題を防ぐ。

---

# 15. MediaProjector

## 15.1 目的

一連のresource/error Observationを、ユーザーが理解・操作できる「最近のmedia attempt」へまとめる。

主目的:

> VRChat内で自分だけ動画を再生できない場合でも、元のHTTP(S) URLをCompanionから取得してブラウザで開けるようにする。

## 15.2 入力

初期:

- `resource.url_observed`
- `resource.resolved`
- `media.error_observed`

## 15.3 Status

初期statusは2つだけ。

```text
observed
failed
```

ログで再生開始を明確に確認していないため、`playing`、`paused`、`ended`を作らない。

## 15.4 DTO

```go
type MediaAttempt struct {
    ID                string
    FirstObservedAt   time.Time
    LastObservedAt    time.Time
    Status            string
    BestOpenableURL   string
    Resources         []MediaResourceObservation
    Errors            []MediaError
    ObservationIDs    []string
    AdapterIDs        []string
    Target            *MediaTargetDTO
    WorldInstanceID   string
}

type MediaResourceObservation struct {
    URL           string
    Kind          string
    Role          string
    AdapterID     string
    RuleID        string
    ObservationID string
    ObservedAt    time.Time
}

type MediaError struct {
    Stage         string
    Code          string
    Message       string
    AdapterID     string
    ObservationID string
    ObservedAt    time.Time
}
```

実装上のfield追加はよいが、raw lineやlocal pathをDTOへ含めない。

## 15.5 Attempt ID

Attemptを最初に作ったObservation IDをAttempt IDとして使う。

- deterministic rebuild可能
- random UUIDを使わない
- 後からより優先度の高いsource URLが追加されてもAttempt IDは変えない

## 15.6 保持数

in-memory recent attemptsは最大50件。

- 新しい順でAPIへ返す。
- 古いものをdropしてよい。
- DB Observationは削除しない。
- limitは定数またはconfigにしてよいが、初期値50。

## 15.7 URL openability

openable URL判定:

- `net/url` でparse可能
- schemeが`http`または`https`
- hostがnon-empty

original stringは変更しない。

禁止:

- URLへアクセスして検証
- provider canonicalization
- query削除
- token削除後URLをBest URLとする

## 15.8 BestOpenableURL優先順位

同一Attempt内で次の順序。

```text
1. role=source
2. role=resolver_input
3. role=playback_input
4. なし
```

`role=resolved` は初期実装ではBestOpenableURLへ採用しない。

理由:

- signed CDN URL
- temporary token
- browserで開く用途に不向き
- privacy/security上の露出増加

resolved URLはdetailsには保持する。

同じpriorityが複数ある場合、最初に観測されたopenable URLを維持する。後続で同roleのURLが変わった場合は全Resourceを保持しつつ、明確なtarget対応があれば最新へ更新してよい。挙動をtestで固定する。

## 15.9 Correlation window

初期windowは10秒。

wall clockではなくOccurredAtを使用する。

同じworld session内だけを候補とする。definitive world transitionを跨いでmergeしない。

## 15.10 Correlation priority

ObservationをAttemptへattachする優先順位:

### 1. Exact target

non-empty `component + key` が一致するattempt。

- keyがある場合は最強の相関。
- backendだけの一致はexact targetではない。

### 2. Exact URL transition

`resource.resolved` のInput URLまたはOutput URLを既に含むattempt。

### 3. Exact resource URL

同じURLを既に含むrecent attempt。

### 4. Single unambiguous recent candidate

10秒以内に、同じworld sessionで、まだ競合しないcandidateが一件だけ存在する場合。

これはYamaPlayerの次の流れをまとめるために必要である。

```text
community.yamaplayer source YouTube URL
→ 数秒後
vrchat.core resolver relay URL
→ AVPro error
```

条件:

- candidateが一件だけ
- target conflictがない
- 新Observationが明確に別attemptを開始する強い証拠を持たない

### 5. New attempt

candidateが0件または複数で曖昧なら、新しいAttemptを作る。

誤mergeより分離を優先する。

## 15.11 Source URL event

`role=source`:

- 基本的に新Attemptを開始する。
- exact target/URLで既存と明確に同じ場合はattachしてよい。
- YamaPlayer source URLはBestOpenableURLの最優先候補。

## 15.12 Resolver/playback URL event

- exact target/URLまたはsingle recent candidateへattach
- 曖昧なら新Attempt
- relay URLがoriginal source URLを上書きしない

## 15.13 Resolved event

- Input URLを含むAttemptへattach
- 見つからなければOutput URL
- それでもなければsingle recent candidate
- resolved URLをdetailsへ追加
- BestOpenableURLへしない

## 15.14 Error event

attach priority:

1. exact target
2. error Resource URL exact match
3. same component/backendかつ10秒以内のsingle candidate
4. 10秒以内のsingle candidate
5. new failed Attempt

attach後:

- Status = failed
- errorをappend
- LastObservedAt更新
- BestOpenableURLは既存source URLを維持

## 15.15 Multiple players

複数video playerが同時に動く場合:

- target keyが異なるものをmergeしない。
- targetなしcandidateが複数ある場合、時間だけでmergeしない。
- 一つの`CurrentVideo`へ潰さない。
- UIはrecent attemptsを複数表示する。

## 15.16 World transition

- correlation candidateをclearする。
- recent history 50件は残す。
- 新Attemptへcurrent WorldInstanceIDを設定する。

## 15.17 Latest openable media

State snapshotに、次をoptionalで含める。

```go
type LatestOpenableMedia struct {
    AttemptID string
    URL       string
    Status    string
    ObservedAt time.Time
}
```

recent attemptsを新しい順に見て、BestOpenableURLがある最初のもの。

failedでも対象にする。今回の主要用途だからである。

---

# 16. Startup rebuild

起動順の推奨:

1. config/security/single instance初期化
2. Store openとschema validation
3. Adapter/Engine構築
4. Projector Manager作成
5. DB Observationをsequence昇順に全件rebuild
6. HTTP API/Web UI起動
7. latest cursor取得
8. ingest supervisor開始

Rebuild要件:

- notificationsを送らない
- SSEを送らない
- latest state/mediaを再現する
- corrupt payloadをDiagnostic/healthへ出し、可能なら後続Observationを継続
- 初期実装では全件rebuildでよい
- snapshot cacheやprojector tableを先回りで作らない

---

# 17. Notifications

## 17.1 Player/World

既存Discord通知を、新Projector Changeへ接続する。

通知対象:

- definitive WorldChanged
- PlayerJoined
- PlayerLeft

非通知:

- WorldEnteringObservationだけ
- WorldNameUpdatedだけ
- world transition時のPlayer reset
- duplicate join/left
- startup rebuild

## 17.2 Media

初期実装ではmedia通知を実装しない。

禁止:

- URLをDiscord webhookへ送信
- signed URLを外部へ送信
- playback errorごとの自動通知

将来設定をplaceholderで作らない。

---

# 18. API

base pathは `/api/v1` を維持してよいが、旧contractとの互換は不要。

## 18.1 Health

```text
GET /api/v1/health
```

最低限返す。

```json
{
  "status": "ok|degraded",
  "database": "ok|error",
  "ingest": "running|retrying|stopped",
  "last_ingest_error": "",
  "last_record_at": "",
  "loaded_adapters": 3
}
```

secret/path/full URLを含めない。

## 18.2 Observations

```text
GET /api/v1/observations
```

query:

```text
cursor=<sequence>
limit=1..500          default 100
type=<exact kind>
adapter_id=<exact id>
since=<RFC3339>
until=<RFC3339>
```

- type allowlistを持たない。
- unknown exact typeは空結果でよい。
- SQL bind parameterを使う。
- default orderはsequence descendingまたはascendingを一つに固定し、UIとdocを合わせる。History用途ではdescendingを推奨。
- responseにnext cursorを含める。

response item:

```json
{
  "sequence": 42,
  "id": "...",
  "occurred_at": "...",
  "type": "resource.url_observed",
  "payload": {},
  "adapter_id": "community.yamaplayer",
  "rule_id": "youtube_resolve_url",
  "record": {
    "id": "...",
    "source_id": "...",
    "offset": 1234,
    "line": 52
  },
  "ingested_at": "..."
}
```

含めない:

- local Path
- raw line
- old flat player/world fields
- `meta_json`

## 18.3 State

```text
GET /api/v1/state
```

response:

```json
{
  "world": {
    "id": "wrld_...",
    "name": "...",
    "instance_id": "...",
    "joined_at": "..."
  },
  "players": [
    {
      "id": "usr_...",
      "display_name": "...",
      "joined_at": "..."
    }
  ],
  "latest_openable_media": {
    "attempt_id": "...",
    "url": "https://...",
    "status": "failed",
    "observed_at": "..."
  }
}
```

値がないfieldはnullまたはomitを一貫させる。

旧 `/now` aliasを残さない。

## 18.4 Recent media

```text
GET /api/v1/media/recent?limit=1..50
```

- newest first
- default 20
- MediaAttempt DTO
- all resource URLs/detailsを返す
- local authenticated contextを前提とする
- no title/thumbnail external fetch

## 18.5 Adapters

```text
GET /api/v1/adapters
```

response:

```json
{
  "adapters": [
    {"id": "vrchat.core", "origin": "core"},
    {"id": "community.yamaplayer", "origin": "community"},
    {"id": "community.iwasync3", "origin": "community"}
  ]
}
```

runtime support versionやupdate stateを作らない。

## 18.6 Diagnostics

診断UIが必要なら、次を追加してよい。

```text
GET /api/v1/diagnostics
```

- latest only
- no raw line/path
- adapter/code/message/record position
- URLを不用意に含めない

MVP必須ではないが、health degradedの調査に有用。

## 18.7 Config/Stats

現行のconfig/auth/stats endpointは、新modelへ更新して残してよい。

- old Event tableへのSQLを削除
- statsはcanonical type/payloadまたはprojected changesから算出
- event type allowlistを再導入しない
- secretsをresponseへ返さない

---

# 19. SSE

endpoint:

```text
GET /api/v1/stream
```

## 19.1 Event format

全Observationを一種類のSSE eventとして送る。

```text
id: <observation-id>
event: observation
data: <Observation API JSON>
```

typeごとにSSE event nameを増やさない。

必要ならstate changeを別event名で送ってよいが、初期MVPはObservationだけでよい。FrontendはObservationを受けてstate endpointをrefetchしてもよい。

## 19.2 Newly inserted only

- duplicate DB insertでは送らない。
- startup rebuildでは送らない。
- transaction commit前に送らない。

## 19.3 Last-Event-ID

clientが`Last-Event-ID`を送った場合:

1. StoreでObservation IDからsequenceを取得
2. それより後のObservationをDBから送る
3. live streamへ接続

backlogとliveのraceを防ぐ。

推奨手順:

- broadcasterへsubscribe
- current high-water sequenceを取得
- DBからlast sequence超〜high-waterを送信
- channelからhigh-water超だけ送信

重複が発生し得る実装ではclient送信前にsequenceでdedupeする。

Last-Event-IDが見つからない場合は、明確な`reset` eventまたは409相当でclientへfull refetchを促す。silent lossにしない。

## 19.4 Backpressure

- slow clientでingestをblockしない。
- bounded per-client buffer
- overflow時はconnectionを切断し、clientがLast-Event-IDで再接続できるようにする。
- event dropをsilentにしない。

---

# 20. Web UI

## 20.1 画面

既存のNow/History/Stats/Settings構造を活かしてよいが、data modelを刷新する。

最低限:

```text
Now
History / Observations
Media
Stats
Settings
```

## 20.2 Now

表示:

- Current World
- Current Players
- Latest Openable Media card

Latest media card:

```text
再生失敗 / URL検出
https://www.youtube.com/watch?v=...
YamaPlayer / AVPro
[URLをコピー] [ブラウザで開く] [詳細]
```

「再生中」と断定しない。statusがobservedなら「検出」、failedなら「再生失敗」。

## 20.3 Media page

recent attemptsをnewest firstで表示する。

各Attempt:

- status
- first/last observed time
- BestOpenableURL
- component/target/backend（分かる範囲）
- adapters
- source/resolver/playback/resolved URL一覧
- error一覧
- observation IDs

複数playerを別Attemptとして表示できること。

## 20.4 URL copy

- Clipboard APIを明示クリック時だけ使う。
- success/failure feedbackを表示。
- page loadやObservation受信時にclipboardへ自動copyしない。

## 20.5 Browser open

client/server双方でURLをvalidateする。

許可:

```text
http
https
```

拒否:

```text
file
javascript
data
steam
vrchat
custom schemes
```

UIは原則次を使う。

```html
<a target="_blank" rel="noopener noreferrer">
```

- 自動openしない。
- serverからShellExecuteしない。
- open前にHTTP requestしない。

## 20.6 Metadata

初期実装で次をしない。

- YouTube oEmbed
- YouTube API
- thumbnail fetch
- title fetch
- yt-dlp
- favicon/provider iconのremote fetch

ログからmetadata Eventが得られる将来変更までは、URLとsource情報だけを表示する。

## 20.7 History

旧flat Event unionを削除し、generic Observation rowへ変更する。

- type
- adapter
- occurred time
- summary
- expandable typed payload
- record line/offset

`dangerouslySetInnerHTML`でpayloadを表示しない。

## 20.8 Stats

旧Event table前提を削除する。

例:

- observations by type
- observations by adapter
- player joins
- world transitions
- media attempts / failures

privacy-sensitive URLをchart labelに使わない。

## 20.9 Frontend types

TypeScriptで次を定義する。

```text
Observation
ObservationPayload union for canonical EventKinds
StateSnapshot
MediaAttempt
LoadedAdapter
Health
```

unknown type/payloadはgeneric JSONとして安全に表示できるfallbackを持ってよい。ただしHTMLとしてrenderしない。

---

# 21. LAN modeとprivacy

Media URLは機密情報として扱う。

## 21.1 Local mode

loopbackでは通常どおりfull URLを表示する。

## 21.2 LAN mode

既存のLAN auth/securityを維持・検証する。

- unauthenticated accessでURLを返さない。
- Basic AuthのみでTLSがない場合、README/UIで盗聴耐性がないことを明示する。
- health endpointにfull URLを含めない。
- access logにquery/full URL payloadを出さない。

## 21.3 Logs

Companion自身のapplication logへ次を出さない。

- full media URL
- signed query
- raw line
- secret

必要ならObservation ID、Adapter ID、Rule ID、hostを除いたredacted情報だけを使う。

---

# 22. App lifecycleとhealth

## 22.1 Startup

- schema mismatchは明確にfatal
- projector rebuild failureはfatalまたはdegradedを明確に判断し、corrupt stateで黙って起動しない
- Adapter Engine構築失敗はfatal
- HTTP bind/security errorはfatal
- ingest source errorはapp全体を落とさずdegraded/retry可能

## 22.2 Shutdown

context cancellationで次を順序立てて停止する。

1. ingest source
2. pending DB transaction
3. SSE clients
4. HTTP server
5. DB

channel/goroutine leakを起こさない。

## 22.3 Health state

内部にthread-safeなstatusを持つ。

```go
type IngestStatus struct {
    State          string
    LastRecordAt   time.Time
    LastError      string
    RetryCount     uint64
    CurrentSource  vrclog.SourceID
}
```

PathやURLをhealthへ含めない。

---

# 23. Store tests

最低限:

## 23.1 Schema

- empty DB creates version 3
- version 3 opens
- old version 1 rejects
- old version 2 rejects
- version 0 with old tables rejects
- no auto migration
- WAL/constraints enabled

## 23.2 CommitRecord atomicity

- observation + cursor commit
- zero observation + cursor commit
- diagnostic + cursor commit
- duplicate identical observation ignored + cursor commit
- duplicate conflicting observation rolls back
- observation insert failure rolls back cursor
- diagnostic insert failure rolls back cursor
- cursor upsert failure rolls back observations
- inserted list contains only new rows and preserves order

## 23.3 Cursor

- latest updated cursor
- rotation produces multiple source rows
- Path not exposed in Observation API

## 23.4 Query

- sequence pagination
- type filter arbitrary exact
- adapter filter
- since/until
- deterministic order
- Observation ID lookup
- SSE backlog query

---

# 24. Ingest tests

- Engine receives every Record
- 0-event Record advances cursor
- RecordIssue persists Diagnostic and advances cursor
- Adapter error persists Diagnostic and continues
- DB failure does not consume next Record
- duplicate replay does not reapply projector
- cursor missing fallback runs once
- source retry resumes persisted cursor
- cancellation cleanly stops
- rotation E2E with temp directory

Use fake RecordSource and real SQLite temp DB for integration。

---

# 25. Projector tests

## 25.1 World

- entering → joining merges name
- joining → entering merges name
- pending older than15s not merged
- same instance joining duplicate no second transition
- new instance emits one WorldChanged
- entering alone does not clear players
- joining clears players once
- late name update no duplicate notification

## 25.2 Presence

- join adds
- duplicate join no-op
- left removes
- unknown left no-op
- key by ID, fallback name
- world reset no leave notifications

## 25.3 Media: YamaPlayer

input sequence:

1. `community.yamaplayer` source YouTube URL
2. `vrchat.core` resolver relay URL
3. `vrchat.core` AVPro opening/error
4. `community.yamaplayer` video error

expected:

- one Attempt when unambiguous
- BestOpenableURL = original YouTube URL
- relay/resolved URLs remain details
- status failed
- adapters include core + YamaPlayer
- deterministic rebuild

## 25.4 Media: iwaSync3

input:

1. `vrchat.core` source/resolver URL
2. `community.iwasync3` PlayerError

expected:

- one Attempt when unambiguous
- source URL remains best
- status failed

## 25.5 Multiple player

- different target keys produce separate Attempts
- ambiguous targetless candidates are not force-merged
- no single CurrentVideo assumption

## 25.6 Resolved URL

- signed resolved URL remains details
- does not replace source/resolver BestOpenableURL
- resolved-only Attempt has empty BestOpenableURL in initial policy

## 25.7 World boundary

- observations across world transition are not merged
- recent history remains

---

# 26. API/SSE tests

## API

- health no secrets
- observation JSON shape
- arbitrary exact type filter
- pagination
- state snapshot
- recent media limit
- adapters list order
- old `/now` and old response are absent
- scheme validation
- auth in LAN mode

## SSE

- generic `event: observation`
- commit前に送らない
- duplicate insert送信なし
- Last-Event-ID backlog
- backlog/live raceなし
- slow client overflow disconnect/recovery
- startup rebuild送信なし

---

# 27. Frontend tests

既存toolingに合わせて最低限:

- TypeScript typecheck
- lint
- production build
- Media component unit test（利用中test frameworkがある場合）
- http/https open button enabled
- invalid scheme disabled
- copy feedback
- no auto open
- failed/observed label
- multiple Attempt rendering
- resolved URLがBestとして表示されないpolicy
- generic unknown Observation fallback

不要なtest framework新規導入は避ける。既存にない場合、重要ロジックはpure TypeScript functionとしてtest可能にするか、Go/API E2Eで補う。

---

# 28. 最重要E2E test

fixture fileを実際のpipelineへ通す。

```text
YamaPlayer fixture input.log
    ↓ vrclog.ReadFile
Records
    ↓ Engine(vrchat.core + community adapters)
Observations
    ↓ Store.CommitRecord
SQLite
    ↓ Projector rebuild/apply
MediaAttempt
    ↓ GET /api/v1/media/recent
exact original YouTube URL
```

assert:

- source URL文字列がexact一致
- status failed
- resolver/relay URLがdetailsに存在
- BestOpenableURLはsource URL
- APIにlocal path/raw lineなし
- UIのCopy/Open対象も同じURL

別E2EとしてiwaSync3 fixtureを通す。

VizVidは実fixtureがありgeneric coreで取得できることを確認できた場合だけ追加する。

---

# 29. Documentation刷新

## README

記載:

- local passive log reader architecture
- 3repo責務
- Observation/Projector model
- supported verified adapters
- media URL recovery use case
- privacy/LAN warning
- no VRChat process/API interaction
- DB schema incompatibilityとreset方法
- run/build/test方法

削除:

- 旧Event model
- old replay behavior
- old endpoint examples
- old schema
- Parser/YAML/WASM future
- 未検証project support

## SPEC.md

本実装後の製品仕様へ全面更新する。

最低限:

- Record transaction invariant
- schema
- projector semantics
- API/SSE
- security/privacy
- notification policy

旧SPECをappend修正せず、古い章を削除する。

## CLAUDE.md

永続ルール:

- canonical Event is owned by core
- per-Record transaction
- cursor committed with observations
- no raw-line dedupe
- projectors rebuild from DB
- no legacy migration
- media URLs are sensitive
- no auto open/external metadata
- generic SSE
- adapter composition is compile-time

## CHANGELOG

breaking renewalとして記載する。

- module path
- Observation storage
- cursor ingest
- projector model
- media UI
- API/SSE replacement
- old schema unsupported

---

# 30. 実装フェーズ

## Phase 1: Module/path and composition baseline

実装:

- module path変更
- imports変更
- `vrclog-go` / `vrclog-adapters`依存
- Adapter composition
- loaded adapters model
-旧Event変換への新規依存を増やさない

完了条件:

- appが新Engineを構築できる
- local replaceなし

## Phase 2: Clean SQLite schema/store

実装:

- schema version 3
- observations/cursors/diagnostics
- StoredObservation
- event codec integration
- CommitRecord transaction
- query methods
- old schema rejection

完了条件:

- Store test全成功
- old events/meta_json/raw dedupeなし

## Phase 3: RecordSource and ingest supervisor

実装:

- VRChatSource
- latest cursor
- Follow
- cursor missing fallback
- transaction retry/source retry
- health status

完了条件:

- zero-event Recordもcursor commit
- DB failureでcursor advanceなし
- rotation integration成功

## Phase 4: Projector manager

実装:

- world
- presence
- startup rebuild
- Changes
- notification rewiring

完了条件:

- world two-phase test
- duplicate notificationなし
- old derive package削除可能

## Phase 5: MediaProjector

実装:

- attempts
- correlation
- URL priority
- multiple targets
- world boundary
- latest openable

完了条件:

- Yama/iwa fixture test成功
- original URL選択
- resolved URL非選択

## Phase 6: API/SSE

実装:

- observations
- state
- media/recent
- adapters
- health update
- generic SSE
- Last-Event-ID
- old endpoint削除

完了条件:

- API/SSE test成功
- old type allowlistなし

## Phase 7: Web UI

実装:

- frontend model刷新
- Media page
- Now media card
- History generic Observation
- Copy/Open
- Stats update

完了条件:

- build/lint成功
- no auto open/external fetch
- multiple Attempt表示

## Phase 8: Remove legacy architecture

削除:

- internal old Event
- old ingest conversion/source
- old replay
- old derive
- old events SQL
- old API DTO/allowlist
- stale tests/docs
- wrong module path references

完了条件:

- grepでold event architectureが残っていない
- compatibility shimなし

## Phase 9: E2E and release readiness

実装:

- Yama E2E
- iwa E2E
- schema reset doc
- security/privacy review
- full build/test

完了条件:

- 最重要E2E条件を満たす
- Windows app build成功

---

# 31. Quality gate

Go:

```bash
gofmt -w .
go test ./...
go test -race ./...
go vet ./...
GOOS=windows GOARCH=amd64 go build ./...
```

Frontendはrepoのpackage managerに合わせる。例:

```bash
npm ci
npm run lint
npm run build
```

lockfileと実際のscriptを確認し、存在しないcommandを盲目的に追加しない。

E2E:

- temporary SQLite
- fixture log
- core + community Engine
- API response

全て成功させる。

---

# 32. 完了条件

- [ ] module pathが `github.com/vrclog/vrclog-companion`
- [ ] core + community Adapterをcompile-timeで明示構成する
- [ ] Companion独自Parser/Adapter/Eventを作っていない
- [ ] old flat Event modelが削除されている
- [ ] RecordSourceがiterator型である
- [ ] per-Record transactionが実装されている
- [ ] zero-event Recordでもcursorが進む
- [ ] DB failureでcursorが進まない
- [ ] Observation IDだけをstorage identityに使う
- [ ] raw-line SHA dedupeがない
- [ ] duplicate conflictをsilent overwriteしない
- [ ] schema version 3がcleanに作られる
- [ ] old schemaを自動移行しない
- [ ] observations/cursors/diagnostics tableがある
- [ ] raw line/pathがObservation APIへ出ない
- [ ] startup rebuildがsequence順で決定的
- [ ] Worldのentering/joiningを正しくmergeする
- [ ] definitive world transitionだけPlayerをclearする
- [ ] duplicate World/Player通知がない
- [ ] MediaProjectorが複数Attemptを扱う
- [ ] YamaPlayer original URLがBestOpenableURLになる
- [ ] iwaSync3 errorをgeneric URLと相関できる
- [ ] resolved/signed URLがBestを上書きしない
- [ ] statusを根拠なくplayingとしない
- [ ] `/api/v1/observations`がgenericである
- [ ] `/api/v1/state`がprojected stateを返す
- [ ] `/api/v1/media/recent`がある
- [ ] `/api/v1/adapters`がある
- [ ] SSEがgeneric observation eventである
- [ ] Last-Event-ID recoveryがある
- [ ] UIにMedia pageとCopy/Openがある
- [ ] http/https以外をopenできない
- [ ] URLを自動openしない
- [ ] URL metadataを外部取得しない
- [ ] media URLをDiscord通知しない
- [ ] LAN/privacy warningが更新されている
- [ ] old API/schema/docsが残っていない
- [ ] Go test/race/vet/build成功
- [ ] frontend lint/build成功
- [ ] Yama/iwa E2E成功

---

# 33. 実装時にしてはならない妥協

- 旧Eventを新Observationへ変換するcompatibility layerを恒久化する
- old DBを自動migrateする
- old `/events`/`/now`をaliasとして残す
- raw line hashを新Observation dedupeへ流用する
- observation保存とcursor更新を別transactionにする
- 0-event Recordを無視してcursorを止める
- Projector前にSSEを送る
- startup rebuildでDiscord通知する
- entering room nameだけでworld transitionと判断する
- 同じURLだからという理由だけでDB rowを潰す
- 全mediaを単一CurrentVideoへ潰す
- time proximityだけで競合する複数Attemptを強制mergeする
- signed resolved URLをbrowser openの第一候補にする
- YouTube title/thumbnailを自動fetchする
- URL検出時にserver側でbrowserを開く
- media URLをapplication log/Discordへ出す
- runtime Adapter pluginを追加する
- Adapter support catalog/update UIを先回りで作る
- old Future roadmapをREADMEへ残す

この刷新では、Observationの永続化とProjectorの意味付けを明確に分離し、再生できない動画の元URL取得という実用要件を、過剰なplugin基盤なしで確実に満たすことを優先する。
