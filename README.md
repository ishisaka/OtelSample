# Go ゼロコード計装サンプル

[参考記事](https://zenn.dev/quiver/articles/d7bdf991d562e5)と同じ `otelc` のコンパイル時計装を、標準ライブラリの HTTP で試します。SDKの初期化とスパン生成はotelcに任せ、ログ配信には公式の `otelslog` ブリッジを追加しています。

Windows・macOS・Linuxで、miseを使って同じバージョンとコマンドで検証できます。Dockerなしのコンソール出力に加え、Aspire Dashboardでトレース・メトリクス・サーバーの構造化ログを表示できます。

## 1. miseをインストール

| OS | インストール |
| --- | --- |
| Windows（PowerShell） | `winget install jdx.mise` |
| macOS（Homebrew） | `brew install mise` |
| Linux | 公式インストーラー（下記参照） |

Linuxでは次のように実行し、miseをPATHに追加します。

```sh
curl https://mise.run | sh
export PATH="$HOME/.local/bin:$PATH"
```

Windowsではインストール後にターミナルを開き直してください。`mise --version` が実行できる状態にします。mise 2026.9.5で本設定を検証しています。

## 2. プロジェクトのツールを準備

プロジェクトのディレクトリで実行します。以降のコマンドは3 OS共通です。`mise run` がPATHと環境変数を設定するため、シェルへの `mise activate` の設定は必須ではありません。

```text
mise trust
mise install
mise exec -c "go version"
mise tasks
```

PowerShellでmiseのシェル連携が有効な場合、`mise exec -- go version` の `--` がmiseに渡らず、`[-- COMMAND]` が必要というエラーになることがあります。上記の `-c "go version"` はその問題を回避でき、macOS・Linuxでも使用できます。

`mise.toml` でGo **1.27.0**、otelc **1.1.0**を固定しています。Goの自動ツールチェーン切り替えも無効にしています。初回はツールとGo依存モジュールをダウンロードするため、インターネット接続が必要です。

## 3. 自動検証（Docker不要）

```text
mise run verify
```

通常ソースのビルド・静的検査、otelcでのビルド、一時サーバーの起動、HTTPリクエスト、テレメトリの確認を実行します。検証が失敗した場合は終了コードが非ゼロになります。

- `/hello` が `Hello, Gopher!` を返すこと
- `/relay?name=A%26B` が内部HTTP通信を経て `Hello, A&B!` を返すこと
- `/relay` → HTTPクライアント → `/hello` の3スパンが、同じTraceIDで正しい親子関係を持つこと
- `go.` で始まるGoランタイムメトリクスを出力すること
- OTelログに `greeting requested` / `upstream responded`、INFOレベル、`name` / `status` 属性が含まれ、それぞれのHTTPスパンとTraceID・SpanIDが一致すること

検証用サーバーはOSが割り当てた空きポートで起動し、検証後に停止します。起動待ちとテレメトリ待ちの上限は各20秒です。結果の詳細は `.work/telemetry.jsonl` と `.work/app.log` に保存します。既存の `OTEL_*` 環境変数は子プロセス内で置き換えるため、普段の接続先やサンプリング設定に影響されません。

## 4. 手動で観察（Docker不要）

1つ目のターミナルで起動します。

```text
mise run console
```

2つ目のターミナルからリクエストします。

```text
mise run request
```

`Hello, otelc!` が返り、1つ目のターミナルにトレース・メトリクス・OTelログがJSONで出力されます。数秒待ち、`TraceID`、`SpanID`、`Parent.SpanID` を確認してください。アプリのテキストログも標準エラーへ出力します。停止はCtrl+Cです。

自動計装との比較には、サーバーを停止してから次を実行します。

```text
mise run plain
```

HTTP応答とアプリのテキストログは同じですが、OpenTelemetryのスパン・メトリクス・ログは送信されません。通常ビルドではSDKが初期化されず、ログブリッジの送信先もno-opになります。GoLandの通常のRunも同様です。

## 5. Aspire Dashboardで表示

Windows・macOSではDocker Desktopを起動し、Linuxコンテナを使用してください。LinuxではDocker EngineとComposeプラグインを用意します。Dockerのサービス本体はmiseの管理対象外です。Aspire Dashboardを単体コンテナで動かすため、.NET SDKやAppHostの追加は不要です。

```text
mise run aspire
```

このタスクでAspire DashboardとCollectorを起動し、Goアプリを計装付きでビルド・起動します。別のターミナルでリクエストします。

```text
mise run request
```

ブラウザーで **http://localhost:18888** を開きます。

- **Traces（トレース）**：`otelsample` の `GET /relay` を選択すると、HTTPクライアントと `GET /hello` の親子スパンを確認できます。
- **Metrics（メトリック）**：リソース `otelsample` を選択し、`go.memory.used`、`go.goroutine.count` などを表示できます。データ到着まで数秒待ってください。
- **Structured logs（構造化）**：リソース `otelsample` の `HTTP server listening`、`greeting requested`、`upstream responded` を表示します。リクエスト中のログにはトレースへのリンクが付き、詳細で `name` や `status` 属性を確認できます。起動ログにはリクエストのトレースがないため、リンクは付きません。

```text
Go (otelc) -- OTLP/HTTP --> Collector :4318 -- OTLP/HTTP --> Aspire :18890
                                                          Browser :18888
```

Goの送信先は `http://127.0.0.1:4318`、プロトコルは `http/protobuf`、サービス名は `otelsample` です。Collectorはdebug出力とAspireへの転送を両方行います。コンテナ間の送信先 `http://aspire:18890` はDocker内部ネットワークで解決されます。Aspire 13.2.0のHTTP受信との互換性のため、Collectorの送信圧縮を `compression: none` に設定しています。

`compose.yaml` でAspire Dashboard **13.2.0**、Collector **0.135.0**を固定しています。ローカル検証用にDashboardの認証を無効にし、UIとCollectorのポートはホストのループバックアドレスにだけ公開しています。DashboardのOTLPポートはホストに公開しません。受信データはメモリ上に保持され、Dashboardの再起動で消えます。

`.work/app.log` は `mise run verify` が保存した標準エラーのテキストファイルです。Dashboardはこのファイルを読み込まず、新しくOTLP送信されたデータを表示します。単体DashboardではAppHostのResourcesやConsole logsも提供されません。`mise run console` / `mise run verify` の送信先はコンソールなので、Aspireを見るときは `mise run aspire` または `mise run otlp` を使用してください。

起動状態・転送エラーを調べる場合は、次のログを確認します。

```text
mise run aspire:logs
```

アプリとログ表示をCtrl+Cで終了し、DashboardとCollectorを停止します。

```text
mise run aspire:down
```

## タスクと生成物

| コマンド | 内容 |
| --- | --- |
| `mise run check` | 通常ソースと検証スクリプトの静的検査 |
| `mise run build` | 計装付きビルドのみ |
| `mise run verify` | ビルドからトレース・メトリクス・ログ関連付け確認まで自動実行 |
| `mise run console` | ビルドしてコンソール出力で起動 |
| `mise run otlp` | ビルドしてCollector向けに起動 |
| `mise run aspire` | Dashboard・Collectorと計装済みGoアプリを起動 |
| `mise run aspire:up` | Dashboard・Collectorのみ起動 |
| `mise run aspire:logs` | Dashboard・Collectorのログを表示 |
| `mise run aspire:down` | Dashboard・Collectorを停止・削除 |
| `mise run request` | 起動中の8080番ポートへリクエスト |
| `mise run plain` | 計装なしで起動 |

実行処理は標準Goだけで書いた `scripts/dev.go` にあります。PowerShell、Bash、curl、makeを検証用スクリプトの実行に追加導入する必要はありません。

otelcはモジュールを変更し、計装ソースを生成するため、`main.go`、`go.mod`、`go.sum` を `.work/build-*` にコピーしてビルドします。一時ディレクトリはビルド後に削除します。元の `go.mod` やソースを変更しないので、計装後も `mise run plain` で比較できます。

バイナリはWindowsでは `.work/bin/otelsample.exe`、macOS・Linuxでは `.work/bin/otelsample` です。`.work/` はGit管理対象外です。旧手順で作成したルートの `otelsample.exe` や `.tools/` は本タスクでは使用しません。

既存の `collector:up` / `collector:down` もDashboard・Collectorの両方を操作します。`collector:logs` はCollectorのログのみを表示します。

手動の起動は8080番ポートを使うので、`plain`、`console`、`otlp`、`aspire` は同時起動しないでください。同じ作業ディレクトリで複数のビルド・検証も同時に実行しないでください。

## 検証範囲

Windowsで `mise run verify` が成功し、HTTP応答・スパンの親子関係・Goランタイムメトリクスを確認済みです。`mise run aspire` と `mise run request` でCollector経由の受信も検証し、Aspire Dashboardのブラウザー画面で3スパンのトレースと `go.memory.used` の表示を確認しています。検証プログラムのmacOS/arm64・Linux/amd64向けクロスコンパイルと、Docker Composeの構文検証も成功しています。macOS・Linux上での実行は未検証です。

ログ配信とHTTPスパンへの関連付けも自動検証で確認しています。Aspireの構造化ログ画面でも、起動ログ・挨拶ログ・応答ログと、リクエストログからトレースへのリンクを確認済みです。任意の業務関数が自動でスパンになるわけではなく、対応ライブラリの処理が計装されます。

## ログ配信の仕組み

otelc v1.1.0の`slog`計装はレコードへトレース情報を付加するためのもので、ログをOTLP送信するブリッジは含みません。そのため `main.go` で `otelslog.NewHandler("OtelSample")` を設定しています。ログについてはこの明示的なブリッジ設定が必要です。

Goの `slog.NewMultiHandler` で、標準エラーへの `TextHandler` とOTelへのハンドラーに分配します。`otelslog` はotelcが初期化したグローバルLoggerProviderを利用するため、アプリ側に別のSDKやExporterを初期化する処理はありません。`slog.InfoContext(r.Context(), ...)` のContextからTraceID・SpanIDが渡り、Aspireでログとトレースを関連付けられます。

`OTEL_LOGS_EXPORTER` はmiseタスクによって、`console` / `verify` では `console`、`otlp` / `aspire` では `otlp` に設定されます。配信は非同期です。起動直後やリクエスト直後に強制終了すると、未送信のログが残る場合があります。

変更前からサーバーを起動していた場合はCtrl+Cで停止し、`mise run aspire` で再ビルド・再起動してください。

- [miseのインストール](https://mise.jdx.dev/installing-mise.html)
- [miseのGo対応](https://mise.jdx.dev/lang/go.html)
- [OpenTelemetry公式 Getting started](https://opentelemetry.io/docs/zero-code/go/compile-time/getting-started/)
- [OpenTelemetry公式 Configuration](https://opentelemetry.io/docs/zero-code/go/compile-time/configuration/)
- [Aspire Dashboard standalone](https://aspire.dev/dashboard/standalone/)
- [公式otelslogブリッジ](https://pkg.go.dev/go.opentelemetry.io/contrib/bridges/otelslog)
