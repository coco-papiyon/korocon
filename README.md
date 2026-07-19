# korocon

Go から OpenAI Codex CLI などの AI CLI を起動するための薄いオーケストレーターです。

OpenAI Codex CLIとGitHub CLI (`gh`)のインストール・ログインが必要です。

CLIを起動するとCodexを常駐プロセスとして1回だけ起動し、入力待ちになります。標準入力から受けた指示は同じCodexセッションへ順番に送り、最終結果を画面に表示します。JSONイベントと標準エラーは`korocon.log`へ追記します。Ctrl+CでCLIとCodexを停止します。

各ジョブの開始前に対象リポジトリで`git fetch --prune origin`と`git pull --ff-only`を実行します。同期に失敗した場合はAIジョブを開始せず、エラーを表示します。

設定は実行バイナリと同じディレクトリの`config.json`から読み込みます。`workspaceName`で成果物ディレクトリ名、`builtinAllowedCommands`でCodexの自動承認対象コマンドを指定します。

`korocon config init`で設定ファイルを対話作成できます。`baseBranch`、`branchNamePattern`、`startupCommand`を入力し、空入力では既定値を使用します。続けて実装者・検証者・レビューアのProviderとModelを設定します。Model候補は選択したProviderごとに表示し、Copilotでは`auto`を選択できます。既存ファイルを再初期化する場合は`--force`を指定します。モデル設定だけを変更する場合は`korocon config model`を使用します。

```sh
korocon config init
korocon config model
korocon config allow "go test ./..."
```

`korocon config allow`は`builtinAllowedCommands`へ自動承認コマンドを追加します。コマンド引数を省略すると対話入力になります。大文字・小文字と連続空白を正規化して重複を判定します。

```json
{
  "workspaceName": ".workspace",
  "branchNamePattern": "issue_#<issue番号>",
  "implementationDirectory": "../<リポジトリ名>-branches/",
  "implementationLoopCount": 3,
  "autoPollingInterval": "5m",
  "baseBranch": "main",
  "implementerProvider": "codex",
  "implementerModel": "gpt-5.6-luna",
  "verifierProvider": "codex",
  "verifierModel": "gpt-5.4-mini",
  "reviewerProvider": "copilot",
  "reviewerModel": "claude-sonnet-4.5",
  "reviewer": "octocat",
  "startupCommand": "go run ./cmd/app",
  "builtinAllowedCommands": ["git add", "git diff", "git status", "go test"]
}
```

実装者・検証者・レビューアはProviderとModelを個別指定できます。`verifierProvider`、`verifierModel`、`reviewerProvider`、`reviewerModel`の未指定値は実装者と同じ値になります。CLIでは`--implementer-provider`、`--implementer-model`、`--verifier-provider`、`--verifier-model`、`--reviewer-provider`、`--reviewer-model`で設定ファイルを上書きします。既存の`--provider`と`--model`は実装者指定として利用できます。

`reviewer`にはGitHub上のレビュー担当ユーザーを指定します。実装承認後に新しいPRを作成すると、PRのassigneeには現在のGitHubユーザー（`@me`）を設定し、`reviewer`が設定されている場合はそのユーザーへレビューを依頼します。

`builtinAllowedCommands`を省略した場合はkorobokcleと同じ既定コマンドを使用します。許可外の操作は自動拒否せず、未入力Enterまたは`/approve`で今回だけ承認、`/allow`で承認して自動承認リストへ追加、`/decline`で拒否します。

起動直後にGitHubから取得する情報を`ISSUE`または`PR`で選択します。入力は大文字・小文字を区別せず、`i`/`I`と`p`/`P`だけでも指定できます。未入力Enterは`ISSUE`です。`ISSUE`を選んで番号を入力すると、Issue本文・ラベル・コメントを取得し、状態ラベルに応じて設計または実装をCodexへ自動投入します。`PR`を選ぶとMERGEDまたはDraftを除外したPR一覧を番号・ステータス・タイトルの表形式で表示し、指定したPRのレビューを開始します。PRがない場合やIssueがopenでない場合はISSUE/PR選択へ戻ります。この機能には認証済みのGitHub CLI (`gh`) が必要です。

担当者別の起動は`--implementer`（`-i`）または`--reviewer`（`-r`）で指定できます。`--implementer`は実装者が担当するIssue、コンフリクトPR、レビュー指摘修正PRを表示します。`--reviewer`はIssue/PRの種別選択を省略し、PR自身に`state:*`ステータスがない未レビューPRだけを表示します。Issue側のラベルは判定に使用しません。

担当者別のIssue/PR一覧は番号降順で表示され、番号を未入力または空白のみでEnterすると一覧先頭の対象を選択します。フィルタ指定時はフィルタ適用後の一覧が対象です。

レビュー指摘修正PRを選択すると、PRの一般コメント・レビュー本文・行単位レビューコメントを`.workspace/review_fix/`へ保存して表示します。未入力Enterでは保存内容をそのまま修正し、文字入力では修正対象・修正不要対象を指定できます。その後、実装者と検証者が実装・検証を既定3回まで繰り返し、回数別の結果を同ディレクトリへ保存します。

`--auto`を追加すると、フィルタに一致する対象を一覧の上から順番に処理します。各処理の完了後に一覧を再取得し、次の対象へ進みます。対象がない場合は「Enterで再取得、`autoPollingInterval`（既定`5m`）後に再取得します」と表示して待機します。待機中もCtrl+Cで終了できます。実装者モードではIssueを優先し、対象Issueがない場合だけPRを処理します。`--auto`には`-i`または`-r`が必要です。

```sh
go run ./cmd/korocon -i --auto
go run ./cmd/korocon -r --auto
```

`--assigne <ユーザー名>`でIssue/PRの担当者を指定できます。省略時は`gh api user --jq .login`の現在ユーザーを使用し、空白指定時は担当者フィルタを無効にします。

追加フィルタとして、`--label`、`--exclude-label`、`--title`、`--author`、`--search`を使用できます。ラベル、除外ラベル、タイトル、作成者は複数回指定できます。GitHub Projects v2で絞り込む場合は、`--project <番号>`、`--project-owner <owner>`、`--project-status <Status>`を指定します。Status以外のProjectフィールドには`--project-query`を使用できます。

```sh
go run ./cmd/korocon -r --label backend --exclude-label blocked --author coco-papiyon
go run ./cmd/korocon -i --project 3 --project-owner coco-papiyon --project-status "In Progress"
```

GitHubがPRを`CONFLICTING`または`DIRTY`と判定した場合、一覧には`コンフリクト`と表示し、未レビューやレビュー修正状態より優先してコンフリクト解消を開始します。実装者AIが`resolve-pr-conflicts`スキルに従ってPR headのworktreeを修正し、承認後にmerge commitをheadブランチへpushします。

対象番号が分かっている場合は選択を省略できます。指定対象が存在しない、または処理対象外の場合は理由を表示し、通常の`issue`/`pr`選択へ戻ります。`--issue`と`--pr`は同時指定できません。

```sh
go run ./cmd/korocon --issue 42
go run ./cmd/korocon --pr 4
```

PRレビューの`## 結果`が`要修正`または`コメントあり`の場合も、結果を表示して承認待ちになります。未入力Enterなどで承認するとレビューOKとして動作確認または終了へ進み、指摘内容を入力して送信するとPRへ登録して`state:pr_review_comment`へ更新し、最初の選択へ戻ります。`/rerun [補足]`でレビューを再実行できます。次に同じPRを選択すると実装者がPR head用worktreeで設計検討・実装・テストを行います。修正承認後も選択へ戻り、再レビューはレビューアの新しいセッションで行います。レビュー承認後は`startupCommand`が設定されていればレビューア工程として動作確認へ進み、PRをCLOSEDまたはMERGEDにして未入力Enterまたは`/check`を入力すると完了します。未設定なら「動作確認後にPRをマージしてください。」とPR URLを表示してPR処理を終了し、最初の選択へ戻ります。

Issueに`state:design_approved`がなければ設計、あれば実装を行います。開始時と完了時のラベルはkorobokcleと同じ状態遷移で更新します。

```text
設計: state:design_running -> state:design_ready
実装: state:implementation_running -> state:implementation_ready
失敗: state:failed
```

Issueジョブが状態ラベルの更新に成功して実際に開始されると、工程とIssue番号を含むメッセージを表示します。キュー投入だけでは表示せず、設計・実装の再実行時もジョブごとに表示します。

```text
Issue #2の設計を開始します。
---
```

```text
Issue #2の実装を開始します。
---
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

起動時に、入力欄の上へ主要設定をAI・GitHub・Workflowのグループに分けて表示します。実装者と同じ設定の検証者・レビューアは省略されます。詳細設定は`config.json`で確認できます。

```text
AI:
  implementer     : codex / gpt-5.6-luna / codex
  verifier        : copilot / claude-sonnet / copilot

GitHub:
  github reviewer : 未設定

Workflow:
  branch          : issue_#<issue番号>
  base branch     : main
  startup command : 未設定
```

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

Linuxでは通常のフォアグラウンドCLIとして起動します。Codexはバックグラウンドの常駐`app-server`として動き、入力は標準入力のJSONLで同じthreadへ渡されます。`workspace-write` sandboxは維持したまま、GitHub APIなどへ接続できるよう`network_access=true`を明示します。

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
