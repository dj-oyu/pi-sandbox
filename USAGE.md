# pi-sandbox USAGE（オプションの細部リファレンス）

[README.md](./README.md) は「まず動かす」だけ。ここでは全フラグ・全環境変数・管理API・ハーネス拡張の挙動を、コードに書いてある通りに列挙する。

---

## 1. ポートと見取り図

| PORT | 何 | バインド |
|---|---|---|
| `8080` | Sync-Strata エンジン（OpenAI互換 `/v1`）。**直列1リクエスト** | `sync` の設定（既定 127.0.0.1） |
| `18080` | pi-proxy API/Control：`/v1/*` 転送＋監査、`/admin/ui`、`/gh-ci/*`、`/esp-idf/run` | `PI_PROXY_BIND`（既定 0.0.0.0 → WSL からも届く） |
| `18888` | Egress プロキシ（`allowlist.json` マッチのみ通す／CONNECT 対応） | 同上 |
| `19297` | aws-emulator HTTP→HTTPS ブリッジ（`-WithAwsEmulator` の時だけ） | 同上 |
| `3000/5173/8000` | コンテナ内の dev server をホスト loopback に公開 | run スクリプトが固定で付与 |

`pi-proxy.exe` は 1 ファイルで、`export-logs` サブコマンドを持つ（§14）。

---

## 2. setup.ps1

```powershell
.\setup.ps1 [-WithWeb] [-WithAws] [-All] [-NoCache]
```

| フラグ | やること |
|---|---|
| （無印） | `pi-sandbox:latest` をビルド → `wslc` の中で `go test ./...` + `GOOS=windows go build -o /out/pi-proxy.exe` |
| `-WithWeb` | `Dockerfile.web` から `pi-sandbox-web:latest`（Playwright Chromium 入り、apt/curl/wget を除去した軽量版） |
| `-WithAws` | `Dockerfile.aws-emulator` → `aws-emulator:latest`、`Dockerfile.lambda` → `pi-lambda-rie:latest` |
| `-All` | WithWeb + WithAws |
| `-NoCache` | 全ビルドを `--no-cache` で |

`refs/` と `logs/sessions/` は自動作成。ビルド済みの `pi-proxy.exe` が有ると run 側は再ビルドしないので、Go を編集したら **exe を削除してから** `setup.ps1`（または run の初回起動）で作り直す。

---

## 3. run-pi.ps1（本体）

```powershell
.\run-pi.ps1 [-Workspace <path>] [-RefsDir <path>] [-GitHubToken <t>] [-Image <name>]
             [-Model auto|<model-id>] [-ShmSize <size>] [-Prompt <text> | -PromptFile <path>]
             [-NonInteractive] [-WithAwsEmulator] [-UpdateAwsEmulator] [-EnableDream]
             [-- <pi への追加引数...>]
```

| パラメータ | 既定 | 挙動 |
|---|---|---|
| `-Workspace` | 現在のディレクトリ | `/workspace` に rw マウント。無ければ作成 |
| `-RefsDir` | `<sbox>\refs` | `/refs` に ro マウント |
| `-GitHubToken` | `$env:GITHUB_TOKEN` | コンテナに `GITHUB_TOKEN` / `GH_TOKEN` として渡す（`/gh-ci` はホスト側 `gh` が使うので、push は基本ホストで） |
| `-Image` | `pi-sandbox:latest` | ラッパーが上書き可（web / aws） |
| `-Model` | `auto` | :8080 の `/v1/models` 先頭 → 取れなければ `qwen3.8-flash-next-sync-s` をフォールバック（[run-pi.ps1](./run-pi.ps1#L76-L87)）。`.runtime-models.json` に `_strataCompat_` の preset も同居し、`--provider sync-strata --model auto` で list 選択になる |
| `-ShmSize` | web ラッパーのみ `512m` | Chromium 用共有メモリ |
| `-Prompt` / `-PromptFile` | – | プロンプト与時は `-p`（非対話）。**与えない時だけ TUI 対話モード** |
| `-NonInteractive` | off | バッチ自動継続の判定を有効化（下記） |
| `-WithAwsEmulator` | off | emu コンテナ起動・CA取得・ブリッジ :19297・コンテナ内 AWS_* 変数注入 |
| `-UpdateAwsEmulator` | off | emu イメージを上流 LTS で再ビルドして CA も作り直す（ca-pem は自動キャッシュ） |
| `-EnableDream` | off | `PI_DREAM=1`。加えてホスト側で dream 統合ラウンド／評価を追加実行 |
| 残り位置引数 | – | `pi` CLI へそのまま素通し（`-Provider`/`-Model` 系の指定は run 側が先に渡すので重複しない） |

**バッチの自動継続（targetMet ゴールポスト）**：`-NonInteractive` かつ `-PromptFile`/-Prompt 時に、`.pi/DONE.json` を見て `targetMet=false` なら `[HARNESS AUTO-CONTINUE n/2]` を注入して最大 **2 ラウンド**まで `--continue` で走る（[run-pi.ps1](./run-pi.ps1#L230-L246)）。Dream が有効なタスクでは dream.md が 12 KB を超えると先に「記憶統合ラウンド」を挟む。最後に `eval-dream.mjs` が評価ログを出す（§13）。起動終了時は `finally` でプロキシと aws コンテナを必ず止める。

コンテナの不変条件: 非 root ユーザー `agent`, `--cap-drop=ALL`, `HOME=/home/agent`, マウントは `/workspace` rw / `/refs` ro / `~/.pi/agent` ro（`sessions` だけ rw）/ `models.json`・`allowlist.json`・`APPEND_SYSTEM.md` ro / `/usr/local/bin/idf.py` はホスト ESP-IDF＋QEMU を叩くシム（ro）。環境は `sandbox-env` skill に従う（curl/apt/jq/global python なし、python は `uv run`、HTTP チェックは `node -e fetch(...)`）。NO_PROXY は loopback と Link-Local・metadata IP を素通し指定している。

---

## 4. ラッパー

| スクリプト | 内蔵フラグ | 補足 |
|---|---|---|
| `run-pi-web.ps1` | 同様の Workspace/RefsDir/GitHubToken/Prompt/PromptFile/NonInteractive + 残り pi 引数 | `pi-sandbox-web:latest` が無いとビルド案内を出て exit 1。`-ShmSize 512m` を強制 |
| `run-pi-aws.ps1` | Workspace/RefsDir/**`-Update`**/Prompt/PromptFile/NonInteractive | `run-pi.ps1 -WithAwsEmulator` に転送（`-Update` → `-UpdateAwsEmulator`） |

---

## 5. ホスト側 pi-proxy の環境変数

`pi-proxy.exe` は exe 置き場基準で `logs/` と `allowlist.json` を探す。全部任意。

| 変数 | 既定 | 意味 |
|---|---|---|
| `PI_PROXY_BIND` | `0.0.0.0` | 両サーバの bind host |
| `PI_API_PROXY_PORT` / `PI_EGRESS_PROXY_PORT` | `18080` / `18888` | API/管理 と egress |
| `STRATA_HOST` / `STRATA_PORT` | `127.0.0.1` / `8080` | 転送先エンジン |
| `PI_STRATA_CONCURRENCY` | `1` | Strata セマフォ（>1 は GPU が詰まりやすいので推奨外）。PATCH でも可 (§8) |
| `PI_PROXY_TOKEN` | run スクリプトが自動生成 | 認証トークン。**再利用したい時だけ先に設定**（run は生成せずそれを使う） |
| `PI_WORKSPACE` | – | `/admin/fs/*`・LSP・gh・esp-idf の基準パス（run が自動設定） |
| `PI_LOG_DIR` / `PI_SESSIONS_DIR` | `<exe>\logs` / `<logs>\sessions` | バイナリログ／pi セッション |
| `PI_MAX_LOG_BYTES` / `PI_MAX_LOG_FILES` | `10485760` / `5` | .bin ローテーション |
| `PI_ALLOWLIST` | `<exe>\allowlist.json` | egress 許可リスト（ホスト名のマウント元と同じ実ファイルを編集できる） |
| `PI_AUTO_HEAL` | `true` | 3 回連続失敗で対象コンテナを wslc で再起動（§8 health） |
| `PI_HEALTH_INTERVAL_SEC` | `10`（有効値 2–300） | ヘルス周期 |
| `PI_MAX_CMD_WORKERS` | `4` | ジョブ実行ワーカー数 |
| `GH_EXE` | `gh` | `/gh-ci` と GitHub 操作の実体パス |
| `ESP_IDF_PROFILE` | 自動探索（`C:\Espressif\tools\*.PowerShell_profile.ps1`） | `/esp-idf/run` が dot-source するプロファイル |
| `PI_MCP_CONFIG` | `<logs>\mcp-servers.json` | MCP サーバ台帳 |
| `PI_SAAS_BASE_URL/MODEL/API_KEY` | – | SaaS マッチング／Arena の相手側 |
| `PI_SAAS_CONFIG` | `<logs>\saas-config.json` | プリセット永続化先（APIキーは DPAPI で暗号化保存、JSON には出ない） |
| `PI_AWS_EMU_PORT` / `PI_AWS_EMU_TARGET_PORT` / `PI_AWS_EMU_CA` / `PI_AWS_EMU_CONTAINER` | run-aws が設定（19297 / 9297 / ca-pem / コンテナ名） | ブリッジ系。単体運用時だけ手で指定 |

---

## 6. コンテナ内で効く変数（run スクリプト注入分）

`PI_PROXY_URL=http://<wslIp>:18080`, `STRATA_PROXY_TOKEN`, `HTTP(S)_PROXY`/`NO_PROXY`, `GITHUB_TOKEN`/`GH_TOKEN`, AWS モード時は `AWS_ACCESS_KEY_ID=test`・`AWS_SECRET_ACCESS_KEY=test`・`AWS_DEFAULT_REGION=us-east-1`・`AWS_ENDPOINT_URL=http://host:19297`（＋サービス別 `AWS_ENDPOINT_URL_*`）と `AWS_CA_BUNDLE`。Dream 系: `-EnableDream` で `PI_DREAM=1` と `PI_DREAM_RUN_ID`、統合ラウンドでは `PI_DREAM=0`／評価時に `0` に切替。

pi 自身のモデル解決は `.runtime-models.json` を `~/.pi/agent/models.json` に ro マウントして行う（provider 名 `sync-strata`、`--provider sync-strata --model auto` でプロバイダ内 list を選べる）。provider の `apiKey` 欄は env 参照の文字列 `STRATA_PROXY_TOKEN` で、run が注入した同名 env がそのまま認証に効く想定。うまく通らない時は `PI_PROXY_TOKEN` を先に固定して（§5）から `curl` 代わりの node fetch で `Authorization: Bearer <同じ値>` を試すこと（audit ログにはヘッダ本文が残らない）。

---

## 7. ハーネス拡張（~/.pi/agent/extensions）と効かせ方

### verify_metrics（verify-loop.ts）
- `.pi/verifier.json` があればそれを、無ければ自動検出する:
  | 検出 | コマンド |
  |---|---|
  | package.json scripts | `typecheck` / `lint` / `test` / `build` → `npm run <x>` |
  | Cargo.toml | `cargo check --all-targets`, `cargo test` |
  | go.mod | `go vet ./...`, `go test ./...` |
  | pyproject.toml | `uv run pytest` |
  | CMakeLists.txt に idf.cmake または sdkconfig | `idf.py build` |
- `targetScore`（既定100）と `checks[].weight` で加重スコア。baseline／前回比の delta も表示し、`logs/sessions/verify-metrics.jsonl` に記録、`.pi/DONE.json` を更新。
- 不正対策: refs 側の `verify/check_integrity*.sh` が verifier.json の SHA-256 と「スタブ/テスト無効化」を毎回検査する運用が実履歴（Cardputer Zero）で使われた。verifier 側を弱体化させないこと。

### subagent（single / parallel / chain）
- agents: `scout`（圧縮調査）→ `planner`（実装計画・Files To Edit）→ `worker`（テスト付き実装＋検証実行）→ `reviewer`（8項目批判）。
- TUI プロンプト: `/implement <task>`、`/implement-and-review <task>`、`/scout-and-plan <task|dir> [phase]`。chain は `{previous}` を次のタスクに差し込む。上限: 並列 8 タスク・同時 4。
- **エンジンの直列性を考えて基本は single か chain**。並列は1リクエストずつ列になるだけで速度は出ない。

### perturbation（停滞時に視点を変える）
- stuckness = min(0.45, 連続失敗×0.22) + min(0.35, (最大ファイル編集回数−1)×0.17) + min(0.25, (進捗なしターン−1)×0.05, 編集開始後だけ) + min(0.20, (verify なし編集−1)×0.08)。確率は base(0.05 or 0.02) + 0.90×score^1.4、上限0.95。
- クールダウン2ターン／1プロンプト最大3回。角度は `first_principles` → `alternative_approaches` → `minimal_reproduction` → `wider_context` を循環。注入ログは `logs/sessions/perturbation.jsonl`。
- TUI コマンド `/perturb status`、`/perturb <理由>` で手動投入。

### boss-reflect（BOSS モードの15分反省）
- 発火条件: `.pi/BOSS_MODE` が存在、かつ `PI_DREAM=1` でないこと、そのターンにツール実行あり、`.pi/DONE.json` が未達。15分ごとに `[BOSS HARNESS — 15-MINUTE REFLECTION CHECKPOINT n/6]` で `docs/DISCOVERIES.md` を 7 ティア（Epic > Nobel Prize > Genius > Insightful > Useful > Note > Dead End）で更新させる。召回は「実際にコード編集後・4ターン以上・クールダウン5・確率0.22・上限4」。DISCOVERIES の見出しを2件ランダムに引く。
- Cardputer Zero の履歴ではこの召回クローズが docs-only コミットを大量生成した（探索の重複抑制には効いた反面、コミットノイズにもなった）。

### dream（長期記憶）
| 設定 | 既定 | env override |
|---|---|---|
| enabled | `PI_DREAM=1` → true、`=0` → false、どちらも無しなら `.pi/dream.json` の `enabled` | – |
| intervalMin | 12 | `PI_DREAM_INTERVAL_MIN` / dream.json |
| recallProb | 0.22 | `PI_DREAM_RECALL_PROB` / dream.json |
| recallCooldownTurns / maxWrites / maxRecalls | 4 / 6 / 4 | `PI_DREAM_COOLDOWN` / `PI_DREAM_MAX_WRITES` / `PI_DREAM_MAX_RECALLS` |
| memoryFile | `.pi/memory/dream.md` | `PI_DREAM_MEMORY_FILE` / dream.json |
- 見出しフォーマットは `### [Principle|Insight|Dead End|Note] D<n> — <title>`。`dream-metrics.jsonl` に書込/召回/フォロースルー/overhead を記録し、ホスト統合（>12KB で `PI_DREAM=0` の単発ラウンド）と §9 の評価が後追いで走る。

---

## 8. 管理・制御 API（:18080）

認証は `Authorization: Bearer <token>` か `X-Api-Key`、または `/admin/*` に限る `?token=`。**UI は常に token クエリ付き**で開くこと。主な一覧（[servers.go](./go-proxy/internal/proxy/servers.go#L98-L190)）:

| endpoint | method / body | 説明 |
|---|---|---|
| `/admin/ui/` | GET | SPA の管理コンソール。実測で確認できたペイン: **Pi Console**（セッション選択・並列枠 1–16＝Strata 同時実行の適用・`/admin/strata/chat` への直接テスト送信）、ログコンソール（stream 切替 api-audit/egress/control/all、live 更新、許可ドメインの PATCH 編集、export ダウンロード）、ファイルツリー付きエディタ（GitHub ▾、Markdown/LSP hover・定義ジャンプ）、SaaS/Arena 設定フォーム（openai/anthropic、bearer/x-api-key/custom、追加ヘッダ JSON、system プロンプト）。`/admin/events` は SSE で `log`, `health`, `job`, `strata_queue`, `advice` を流す |
| `/admin/config` | GET / PATCH(JSON) | `allowedHosts[]`, `strataConcurrency`(1–16), `autoHealEnabled`, `healthIntervalSec`(2–300), SaaS 設定(`saasLabel`/`saasApiFormat: openai|anthropic`/`saasAuthType: bearer|x-api-key|custom|none`/`saasCustomHeader`/`saasExtraHeaders`/`saasBaseUrl`/`saasModel`/`saasApiKey`/`saasSystem`), `strataSystem`。**再起動不要・即時反映**。SaaS キーは DPAPI 保存 |
| `/admin/health` | GET | 監視対象の up/failures。`PI_AUTO_HEAL=true` で 3 回連続失敗なら wslc で自動再起動（`container_auto_heal` を control.bin に記録） |
| `/admin/dream/status` | GET | runId・overhead・記憶ファイル健康度（tier 集計・bloat）、dream-metrics/dream-evals の統合状態 |
| `/admin/strata/queue` / `/admin/strata/chat` | GET / POST | 待ち行列の可視化、Strata へ直接チャット（UI の Strata スレッドの裏側） |
| `/admin/jobs`, `/admin/jobs/<id>` | GET | ジョブ一覧と個別（esp-idf async で得る jobId はここで追う）。イベント名は `esp_idf_queued`/`esp_idf_run`/`gh_ci_call` など |
| `/admin/logs/recent?stream=api-audit|egress|control|all&tail=N` | GET | N既定80、最大500 |
| `/admin/logs/export?stream=<api-audit\|egress\|control>&format=jsonl&tail=N` | GET | .bin → JSONL ダウンロード |
| `/admin/pi/sessions`, `/admin/pi/session?id=...` | GET | 会話リスト／1会話の再生（UI の pi スレッド） |
| `/admin/fs/tree?dir=&hidden=true`, `/admin/fs/raw?path=` | GET | ワークスペースツリーとプレビュー（先頭のみ、`truncated` 付き） |
| `/admin/lsp/status`, `/admin/lsp/hover|definition` | GET / POST `{path,line,character}` | LSP 状態・参照情報 |
| `/admin/mcp/servers`, `/admin/mcp/servers/<name>/toggle|probe`, `DELETE /admin/mcp/servers/<name>`, `POST /mcp/<name>` | – | MCP サーバ台帳（logs/mcp-servers.json）・トグル・プローブ・実行 |
| `/admin/github/status` | GET | gh auth・リモート・ブランチ・差分数 |
| `/admin/github/create-repo` | POST `{name,visibility,description}` | ワークスペースを git init & push してリポジトリ作成 |
| `/admin/github/push` | POST(無 body) | origin へ push |
| `/admin/github/pr` | POST `{title,body,base}` | PR 作成 |
| `/admin/arena/run` | POST `{topic,turns(≤12,既定4),strataModel,saasBaseUrl/saasModel/saasApiKey(省略で config),initialPrompt,allowedFiles,allowErrors,strataSystem,saasSystem}` | Strata × SaaS の往復（UI の「SaaS AI マッチング」。モデルは tools も使える） |
| `/admin/arena/history`, `/admin/arena/context` | GET | run 履歴／直近 initialPrompt とワークスペース可視ファイル一覧 |
| `/admin/arena/advice` | GET / POST | 次の `/v1/chat/completions` に注入する助言を1件だけキュー。UI の入力欄から投稿できる |

`/gh-ci/<gh api path>` は素通し（例: `/gh-ci/repos/<owner>/<repo>/actions/runs/<id>/jobs`、`.../logs`）。ホストの `gh` を 30 s で叩く同期実行。**古い job ログは GitHub 側が消えていて HTTP 410 になる**のが実測済み（Cardputer Zero 履歴で確認）。

---

## 9. eval-dream.mjs（Dream の事後評価）

```powershell
node .\eval-dream.mjs [--run <runId>] [--workspace <path>] [--no-judge] [--strata http://127.0.0.1:8080/v1] [--json]
```

- ルール判定: 召回の後に verify スコアが上がった件があれば **HELPFUL**、overhead>0.3 か記憶ファイル膨張で **NOISY**、それ以外 NEUTRAL。Qwen Judge（ローカル 1 ショット、`--no-judge` で停止）が NEUTRAL の時に限り HELPULFUL/HARMFUL の定性 verdict で上書きされる。出力は `logs/sessions/dream-evals.jsonl`（memoryHealth, recallImpacts, verdict, reasons）。

---

## 10. AWS エミュレータ + Lambda の使い勝手

- `-WithAwsEmulator` で `aws-emulator:latest` をコンテナ起動（メモリ512M・CA付き HTTPS の health をホストが監視）、ブリッジ :19297 が S3/SQS/DynamoDB/SNS を素通し、Lambda系を横で処理。
- コンテナ内の AWS_* は run-aws が自動注入（§6）。TLS 検証は `NODE_EXTRA_CA_CERTS`/`REQUESTS_CA_BUNDLE`/`AWS_CA_BUNDLE` に ca-pem を渡している。
- 関数のコードは `<workspace>/.pi/lambda-routes.json` の `{"<funcName>": {"dir": "...", "handler": "index.handler"}}` で解決、無ければ `<workspace>/functions/<funcName>/` にフォールバックし、`.aws-emulator/lambda-code/` に staging して `pi-lambda-rie:latest` を Invoke ごとに使い捨てコンテナで実行（`wslc` 必須）。
- `-Update` で上流 LTS に追従ビルドし直す（CA も再生成、ca-pem は同名ファイルにキャッシュ済み）。emu のリポジトリは `.aws-emulator/`、コンテナ名 `pi-aws-emu-<PID>`。

---

## 11. allowlist の管理

`allowlist.json` はホストの同名ファイルに直接書ける（マウント元が同じ）。追加してもプロキシは mtime を見て**ランタイムで再読込**するので再起動は不要。あるいは `/admin/config` PATCH の `allowedHosts` でも可（保存先は logs 配下）。遮断ホストは egress ログに出る。

---

## 12. ESP-IDF（M5Stack）運用

- コンテナの `idf.py` はシム。実体は `powershell.exe` で `ESP_IDF_PROFILE` を dot-source してからホストの `/workspace` で `idf.py` を叩くブリッジ（`:18080/esp-idf/run`）を使う。
- POST ボディ: `{action: "build"|"fullclean"|"set-target"|"qemu", target?: <esp32等の whitelist>, timeoutSec?, async?}`。既定タイムアウト build 180 s / fullclean 30 s / set-target 120 s / qemu 15 s、build は 10–300 s、qemu は 3–60 s で上書き可。`async:true` → jobId を返すので `/admin/jobs/<id>` で追う。
- ホストに ESP-IDF と QEMU が必要です（`sync doctor` と違い、これは pi-proxy 側機能）。

---

## 13. ログの形式と見方

| ファイル | 内容 |
|---|---|
| `api-audit.bin` | `/v1/*` と管理 API の監査（`event:"request"` に tokens/reasoning/duration/status、`gh_ci_call`, `esp_idf_*`, `saas_preset_activated`…）。model はモデル名、provider は preset のラベルが入る（UI の provider 列はここから復元） |
| `egress.bin` | 許可/遮断の host アクセス（`allow_http` / `block`） |
| `control.bin` | 起動・ジョブ・自動修復など運用系 |
| `sessions/*.jsonl` | pi セッション本体（`--workspace--` 配下）、`verify-metrics.jsonl` / `perturbation.jsonl` / `dream-metrics.jsonl` / `dream-evals.jsonl` |

```powershell
.\pi-proxy.exe export-logs --file .\logs\control.bin [--tail N]   # stdout に JSONL
# UI から: GET /admin/logs/export?stream=control
```

ローテーションは 10 MiB × 5 世代。UI は「今出ている行」の末尾だけ見せ、コンソールでは「更新なしの繰り返し行」を折り畳む。時刻はセッションの ISO UTC を正とする（git の作者タイムスタンプは +0900/+0000 が混在して絶対値として使えない）。

---

## 14. Sync-Strata との接続で覚えておくこと

- エンジンは既定で**直列1**。`PI_STRATA_CONCURRENCY` を上げるより subagent を chain にした方が実測は安定していた（Final Boss でも直列前提で回していた）。
- `-Model auto` はエンジン起動後に解決する。モデルID固定なら `.runtime-models.json` を触らず `-Model <id>` で渡す方が速い。
- dsh（会話 UI）は `sync up` が起こし、URL は `sync gui-url`。`--api-only` / `--no-gui` で分離できる。`sync chat --preset speed|quality`、`sync doctor [--ssd]`、`sync down --engine|--dsh` も使える。

---

## 15. よくある詰まり方

| 症状 | 対処 |
|---|---|
| web イメージが無い | `.\setup.ps1 -WithWeb`（または `-All`） |
| Go を直したのに反映されない | `pi-proxy.exe` を消して setup か初回 run で再ビルド（go test も走る） |
| 外向きが 403 | allowlist.json か `/admin/config` PATCH で host 追加（egress ログに遮断名が出る） |
| CI ジョブログが読めない | GitHub のログ保持切れ（HTTP 410）。run の再実行かアーティファクトを見る |
| バッチが終わらない | verify が通らず自動継続中（最大2ラウンド）。`.pi/verifier.json` の check を絞る／targetScore を下げる |
| 会話が見えない | `/admin/pi/sessions` → `?id=`、または `logs\sessions\--workspace--\*.jsonl` を直接 |
| WSL IP が変わった | run が毎回 vEthernet(WSL) を検出して `.runtime-models.json` を作り直すので放置で OK |
| コミットが docs-only で増える | boss-reflect の召回クローズ挙動。止めるなら `.pi/BOSS_MODE` を削除 |

## 16. 履歴を自分で掘る

```powershell
# フェーズ進行（スコア台帳を絞る）
Select-String -Path .\logs\sessions\verify-metrics.jsonl -Pattern '"ts":"(.+?)".*?"score":(\d+)' 
Get-Content .\logs\sessions\verify-metrics.jsonl | ConvertFrom-Json | Select -Last 25
# 会話からのフェーズ特定
Select-String -Path .\logs\sessions\--workspace--\*.jsonl -Pattern 'Phase\s?[0-9A-C]+' -List
# 全ログの JSONL ダンプ
.\pi-proxy.exe export-logs --file .\logs\api-audit.bin
```
