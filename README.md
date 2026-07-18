# korocon

Go から OpenAI Codex CLI などの AI CLI を起動するための薄いオーケストレーターです。

OpenAI Codex CLIとGitHub CLI (`gh`)のインストール・ログインが必要です。

CLIを起動するとCodexを常駐プロセスとして1回だけ起動し、入力待ちになります。標準入力から受けた指示は同じCodexセッションへ順番に送り、最終結果を画面に表示します。JSONイベントと標準エラーは`korocon.log`へ追記します。Ctrl+CでCLIとCodexを停止します。

各ジョブの開始前に対象リポジトリで`git fetch --prune origin`と`git pull --ff-only`を実行します。同期に失敗した場合はAIジョブを開始せず、エラーを表示します。

設定は実行バイナリと同じディレクトリの`config.json`から読み込みます。`workspaceName`で成果物ディレクトリ名、`builtinAllowedCommands`でCodexの自動承認対象コマンドを指定します。

```json
{
  "workspaceName": ".workspace",
  "branchNamePattern": "issue_#<issue番号>",
  "implementationDirectory": "../<リポジトリ名>-branches/",
  "implementationLoopCount": 3,
  "baseBranch": "main",
  "implementerProvider": "codex",
  "implementerModel": "gpt-5.6-luna",
  "verifierProvider": "codex",
  "verifierModel": "gpt-5.4-mini",
  "reviewerProvider": "copilot",
  "reviewerModel": "claude-sonnet-4.5",
  "startupCommand": "go run ./cmd/app",
  "builtinAllowedCommands": ["go test", "git diff", "git status"]
}
```

実装者・検証者・レビューアはProviderとModelを個別指定できます。`verifierProvider`、`verifierModel`、`reviewerProvider`、`reviewerModel`の未指定値は実装者と同じ値になります。CLIでは`--implementer-provider`、`--implementer-model`、`--verifier-provider`、`--verifier-model`、`--reviewer-provider`、`--reviewer-model`で設定ファイルを上書きします。既存の`--provider`と`--model`は実装者指定として利用できます。

`builtinAllowedCommands`を省略した場合はkorobokcleと同じ既定コマンドを使用します。許可外の操作は自動拒否せず、未入力Enterまたは`/approve`で今回だけ承認、`/allow`で承認して自動承認リストへ追加、`/decline`で拒否します。

起動直後にGitHubから取得する情報を`issue`または`pr`で選択します。`issue`を選んで番号を入力すると、Issue本文・ラベル・コメントを取得し、状態ラベルに応じて設計または実装をCodexへ自動投入します。`pr`を選ぶとMERGEDまたはDraftを除外したPR一覧を番号・ステータス・タイトルの表形式で表示し、指定したPRのレビューを開始します。この機能には認証済みのGitHub CLI (`gh`) が必要です。

GitHubがPRを`CONFLICTING`または`DIRTY`と判定した場合、一覧には`コンフリクト`と表示し、未レビューやレビュー修正状態より優先してコンフリクト解消を開始します。実装者AIが`resolve-pr-conflicts`スキルに従ってPR headのworktreeを修正し、承認後にmerge commitをheadブランチへpushします。

対象番号が分かっている場合は選択を省略できます。指定対象が存在しない、または処理対象外の場合は理由を表示し、通常の`issue`/`pr`選択へ戻ります。`--issue`と`--pr`は同時指定できません。

```sh
go run ./cmd/korocon --issue 42
go run ./cmd/korocon --pr 4
```

PRレビュー完了後は、未入力Enterなどで承認、`/rerun [補足]`でレビュー再実行、それ以外の入力でレビュー指摘の修正を開始します。修正はPR head用worktreeで設計検討・実装・テストを行い、承認後にcommit・pushして再レビューします。レビュー承認後は`startupCommand`が設定されていれば自動起動して動作確認へ進み、PRをCLOSEDまたはMERGEDにして未入力Enterまたは`/check`を入力すると完了します。未設定ならレビュー承認時点でPR処理を終了します。どちらも終了後は最初の`issue`/`pr`選択へ戻ります。

Issueに`state:design_approved`がなければ設計、あれば実装を行います。開始時と完了時のラベルはkorobokcleと同じ状態遷移で更新します。

```text
設計: state:design_running -> state:design_ready
実装: state:implementation_running -> state:implementation_ready
失敗: state:failed
```

設計結果を承認すると、設計用Codexを停止して実装ジョブを自動開始します。既定では`../<リポジトリ名>-branches/<リポジトリ名>-<Issue番号>`にworktreeを作成し、`branchNamePattern`から生成したブランチをチェックアウトします。既存ディレクトリがある場合はworktree作成をスキップします。

実装ジョブは実装用Codexと読み取り専用の検証用Codexを起動します。実装後に検証し、問題があれば検証指摘を実装用Codexへ返します。`implementationLoopCount`回以内に検証が合格すると結果を表示して承認待ちになります。既定値は3回、上限は10回です。

実装中の表示例:

```text
[job 1] 実行中(実装1回目)...
[job 2] 実行中(検証1回目)...
```

設計または実装の結果を表示した後、区切り線を挟んでCLIは承認または修正指示の入力を待ちます。未入力状態でEnter、`承認`、`approve`、`a`などを入力すると承認します。設計は`state:design_approved`へ更新し、設計結果をIssueコメントへ保存します。実装はworktreeの変更をcommit・pushしてPRを作成し、成功後に`state:pr_created`へ更新します。PR URLを表示した後は現在のCodexセッションを停止し、最初の`issue`/`pr`選択へ戻ります。

それ以外の文字列を入力するとフィードバックとして同じCodexセッションへ送り、設計または実装を再実行します。再実行結果の表示後も同じ承認入力へ戻ります。

設計・実装結果はkorobokcleと同じ規則で保存します。

```text
<repository>/<workspaceName>/design/<issue番号>_<正規化タイトル>.md
<repository>/<workspaceName>/implementation/<issue番号>_<正規化タイトル>.md
<repository>/<workspaceName>/implementation/<issue番号>/<実装回数>回目_実装.md
<repository>/<workspaceName>/implementation/<issue番号>/<検討回数>回目_検討.md
```

実装・検証ループでは各AIターンの応答直後に回数付きファイルを保存します。回数なしの実装ファイルは、検証合格後に承認対象として保存する最終成果物です。

起動時に、入力欄の上へ実装者・検証者・レビューアそれぞれのProviderとModel、設定ファイル、workspace名、ログファイルが表示されます。

端末入力では`Shift+Enter`で改行、`Enter`で送信します。左右・上下矢印で入力中のカーソルを移動できます。
Ctrl+Cまたは`exit`の入力で、実行中のAIを停止してCLIを終了します。

起動すると `> ` が表示され、テキスト入力を受け付けます。指示を入力すると、実行中のジョブにプロバイダーとモデルを併記した `[job 1] 実行中...` が表示され、完了時にその行が消えて`[job 1] 完了`へ置き換わり、その下に結果が表示されます。

入力の先頭に `/` を付けるとコマンドを実行できます。`/model` で選択可能なモデルを表示し、`/model 2` または `/model gpt-5.6-terra` のように入力すると、常駐中のCodexへモデル変更を送信して次のターンから切り替えます。先頭に空白などがある `/model` は通常のプロンプトとして扱われます。

Codexが操作の承認を要求した場合は内容を画面に表示します。未入力Enterまたは`/approve`で承認、`/allow`で承認して`config.json`へ自動承認コマンドを追加、`/decline`で拒否します。

直前に完了したジョブの修正差分は`/diff`で表示できます。`/diff ファイル名`と入力すると、差分を作業ディレクトリのファイルへ保存します。

```sh
go run ./cmd/korocon --dir .
```

プロンプトを標準入力から渡すこともできます。

```sh
printf 'pr\nこのリポジトリの構成を説明して\n' | go run ./cmd/korocon
```

Linuxでは通常のフォアグラウンドCLIとして起動します。Codexはバックグラウンドの常駐`app-server`として動き、入力は標準入力のJSONLで同じthreadへ渡されます。

```sh
printf '%s\n' 'このリポジトリの構成を説明して' | go run ./cmd/korocon
go run ./cmd/korocon --dir /path/to/repository
```

実装者のデフォルトProviderは `codex` です。利用できるモデルは `gpt-5.6-sol`、
`gpt-5.6-terra`、`gpt-5.6-luna`、`gpt-5.5`、`gpt-5.4`、`gpt-5.4-mini`です。
デフォルトは`gpt-5.6-luna`で、`--implementer-model`または互換オプションの`--model`で変更できます。
`--binary` で実行ファイルを変更できます。

`--stream-logs`でAI CLIの標準出力と標準エラーをリアルタイム表示できます。試験期間中はデフォルトONですが、正式仕様ではデフォルトOFFに変更予定です。
コマンドはシェルを経由せず、引数を分離したまま起動します。

詳細は [docs/usage.md](docs/usage.md) を参照してください。
対話型CLIの詳細は [docs/daemon.md](docs/daemon.md) を参照してください。
実装設計は [docs/design.md](docs/design.md) を参照してください。
