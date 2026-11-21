# NOTICE — 第三者由来のものについて

このリポジトリのコード（`run-*.ps1`, `setup.ps1`, `Dockerfile*`, `go-proxy/`, `pi-config/`, `eval-dream.mjs`）は MIT License（[LICENSE](./LICENSE)）で配布します。`go-proxy` は Go 標準ライブラリだけで書かれており（[go.mod](./go-proxy/go.mod) 参照）、第三者の Go 依存はありません。

ただしビルド時にイメージ／コンテナが取り込む実行環境と、外部から起動するソフトウェアは別ライセンスです。条項はこのファイルでは複製せず、各上流に従ってください。

## ビルド時に Dockerfile が取り込むもの
| もの | 主なライセンス | 出所 |
|---|---|---|
| Debian bookworm-slim の各種パッケージ | 各パッケージの © （DFSG 準拠中心） | apt (上流メタデータ) |
| Node.js | MIT / Apache-2.0 混在 | nodejs.org |
| Playwright + Chromium | Apache-2.0 / BSD 系（Chromium は BSD-ish、Blink/V8 等は別途表示） | microsoft/playwright |
| Rust toolchain | MIT OR Apache-2.0 | rust-lang |
| Go toolchain | BSD-3-Clause | golang.org |
| uv / uvx | MIT OR Apache-2.0 | astral-sh/uv |
| ripgrep | MIT or Unlicense | BurntSushi/ripgrep |

## ホスト側で起動して繋ぐもの（このリポジトリの外）
| もの | ライセンスの扱い |
|---|---|
| Sync-Strata（エンジン／dsh／launcher／chat） | プロプライエタリ＋同梱 OSS（llama.cpp, CUDA, フォント, モデル Qwen Community License 等）。`D:\apps\sync-strata\app\licenses\README.txt` と `app\licenses\*` を参照。ここを再配布するライセンスではない |
| wslc / WSL コンテナ機能 | Microsoft の Windows / WSL の条項に従う |
| pi coding agent（`@earendil-works/pi-*`）と skills/extensions に使う型定義 | 上流の条項に従う（pi 本体 OSS の派生設定を載せている） |
| GitHub CLI (`gh`) | MIT（任意構成要素、ホスト実行のみ） |
| ESP-IDF / QEMU / Espressif ツールチェーン | Apache-2.0 / GPL 混在（任意構成要素、ホスト実行のみ） |
| AWS Official Emulator（aws-emulator:latest, pi-lambda-rie:latest） | 上流イメージの条項に従う（任意ビルド） |

## 注意
- Sync-Strata のモデルウェイトやエンジンバイナリをこのリポジトリ経由で再配布する許可にはなりません（各自がインストール済みであることが前提）。
- ワークスペース（`-Workspace` でマウントするリポジトリ）の成果物のライセンスはそのリポジトリ側のものを優先してください。Cardputer-Zero 系の実機プロジェクトには別のライセンスが入る余地が残っています。
