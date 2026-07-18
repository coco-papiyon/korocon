# korocon

Go から GitHub Copilot CLI などの AI CLI を起動するための薄いオーケストレーターです。
`korobokcle` の worker 実行・作業ディレクトリ・許可制御の考え方を参考にしています。

## 必要なもの

- Go 1.22 以上
- OpenAI Codex CLI (`codex`) とログイン済みのOpenAIアカウント

## 使い方

```sh
go build -o ./korocon ./cmd/korocon
./korocon --dir .
```

### 設定ファイル

`korocon`は実行バイナリと同じディレクトリにある`config.json`を読み込みます。ファイルが存在しない場合は既定値を使用します。`go run`では一時ディレクトリに実行バイナリが作られるため、設定ファイルを利用するときはビルドしたバイナリを起動してください。

```text
tools/
  korocon
  config.json
```

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

`workspaceName`は対象リポジトリ直下に作成する成果物ディレクトリの名前です。絶対パス、`..`、パス区切りを含む値は指定できません。

| 設定 | 既定値 | 内容 |
| --- | --- | --- |
| `branchNamePattern` | `issue_#<issue番号>` | 実装worktreeのブランチ名。`<issue番号>`または`<issueNumber>`を置換します。 |
| `implementationDirectory` | `../<リポジトリ名>-branches/` | 実装worktreeを置く親ディレクトリ。相対パスは対象リポジトリ基準で、`<リポジトリ名>`または`<repositoryName>`を置換します。 |
| `implementationLoopCount` | `3` | 実装と検証の最大試行回数。最大10回です。 |
| `baseBranch` | `main` | 実装承認時に作成するPRのbaseブランチです。 |
| `startupCommand` | 未設定 | レビュー承認後にPR headのworktreeで自動起動する動作確認コマンドです。標準出力と標準エラーはログファイルへ記録します。 |
| `builtinAllowedCommands` | korobokcleと同じ既定リスト | Codexのコマンド実行要求を自動承認するコマンドです。省略または空配列では既定リストを使用します。 |
| `implementerProvider` | `codex` | 設計、実装、レビュー指摘修正を担当するProviderです。 |
| `implementerModel` | `gpt-5.6-luna` | 実装者のModelです。 |
| `verifierProvider` | 実装者と同じ | 実装検証を担当するProviderです。 |
| `verifierModel` | 実装者と同じ | 検証者のModelです。 |
| `reviewerProvider` | 実装者と同じ | PRレビューを担当するProviderです。 |
| `reviewerModel` | 実装者と同じ | レビューアのModelです。 |

設定ファイル値はCLI引数で上書きできます。`--provider`と`--model`は互換性のため残しており、実装者指定として扱います。

```sh
korocon \
  --implementer-provider codex --implementer-model gpt-5.6-luna \
  --verifier-provider codex --verifier-model gpt-5.4-mini \
  --reviewer-provider copilot --reviewer-model claude-sonnet-4.5
```

プロンプトを標準入力から渡すこともできます。

```sh
cat prompt.md | go run ./cmd/korocon
```

## 対話型CLIとしての実行

`korocon`は起動時に`codex app-server --stdio`を1回だけ起動してから標準入力を待機します。各入力を同じCodex threadへ順番に送り、AIの最終結果を画面に表示します。通常時の空行は送信しませんが、Issueの承認待ちでは空行を承認として扱います。CodexのJSONイベントと標準エラーはログファイルへリアルタイム追記します。

通常指示、設計、実装、再設計、再実装を含むすべてのAIジョブは、開始前に対象リポジトリで次を実行します。

```sh
git fetch --prune origin
git pull --ff-only
```

fetchまたはfast-forward pullに失敗した場合は、そのAIジョブを開始せず`[job N] 失敗`を表示します。自動mergeやrebaseは行いません。競合、未追跡ブランチ、認証・ネットワークエラーなどの原因を解消してからジョブを再投入してください。

起動直後に、GitHubから取得する情報として`issue`または`pr`を選択します。`issue`の場合は続けてIssue番号を入力します。koroconは`gh issue view --json`で本文・ラベル・コメントなどを取得し、Issueの状態から設計または実装を判定してCodexへ初期ジョブとして投入します。`pr`の場合は、番号、ステータス、タイトルを列とする表形式のPR一覧を表示し、`MERGED`またはDraftがtrueのPRは除外します。`state:*`ラベルがある場合は、工程ラベルを日本語ステータスへ変換して表示します。続けてPR番号を入力すると、選択したPRの本文、ブランチ、コメント、レビューを取得してレビューを開始します。事前にGitHub CLI (`gh`) のログインを完了してください。

標準入力から実行する場合も、最初の行に`issue`または`pr`を指定します。

Issue番号またはPR番号が分かっている場合は、起動引数で最初の選択を省略できます。

```sh
korocon --issue 42
korocon --pr 4
```

`--issue`と`--pr`は同時指定できません。指定されたIssueまたはPRを取得できない場合は理由と`通常の選択へ戻ります。`を表示し、`取得する情報を選択してください (issue/pr):`から通常の選択を受け付けます。直接指定は起動直後の1回だけ使用し、ジョブ完了後は通常の選択へ戻ります。

### PRレビュー

PRの`mergeable`が`CONFLICTING`、または`mergeStateStatus`が`DIRTY`の場合は、未レビューやレビュー修正状態よりコンフリクト判定を優先します。一覧のステータスには`コンフリクト`と表示し、選択するとレビューではなくコンフリクト解消を開始します。

コンフリクト解消では実装者のProviderとModelを使用し、PR head用worktreeでbaseブランチのmergeを開始してから`resolve-pr-conflicts`スキルを実行します。競合ファイル、head/baseブランチ、双方に対応するIssueの意図を確認し、結果を`<workspaceName>/pr_conflict/<PR番号>_<正規化タイトル>.md`へ保存します。状態は`state:pr_conflict_running`、`state:pr_conflict_ready`、承認後の`state:pr_conflict_resolved`の順に遷移します。承認時に未解消ファイルと競合マーカーを検査し、merge commitをPR headへpushして最初のIssue/PR選択へ戻ります。

PRレビューはリポジトリの`review-pull-request`スキルに従い、結果を`<workspaceName>/review/<PR番号>_<正規化タイトル>.md`へ保存します。実行中は`state:review_running`、確認待ちは`state:review_ready`です。

レビュー結果の確認待ちでは次の入力を使用します。

| 入力 | 処理 |
| --- | --- |
| 未入力Enter、`承認`、`approve`、`a` | レビューを承認し、動作確認へ進む |
| `/rerun`、`/rerun <補足>` | 同じPRのレビューを再実行する |
| `/fix <指示>`または任意の文字列 | レビュー修正指示として修正の検討・実装を開始する |

レビュー修正指示はPRコメントへ投稿し、`state:pr_review_comment`へ更新します。修正ジョブは`../<リポジトリ名>-branches/<リポジトリ名>-pr-<PR番号>`へPR headのworktreeを作り、`review-comment-fix`スキルに従って設計検討、実装、テストを行います。結果は`<workspaceName>/review_fix_implementation/<PR番号>_<正規化タイトル>.md`へ保存します。承認すると変更をcommitしてPR headへpushし、`state:review_fixed`へ更新してレビューを再実行します。

レビュー承認後、`startupCommand`が設定されていればコマンドを自動起動し、`state:review_approved`として動作確認を待ちます。動作確認を完了してPRをクローズまたはマージした後、未入力Enterまたは`/check`を入力します。PRがCLOSEDまたはMERGEDならコマンドを停止して`state:completed`へ更新し、最初の`issue`/`pr`選択へ戻ります。OPENの場合は動作確認待ちを継続します。`startupCommand`が未設定の場合はレビュー承認時点でPR処理を終了し、最初の選択へ戻ります。

### Issueの状態遷移

Issue処理ではkorobokcleと同じ状態ラベルを使います。対象ラベルが存在しない場合は`gh label create --force`で作成し、状態更新時は既存の非状態ラベルを保持します。

| 条件 | 処理 | 開始時 | 正常完了時 |
| --- | --- | --- | --- |
| `state:design_approved`がない | 設計 | `state:design_running` | `state:design_ready` |
| `state:design_approved`がある | 実装 | `state:implementation_running` | `state:implementation_ready` |

AI実行が失敗した場合は`state:failed`へ更新します。正常完了時は結果を表示して`state:design_ready`または`state:implementation_ready`へ更新し、同じCLIで承認または修正指示を待ちます。

ジョブの開始処理で状態ラベルの更新に成功した後、AI処理に入る前に次の開始メッセージを表示します。設計承認による実装ジョブのキュー投入時ではなく、実装ジョブが実際に開始された時点で表示します。

```text
Issue #2の設計を開始します。
---
Issue #2の実装を開始します。
---
```

再設計・再実装でもジョブ開始ごとに1回表示します。状態更新などで開始処理に失敗した場合や、ジョブがキューに入っただけで開始されていない場合は表示しません。

結果と承認案内の間には区切り線を表示します。承認として扱う入力は、未入力状態でEnter、`承認`、`approve`、`a`などです。設計承認時は結果をIssueコメントへ保存して`state:design_approved`へ更新します。実装承認時はworktreeの変更をcommitし、同名のリモートブランチがあればrebaseしてpushした後、PRを作成して`state:pr_created`へ更新します。PR URLを表示した後は実装用・検証用Codexを停止し、最初の`issue`/`pr`選択へ戻ります。PR作成に失敗した場合は現在の承認待ちを維持します。それ以外の入力はフィードバックとしてAIへ送信し、同じ工程を再実行します。再実行の完了後も結果を表示して承認入力へ戻ります。

### 実装ジョブ

設計承認後は実装ジョブを自動投入します。対象リポジトリが`/home/user/project`、Issue番号が`42`の場合、既定のworktreeは`/home/user/project-branches/project-42`です。

```text
git -C <repository> worktree add -B <branchName> ../<repositoryName>-branches/<repositoryName>-<issueNumber> HEAD
```

worktreeパスがすでに存在する場合は作成コマンドを実行せず、そのディレクトリを利用します。

設計用Codexは設計承認まで常駐し、実装開始時に停止します。その後、同じworktreeを作業ディレクトリとする2つのCodex app-serverを起動します。

1. 実装用Codexが承認済み設計に従って実装とテストを行う
2. 検証用Codexが読み取り専用sandboxで設計、差分、テスト結果を検証する
3. `changes_requested`なら指摘を実装用Codexへ返して再実装する
4. `passed`なら実装成果物を保存して承認待ちへ進む
5. 設定回数までに合格しなければ`state:failed`へ更新する

内部フェーズの表示は次の形式です。検証指摘で再実装に戻る場合も、内部ジョブ番号を進めます。

```text
[job 1] 完了(実装1回目)
[job 2] 完了(検証1回目)
[job 3] 実行中(実装2回目)...
```

フェーズが切り替わると、直前の実行中行は完了履歴として改行確定され、次のフェーズが新しいジョブ番号で表示されます。同じフェーズ内の進捗更新は現在行を上書きします。

実装結果への修正指示では同じ2つのCodexセッションを継続利用します。実装承認または実装ジョブ失敗時に両セッションを停止します。

実装承認時のPRは`baseBranch`をbase、実装worktreeのブランチをhead、IssueタイトルをPRタイトルとして作成します。本文には保存済み実装結果と`Closes #<Issue番号>`を含めます。PR作成済みでラベル更新だけ失敗した場合は、再承認時に既存PRを再利用します。commit、rebase、push、PR作成のいずれかが失敗した場合は`state:implementation_ready`とCodexセッションを維持し、エラーを表示して再承認または修正を待ちます。

正常完了した結果は承認前にMarkdown成果物として保存します。再実行時は同じファイルを上書きします。

実装・検証ループの各応答も、次の処理へ進む前に回数付きMarkdownとして保存します。検証処理の応答は`<N>回目_検討.md`として保存します。検証結果がJSONとして不正で後続処理が失敗した場合も、受信済みの検証応答はファイルに残ります。同じ実装ジョブを再実行した場合は回数が1から始まり、同名ファイルを上書きします。

| 工程 | 保存先 |
| --- | --- |
| 設計 | `<repository>/<workspaceName>/design/<issue番号>_<正規化タイトル>.md` |
| 実装 | `<repository>/<workspaceName>/implementation/<issue番号>_<正規化タイトル>.md` |
| 実装N回目 | `<repository>/<workspaceName>/implementation/<issue番号>/<N>回目_実装.md` |
| 検証N回目 | `<repository>/<workspaceName>/implementation/<issue番号>/<N>回目_検討.md` |

最終成果物のファイル名はタイトルを小文字化し、空白や`/`、`\\`、`:`, `#`, `.`, `,`, `(`, `)`を`-`へ置換します。中間成果物はIssue番号のディレクトリへ回数と工程名で保存し、先頭見出しにも`実装 <N>回目`または`検討 <N>回目`を付けます。

起動時点ですでに`state:design_ready`または`state:implementation_ready`のIssueは、別セッションで完了した承認待ちとして自動実行しません。`state:implementation_approved`または`state:pr_created`のIssueも処理済みとして実行しません。

Codexへ渡す内容は「設計または実装を行う」という工程指示と、Issue番号・タイトル・URL・作成者・本文・ラベル・コメントです。具体的な手順と成果物形式は対象リポジトリのスキルに委ねます。ラベル操作などのワークフロー制御はAIプロンプトへ含めず、korocon自身が実行します。

起動時は入力欄の上に実装者・検証者・レビューアのProviderとModel、設定ファイル、ログファイルが表示され、その下に入力待ちの`> `が表示されます。

入力の先頭が `/` の行はコマンドとして扱われます。`/model` で選択可能なモデルを表示し、`/model 1` のように番号、または`/model gpt-5.6-terra` のようにモデル名を指定して切り替えます。koroconは常駐中のCodexへ`/model`相当のモデル変更要求を標準入力で送信し、Codexの成功応答後に表示中のモデルを更新します。選択したモデルは次のターンから同じthreadへ適用されます。先頭に空白がある行はコマンドではなくプロンプトです。

Codexがコマンド実行を要求した場合、`builtinAllowedCommands`に一致するコマンドは自動承認し、`[自動承認]`と対象を表示します。完全一致のほか、安全な引数やLinuxの安全な環境変数代入を付けた実行とCodexが提示する`proposedExecpolicyAmendment`、`commandActions`を判定します。`;`、`&&`、パイプ、コマンド置換などを含む複合実行は、許可コマンドから始まっていても自動承認しません。

許可リストに一致しない操作やファイル変更要求は画面へ表示します。未入力状態でEnterまたは`/approve`を入力すると今回だけ承認し、`/decline`で拒否します。`/allow`を入力すると今回の操作を承認し、Codexの`commandActions`から抽出した具体的なコマンドを実行中の許可リストとバイナリ横の`config.json`へ追加します。Linuxの先頭環境変数代入は除去して保存するため、`GOCACHE=/tmp/cache go test ./...`は`go test ./...`として追加されます。設定保存に失敗した場合は承認せず、承認待ちを継続します。`--dangerously-bypass-approvals-and-sandbox`は使用しません。

```text
implementer: codex / gpt-5.6-luna / codex
verifier: codex / gpt-5.4-mini / codex
reviewer: copilot / claude-sonnet-4.5 / copilot
config: /path/to/config.json
workspace: .workspace
branch: issue_#<issue番号>
implementation directory: ../<リポジトリ名>-branches/
implementation loops: 3
log: korocon.log
>
[job 1] 実行中...
[job 1] 完了（トークン数: 1234）
5
>
```

Codexの使用量イベントを受け取った場合、完了メッセージには入力・出力トークン数の合計が表示されます。

ログファイルは`--log-file`で変更できます。デフォルトは`korocon.log`で、既存ファイルには追記します。ファイル権限は所有者のみ読み書き可能です。`--stream-logs=false`の場合も、完了した結果は画面に表示します。

Ctrl+Cを押すとCLIを終了し、常駐Codexプロセスと実行中のターンもキャンセルします。
`exit`と入力してEnterを押した場合も同様に終了します。

`--stream-logs`を指定すると、AI CLIの標準出力と標準エラーを実行中にログファイルへリアルタイム追記します。試験期間中は実装上のデフォルトがONですが、正式仕様ではデフォルトOFFに変更予定です。明示的に停止する場合は`--stream-logs=false`を指定してください。

端末での入力中は、`Shift+Enter`で改行、`Enter`でAIへ送信します。左右矢印で文字位置を移動し、上下矢印で入力行を移動できます。最上段より上へは移動せず、移動先が短い行の場合は行末へ移動します。

SIGINTまたはSIGTERMを受けると、新規入力の受付を停止し、常駐Codexプロセスを終了します。

```sh
go build -o ./korocon ./cmd/korocon
./korocon --dir /path/to/repository
```

対話型CLIの詳細は [対話型CLI運用](daemon.md) を参照してください。
実装設計の詳細は [設計資料](design.md) を参照してください。

実装者のデフォルトProviderは`codex`、Modelは`gpt-5.6-luna`です。検証者とレビューアは、個別指定がなければ実装者と同じProvider・Modelを使用します。既存の`--provider`と`--model`は実装者設定を変更します。CLIの実行ファイル名は`--binary`で変更できます。コマンドはシェルを経由せず、引数を分離したまま起動します。

Codexは`app-server --stdio`で起動し、threadには`workspace-write` sandboxと`on-request`承認ポリシーを設定します。危険な全自動実行フラグは使用しません。Copilotは役割別Providerへ`copilot`を指定します。
