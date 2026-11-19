# pi-sandbox Meta-Harness

Windows 11 ホスト上の **Sync-Strata**（GPU 推論）と、`wslc.exe` で隔離した **pi coding agent** を安全に連携させるためのメタハーネス環境です。

---

## 1. 全体構成

```text
[Windows ホスト]
  ├─ Sync-Strata (127.0.0.1:8080)       ... 推論エンジン (qwen3.8-flash-next-sync-s)
  ├─ pi-proxy.exe (Go / :18080, :18888) ... 統合ゲートウェイ + バイナリログ (10MB x 5世代)
  │    ├─ :18080  API 監査プロキシ / /gh-ci / /esp-idf/run / /admin/* (UI: /admin/ui)
  │    ├─ :18888  許可リスト型 Egress プロキシ (allowlist.json 以外は 403 遮断)
  │    └─ :19297  aws-emulator (HTTP->HTTPS) + pi-lambda-rie サブコンテナ実行 (-WithAwsEmulator)
  └─ logs/                              ... バイナリ監査ログ (.bin) + セッション・メトリクス (.jsonl)
       ▲
       │ (WSL vEthernet IP 経由 / Bearer トークン認証)
       ▼
[wslc コンテナ (非 root: agent, --cap-drop=ALL)]
  ├─ /workspace (rw)                    ... 作業リポジトリ (-Workspace)
  ├─ /refs      (ro)                    ... 資料・参考コード (-RefsDir)
  ├─ ~/.pi/agent/ (ro)                  ... ハーネス設定・拡張・Skills・Agents (sessions のみ rw)
  └─ /usr/local/bin/idf.py (ro)         ... ホスト ESP-IDF + QEMU 呼び出しシム
```

---

## 2. コンテナイメージと起動スクリプト

| 用途 | イメージ名 | 起動スクリプト | 特徴 |
|---|---|---|---|
| **汎用・M5Stack** | `pi-sandbox:latest` | `.\run-pi.ps1` | Node.js 22 / npm / Rust (cargo) / Go / uv / gcc / make / git / rg。<br>`idf.py` シム経由でホストの ESP-IDF + QEMU も利用可能。 |
| **Web UI (Playwright)** | `pi-sandbox-web:latest` | `.\run-pi-web.ps1` | 上記 + Playwright (Chromium のみ)。`--shm-size 512m` で起動。 |
| **AWS Emulator + Lambda** | `aws-emulator:latest` + `pi-lambda-rie:latest` | `.\run-pi-aws.ps1` | AWS 公式エミュレータ（S3/SQS/DynamoDB）＋ Lambda RIE サブコンテナ。`-Update` で上流追従。 |
| **完全隔離 ESP-IDF** *(任意)* | `pi-sandbox-esp:latest` | `.\run-pi-esp.ps1` | コンテナ内に ESP-IDF v5.4 + QEMU を内蔵。 |

### 初回ビルドコマンド
```powershell
# 基本イメージ
wslc build -t pi-sandbox:latest .

# Web UI (Playwright) 用
wslc build -f Dockerfile.web -t pi-sandbox-web:latest .

# AWS Emulator + Lambda RIE サブコンテナ用
wslc build -f Dockerfile.aws-emulator -t aws-emulator:latest .
wslc build -f Dockerfile.lambda -t pi-lambda-rie:latest .
```
※ `pi-proxy.exe` は未ビルドの場合 `.\run-pi.ps1` 実行時に `wslc` 経由で自動ビルドされます。

---

## 3. 起動方法

```powershell
# 基本の起動（対話モード）
.\run-pi.ps1 -Workspace <作業フォルダ>

# Web UI (Playwright) を使う場合
.\run-pi-web.ps1 -Workspace <作業フォルダ>

# AWS Emulator + Lambda RIE を使う場合
.\run-pi-aws.ps1 -Workspace <作業フォルダ>

# 非対話バッチ実行（ハーネス評価用）
.\run-pi-web.ps1 -Workspace <作業フォルダ> -PromptFile <プロンプトファイル>
```

---

## 4. ログ観測と JSONL 変換

`pi-proxy.exe` は `api-audit.bin` / `egress.bin` / `control.bin` を CRC32 付きバイナリログとして保存し（10 MiB × 5 世代）、CLI または管理 API から JSONL へ変換できます。

```powershell
# CLI で JSONL 変換
.\pi-proxy.exe export-logs --file .\logs\api-audit.bin --tail 50

# ハーネス・自己改善ループのリアルタイム監視
Get-Content .\logs\sessions\verify-metrics.jsonl -Wait
Get-Content .\logs\sessions\perturbation.jsonl -Wait
```

管理コンソール（Strata 直接対話・SaaS AI マッチング・ヘルス監視・ジョブ一覧）はプロキシ起動中に `http://127.0.0.1:18080/admin/ui` から利用できます。
