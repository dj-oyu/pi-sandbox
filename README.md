# pi-sandbox Meta-Harness

Windows 11 ホストの **Sync-Strata**（ローカル GPU 推論）と、`wslc.exe` で隔離した **pi coding agent** を安全に繋ぐためのメタハーネス。細かいオプションは **[USAGE.md](./USAGE.md)** に全部書いた。ここでは「まず動かす」だけ。

## 1枚図

```text
[ホスト] Sync-Strata :8080 (直列1リクエスト)
   ▲ OpenAI互換 /v1/chat/completions
[ホスト] pi-proxy.exe
   ├─ :18080  API監査 + 管理UI /admin/ui + /gh-ci + /esp-idf/run + /admin/*
   ├─ :18888  Egress allowlist プロキシ（allowlist.json 以外 403）
   └─ :19297  aws-emulator ブリッジ（-WithAwsEmulator の時だけ）
        ▲ Bearer token + vEthernet(WSL) IP（自動検出）
[wslc] non-root agent / --cap-drop=ALL
   /workspace rw ＋ /refs ro ＋ ~/.pi/agent ro(sessions だけ rw)
   ホスト開放: 3000 / 5173 / 8000（127.0.0.1）
```

## 1. 初回のビルド（1コマンド）

```powershell
# WSL コンテナ対応か確認込みで、基底イメージ + pi-proxy.exe + 各種イメージを作る
.\setup.ps1            # 汎用 pi-sandbox:latest + pi-proxy.exe のみ
.\setup.ps1 -All       # Playwright (pi-sandbox-web) と AWS emu + Lambda RIE も
```

前提（このリポジトリの外側で用意するもの）:
- Windows 11 + `wslc`（`wsl --update` 済み）
- Sync-Strata をホストで起動できること（会話まで要るならデスクトップの **Sync-Strata**、APIだけなら `sync up --api-only`）
- 任意で GitHub CLI (`gh`)、ESP-IDF、M5Stack 実機

## 2. とりあえず動くコマンド

```powershell
# (A) 対話で作業
.\run-pi.ps1 -Workspace D:\vscode\my-repo

# (B) Web/E2E 付き（Chromium は shm 512m で起動）
.\run-pi-web.ps1 -Workspace D:\vscode\my-repo

# (C) AWS エミュレータ + Lambda（コンテナ内 AWS_ENDPOINT_URL が自動設定）
.\run-pi-aws.ps1 -Workspace D:\vscode\my-repo

# (D) 非対話の自己改善バッチ（.pi/verifier.json が通るまで最大2ラウンド自動継続）
.\run-pi-web.ps1 -Workspace D:\vscode\my-repo -PromptFile .\task.md -NonInteractive
```

Sync-Strata は必ず **先に起こしてから** 実行する（`-Model auto` は :8080 のモデル一覧を見るため）。

```powershell
sync status            # エンジンと dsh が生きているか
sync gui-url           # ブラウザで開く URL
```

## 3. 確認の最短ルート

```powershell
# 管理コンソール（起動ログに出る token 付き URL をそのまま貼る）
#   http://127.0.0.1:18080/admin/ui/?token=...
# 検証スコアの推移
Get-Content .\logs\sessions\verify-metrics.jsonl -Wait
# ハーネスの視点転換（停滞→再構築）
Get-Content .\logs\sessions\perturbation.jsonl -Wait
# 外向きの通信（遮断含む）を JSONL で見る
.\pi-proxy.exe export-logs --file .\logs\egress.bin --tail 50
```

## 4. 置き場所

| もの | 場所 |
|---|---|
| イメージ | `pi-sandbox:latest` / `pi-sandbox-web:latest` / `aws-emulator:latest` / `pi-lambda-rie:latest` |
| ゲートウェイ | `.\pi-proxy.exe`（`go-proxy/` を `wslc` の中でビルド。既存ならスキップされるので、直したら exe を消す） |
| ログ | `logs/` = `api-audit.bin` / `egress.bin` / `control.bin`（10 MiB × 5 世代）＋ `sessions/*.jsonl`（会話・計測） |
| 資料 | `refs/` → コンテナの `/refs`（読み取り専用。verifier / e2e / fixtures を置く運用が実履歴上の既定） |

すべてオプション（モデル固定、トークン再利用、Dream、ジョブ数、ポート番号など）は **[USAGE.md](./USAGE.md)** を読むこと。

## 5. ライセンス

このリポジトリのコードは **MIT License**（[LICENSE](./LICENSE)）。ただしビルドで取り込む実行環境や、外部から起動する Sync-Strata / WSL / pi / ESP-IDF / AWS Emulator は別ライセンスなので、再配布時の切り分けは [NOTICE.md](./NOTICE.md) を見ること。
