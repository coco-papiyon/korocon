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

設定ファイルは`korocon config init`で対話作成できます。`baseBranch`、`branchNamePattern`、`startupCommand`の順に入力し、未入力状態でEnterを押すと画面に表示された既定値を使用します。その後、`korocon config model`と同じモデル設定へ進み、実装者・検証者・レビューアのProviderとModelを入力します。Model候補は選択したProviderごとに表示します。Copilotでは`auto`、`gpt-5.6-sol`、`gpt-5.6-terra`、`gpt-5.6-luna`、`gpt-5-mini`、`claude-sonnet-4.6`、`claude-opus-4.6`を選択でき、既定値は`auto`です。検証者とレビューアは`inherit`で実装者と同じ設定にできます。

設定一覧は`korocon config list`で表示できます。

```sh
korocon config init
korocon config init --force  # 既存config.jsonを再初期化
korocon config model         # モデル設定だけを変更
korocon config set autoPollingInterval 10m
korocon config set syncDirtyWorktree stash
korocon config set implementationLoopCount 5
korocon config allow "go test ./..."
korocon config allow-path "~/.copilot/session-state/*/plan.md"
```

`korocon config allow [COMMAND]`は`builtinAllowedCommands`へコマンドを追加します。`korocon config allow-path [GLOB]`は`builtinAllowedPaths`へCopilotの自動承認対象パスを追加します。引数を省略した場合は対話入力になります。

`korocon config set <KEY> <VALUE>`は設定ファイルの単一値を変更します。`autoPollingInterval`は`5m`や`30s`などの正のduration、`implementationLoopCount`は1〜10を指定します。`syncDirtyWorktree`は`fail`（既定）または`stash`を指定できます。`stash`では未コミット変更と未追跡ファイルを一時退避して同期後に復元します。復元が競合した場合はジョブを開始せず、stashを残します。Providerには`codex`、`copilot`、検証者・レビューアには`inherit`を指定できます。配列設定は`config allow`または`config allow-path`を使用してください。

### Issue/PR一覧

IssueとPRの一覧は、AIを起動せずにサブコマンドで表示できます。工程状態は表示せず、GitHubから取得したIssue/PR情報だけを表示します。

```sh
korocon issue list
korocon pr list
korocon issue list --state all --label backend
korocon pr list --search 'is:open' --json
```

旧形式（互換別名）: `korocon list issue` / `korocon list pr`

`--state`は`open`（既定）、`closed`、`all`を指定できます。PRは`--state open`の場合、Draftを除外します。`--label`、`--exclude-label`、`--title`、`--author`は複数指定でき、`--json`を指定するとJSON配列を出力します。

```text
tools/
  korocon
  config.json
```

```json
{
  "workspaceName": ".workspace",
  "branchNamePattern": "issue_#{{ issue_number }}",
  "implementationDirectory": "../branches-{{ repository_name }}/",
  "implementationLoopCount": 3,
  "autoPollingInterval": "5m",
  "syncDirtyWorktree": "fail",
  "baseBranch": "main",
  "runtimeVerificationEnabled": true,
  "implementerProvider": "codex",
  "implementerModel": "gpt-5.6-luna",
  "verifierProvider": "codex",
  "verifierModel": "gpt-5.4-mini",
  "reviewerProvider": "copilot",
  "reviewerModel": "claude-sonnet-4.5",
  "reviewer": "octocat",
  "startupCommand": "go run ./cmd/app",
  "builtinAllowedCommands": ["git add", "git diff", "git status", "go test"],
  "builtinAllowedPaths": ["~/.copilot/session-state/*/plan.md"]
}
```

`workspaceName`は対象リポジトリ直下に作成する成果物ディレクトリの名前です。絶対パス、`..`、パス区切りを含む値は指定できません。

| 設定 | 既定値 | 内容 |
| --- | --- | --- |
| `branchNamePattern` | `issue_#{{ issue_number }}` | 実装worktreeのブランチ名。設定値はテンプレートとして展開されます。 |
| `implementationDirectory` | `../branches-{{ repository_name }}/` | 実装worktreeを置く親ディレクトリ。設定値はテンプレートとして展開されます。 |
| `implementationLoopCount` | `3` | Issue実装およびPRレビュー指摘修正の実装・検証の最大試行回数。最大10回です。 |
| `autoPollingInterval` | `5m` | `--auto`で対象がない場合に再取得するまでの待機期間です。`30s`、`5m`、`1h`などの正の期間を指定します。 |
| `syncDirtyWorktree` | `fail` | ジョブ開始前の同期時に未コミット変更がある場合の扱いです。`fail`は同期を停止し、`stash`は変更を一時stashして同期・復元します。復元競合時はジョブを開始せずstashを残します。 |
| `baseBranch` | `main` | 実装承認時に作成するPRのbaseブランチです。 |
| `runtimeVerificationEnabled` | `true` | レビュー承認後にレビューアへPR head worktreeでの動作確認を指示するか指定します。 |
| `vscodeNotificationEnabled` | `true` | VS Code統合ターミナルで、ジョブ完了・承認待ち・ユーザー入力待ちを通知するか指定します。 |
| `startupCommand` | 未設定 | 動作確認が有効な場合にPR head worktreeで追加起動するコマンドです。標準出力と標準エラーはログファイルへ記録します。 |
| `builtinAllowedCommands` | korobokcleと同じ既定リスト | Codexのコマンド実行要求を自動承認するコマンドです。省略または空配列では既定リストを使用します。 |
| `builtinAllowedPaths` | `~/.copilot/session-state/*/plan.md` | Copilotのパス・diff要求を自動承認するglobです。diffは全変更対象が一致する場合だけ承認します。 |
| `implementerProvider` | `codex` | 設計、実装、レビュー指摘修正を担当するProviderです。 |
| `implementerModel` | `gpt-5.6-luna` | 実装者のModelです。 |
| `verifierProvider` | 実装者と同じ | Issue実装とPRレビュー指摘修正の検証を担当するProviderです。 |
| `verifierModel` | 実装者と同じ | 検証者のModelです。 |
| `reviewerProvider` | 実装者と同じ | PRレビューを担当するProviderです。 |
| `reviewerModel` | 実装者と同じ | レビューアのModelです。 |
| `reviewer` | 未設定 | 新規PRでレビューを依頼するGitHubユーザーです。PRのassigneeは常に現在のGitHubユーザー（`@me`）になります。 |

`branchNamePattern`と`implementationDirectory`では、次の内部変数を使用できます。

- `{{ issue_number }}`: Issue番号（PRのworktreeではPR番号）
- `{{ repository_name }}`: 対象リポジトリ名

例: `issue_#{{ issue_number }}`、`../{{ repository_name }}-branches/`

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

`korocon`は起動時に選択Providerを1回だけ起動してから標準入力を待機します。Codexは`codex --config sandbox_workspace_write.network_access=true app-server --stdio`、Copilotは`copilot --acp --stdio --model <model>`で起動します。Copilotには入力受付開始後の最初のターン直前に`/ide`を送り、以後の指示も同じACPセッションへNDJSONで送ります。各入力を同じ会話へ順番に送り、AIの最終結果を画面に表示します。通常時の空行は送信しませんが、Issueの承認待ちでは空行を承認として扱います。JSONイベントと標準エラーはログファイルへリアルタイム追記します。

CLI自身の論理メッセージは、AI本文と区別するため`---`の後に各行`[システム] `を付けて表示します。`[job N]`や`[承認待ち]`など既に明確な状態表示には二重の接頭辞を付けず、区切り線も連続させません。AI本文は変更せず、入力プロンプトは従来どおり`> `です。

```text
AIの回答
---
[システム] 設計結果を保存しました: .workspace/design/16_title.md
[システム] 設計が完了しました。承認する場合は未入力状態でEnter、もしくは承認、approve、aのいずれかを入力してください。
[システム] 修正する場合は内容を入力してください。AIへ送信して再設計します。
>
```

通常指示、設計、実装、再設計、再実装を含むすべてのAIジョブは、開始前に対象リポジトリで次を実行します。

```sh
git fetch --prune origin
git pull --no-rebase
```

fetchまたはfast-forward pullに失敗した場合は、そのAIジョブを開始せず`[job N] 失敗`を表示します。自動mergeやrebaseは行いません。競合、未追跡ブランチ、認証・ネットワークエラーなどの原因を解消してからジョブを再投入してください。

起動直後に、GitHubから取得する情報として`ISSUE`または`PR`を選択します。入力は大文字・小文字を区別せず、`i`/`I`または`p`/`P`だけでも指定できます。未入力でEnterを押した場合は`ISSUE`として扱います。`ISSUE`の場合は続けてIssue番号を入力します。koroconは`gh issue view --json`で本文・ラベル・コメントなどを取得し、Issueの状態から設計または実装を判定してCodexへ初期ジョブとして投入します。PRが0件の場合、または指定したIssueがopenでない場合は、理由を表示して`ISSUE/PR`選択へ戻ります。`PR`の場合は、番号、ステータス、タイトルを列とする表形式のPR一覧を表示し、`MERGED`またはDraftがtrueのPRは除外します。`state:*`ラベルがある場合は、工程ラベルを日本語ステータスへ変換して表示します。続けてPR番号を入力すると、選択したPRの本文、ブランチ、コメント、レビューを取得してレビューを開始します。事前にGitHub CLI (`gh`) のログインを完了してください。

担当者別に対象を選択する場合は`--implementer`（省略形`-i`）または`--reviewer`（省略形`-r`）を指定します。`--implementer`では実装者が担当するIssue一覧と、コンフリクト・レビュー指摘修正など実装者が担当するPR一覧を表示します。`--reviewer`ではIssue/PRの種別選択を省略し、PR自身に`state:*`ステータスが付いていない未レビューPRと`state:review_fixed`の再レビュー対象PRを表示します。PR自身にIssue側のラベルは反映されないため、Issueラベルは判定に使用しません。両方のオプションは同時指定できません。

担当者別のIssue/PR一覧で番号を未入力または空白のみでEnterすると、番号降順で並んだ一覧の先頭を選択します。番号を入力した場合は指定した対象を選択します。フィルタを指定している場合は、フィルタ適用後の一覧が対象です。

`--auto`を指定すると、フィルタ結果を番号の降順で再取得しながら先頭から順に処理します。処理完了後も選択入力へ戻らず、次の対象を自動選択します。実装者モードではIssueを優先し、対象Issueがなくなった後にPRを処理します。レビューアモードではPRだけを処理します。対象がない場合は「Enterで再取得、`autoPollingInterval`だけ待機して再取得します」と表示し、Enterで即時に、またはタイマー満了時にGitHub一覧とProject情報を再取得します。待機中のCtrl+Cは直ちにCLIを終了します。設計・実装・レビュー結果などの承認入力は自動化せず、従来どおり利用者の入力を待ちます。

```sh
korocon -i --auto
korocon -r --auto --project 3 --project-status "Ready"
```

`--auto`は`--implementer`または`--reviewer`が必要で、`--issue`、`--pr`とは同時指定できません。

`--assignee <ユーザー名>`でIssueとPRの担当者を指定できます。省略時は`gh api user --jq .login`で取得した現在のGitHubユーザーを使用します。`--assignee ""`のように空白を指定した場合は担当者フィルタを無効にします。

次の追加フィルタを指定できます。

| 引数 | 処理 |
| --- | --- |
| `--label <名前>` | 指定ラベルをすべて持つ対象だけを表示します。複数回指定できます。 |
| `--exclude-label <名前>` | 指定ラベルを持つ対象を除外します。複数回指定できます。 |
| `--title <文字列>` | タイトルの部分一致で絞り込みます。複数指定時はいずれかに一致する対象を表示します。 |
| `--author <ユーザー>` | 作成者で絞り込みます。複数指定時はいずれかに一致する対象を表示します。 |
| `--search <検索式>` | `gh issue list`と`gh pr list`の`--search`へGitHub検索式を渡します。 |
| `--project <番号>` | GitHub Projects v2のProject番号を指定します。 |
| `--project-owner <owner>` | ProjectのユーザーまたはOrganizationを指定します。既定値は`@me`です。 |
| `--project-status <Status>` | ProjectのStatusで絞り込みます。`--project`が必要です。 |
| `--project-query <検索式>` | `gh project item-list --query`へProjects検索式を渡します。`--project`が必要です。 |

ProjectsのStatusは`--project-status`で指定します。Priorityなどのカスタムフィールドは`--project-query`で指定し、両方を指定した場合はANDで評価します。Projectから得たIssue/PRと通常の担当者・役割・ラベル条件の積集合を一覧へ表示します。

```sh
korocon -r --label backend --exclude-label blocked --author coco-papiyon
korocon -i --project 3 --project-owner coco-papiyon --project-status "In Progress"
korocon -i --project 3 --project-status "In Progress" --project-query 'priority:P1'
```

標準入力から実行する場合も、最初の行に`ISSUE`または`PR`を指定します。入力は大文字・小文字を区別せず、空行は`ISSUE`として扱います。

Issue番号またはPR番号が分かっている場合は、起動引数で最初の選択を省略できます。

```sh
korocon --issue 42
korocon --pr 4
korocon --implementer
korocon --reviewer
korocon -i
korocon -r
```

`--issue`と`--pr`は同時指定できません。指定されたIssueまたはPRを取得できない場合は理由と`通常の選択へ戻ります。`を表示し、`取得する情報を選択してください (ISSUE/PR):`から通常の選択を受け付けます。直接指定は起動直後の1回だけ使用し、ジョブ完了後は通常の選択へ戻ります。

### PRレビュー

PRの`mergeable`が`CONFLICTING`、または`mergeStateStatus`が`DIRTY`の場合は、未レビューやレビュー修正状態よりコンフリクト判定を優先します。一覧のステータスには`コンフリクト`と表示し、選択するとレビューではなくコンフリクト解消を開始します。

コンフリクト解消では実装者のProviderとModelを使用し、PR head用worktreeでbaseブランチのmergeを開始してから`resolve-pr-conflicts`スキルを実行します。競合ファイル、head/baseブランチ、双方に対応するIssueの意図を確認し、結果を`<workspaceName>/pr_conflict/<PR番号>_<正規化タイトル>.md`へ保存します。状態は`state:pr_conflict_running`、`state:pr_conflict_ready`、承認後の`state:pr_conflict_resolved`の順に遷移します。承認時に未解消ファイルと競合マーカーを検査し、merge commitをPR headへpushして最初のIssue/PR選択へ戻ります。

PRレビューは`../branches-<リポジトリ名>/<リポジトリ名>-pr-<PR番号>`のPR head用worktreeを作成または再利用し、リモートから最新ソースを取得してから、そのworktreeを作業ディレクトリとしてレビューアを起動します。リポジトリの`review-pull-request`スキルに従い、結果を`<workspaceName>/review/<PR番号>_<正規化タイトル>.md`へ保存します。実行中は`state:review_running`、確認待ちは`state:review_ready`です。worktreeに未コミット変更がある場合は、リモートのPR headと異なる内容をレビューしないように開始を中止します。

レビュー結果の`## 結果`が`要修正`または`コメントあり`の場合も、レビュー結果を表示して承認待ちにします。承認するとレビューOKとして動作確認または終了へ進み、指摘内容を入力してEnterするとレビュー結果と指示をPRへ登録して`state:pr_review_comment`へ更新し、最初のIssue/PR選択へ戻ります。`/rerun`でレビューを再実行できます。

レビュー結果の確認待ちでは次の入力を使用します。

| 入力 | 処理 |
| --- | --- |
| 未入力Enter、`承認`、`approve`、`a` | レビューを承認し、動作確認へ進む |
| `/rerun`、`/rerun <補足>` | 同じPRのレビューを再実行する |
| `/fix <指示>`または任意の文字列 | レビュー修正指示をPRへ登録し、レビューを終了してIssue/PR選択へ戻る |

`state:review_approved`（レビュー指摘承認済み）のPRを指定した場合は、承認済みであることを表示して入力を待ちます。未入力状態でEnterを押すとIssue/PR選択へ戻り、文字を入力してEnterを押すと、その入力を補足としてレビューアによる再レビューを実行します。

AIが指摘を出した場合、または利用者がレビュー修正指示を入力した場合は、PRコメントへ投稿して`state:pr_review_comment`へ更新します。この時点では修正を開始せず、レビューアを停止して最初のIssue/PR選択へ戻ります。同じPRを再選択すると、PRの一般コメント、レビュー本文、行単位レビューコメントをページング取得し、`<workspaceName>/review_fix/<PR番号>_<正規化タイトル>_レビュー指摘.md`へ保存して画面に表示します。未入力状態でEnterを押すと保存済みのレビュー指摘をそのまま修正対象とし、文字を入力した場合は「指摘Aを修正、指摘Bは修正不要」のような修正方針として追加します。

修正方針の入力後、実装者と検証者を起動し、`../branches-<リポジトリ名>/<リポジトリ名>-pr-<PR番号>`のPR head worktreeで実装と独立検証を`implementationLoopCount`回まで繰り返します。既定値は3回です。各結果は`<workspaceName>/review_fix/<PR番号>/<回数>回目_実装.md`と`<回数>回目_検証.md`へ保存します。検証合格後の最終結果は`<workspaceName>/review_fix_implementation/<PR番号>_<正規化タイトル>.md`へ保存して承認を待ちます。承認すると変更をcommitしてPR headへpushし、`state:review_fixed`へ更新して両セッションを停止し、最初の選択へ戻ります。再レビューは次に同じPRを選択したとき、レビューアの新しいセッションで実行します。

Issueの設計・実装、PRレビュー指摘修正、PRコンフリクト解消は実装者を使用します。Issue実装とPRレビュー指摘修正の検証は検証者、PRレビューと動作確認はレビューアを使用します。担当が変わる工程は同じAIプロセスで連続実行しません。

レビュー承認後、`runtimeVerificationEnabled`が`true`ならPR head用worktreeを再度最新化し、レビューアのCodexまたはCopilotへ同worktreeでの動作確認を指示します。`startupCommand`が設定されていれば同じworktreeでコマンドも自動起動します。動作確認を完了してPRをクローズまたはマージした後、未入力Enterまたは`/check`を入力します。PRがCLOSEDまたはMERGEDならコマンドを停止して`state:completed`へ更新し、最初の`issue`/`pr`選択へ戻ります。OPENの場合は動作確認待ちを継続します。`runtimeVerificationEnabled`が`false`なら動作確認を省略してPR処理を終了します。

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

設計承認後は実装ジョブを自動投入します。対象リポジトリが`/home/user/project`、Issue番号が`42`の場合、既定のworktreeは`/home/user/branches-project/project-42`です。

```text
git -C <repository> worktree add -B <branchName> ../branches-<repositoryName>/<repositoryName>-<issueNumber> HEAD
```

worktreeパスがすでに存在する場合は作成コマンドを実行せず、そのディレクトリを利用します。

設計用AIは設計承認まで常駐し、実装開始時に停止します。その後、同じworktreeを作業ディレクトリとする実装用・検証用の2つの常駐AIセッションを起動します。

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

起動時点ですでに`state:design_ready`または`state:implementation_ready`のIssueを選択した場合は、保存済みの設計・実装結果を表示して承認待ちにします。成果物がない場合は承認待ちにせず、該当工程を再実行します。未入力Enterなどで承認すると次の工程へ進み、文字を入力してEnterすると修正指示として同じ工程を再実行します。`state:implementation_approved`または`state:pr_created`のIssueは処理済みとして実行しません。

Codexへ渡す内容は「設計または実装を行う」という工程指示と、Issue番号・タイトル・URL・作成者・本文・ラベル・コメントです。具体的な手順と成果物形式は対象リポジトリのスキルに委ねます。ラベル操作などのワークフロー制御はAIプロンプトへ含めず、korocon自身が実行します。

起動時は入力欄の上に主要設定をAI・GitHub・Workflowのグループに分けて表示し、その下に入力待ちの`> `が表示されます。AI設定は`Provider / Model / 実行バイナリ`形式で、実装者と同じ設定の検証者・レビューアは省略されます。設定ファイル、workspace名、実装ディレクトリ、ループ回数、自動承認コマンド数、ログファイルなどの詳細設定は起動時には表示しません。

入力の先頭が `/` の行はコマンドとして扱われます。`/model` で選択可能なモデルを表示し、番号またはモデル名を指定して切り替えます。Codexには`thread/settings/update`、CopilotにはACPの`session/prompt`で`/model <モデル名>`を送り、成功応答後に表示中のモデルを更新します。プロセスと会話セッションは再起動しません。

CodexまたはCopilotがコマンド実行を要求した場合、`builtinAllowedCommands`に一致するコマンドは自動承認し、`[自動承認]`と対象を表示します。完全一致のほか、安全な引数やLinuxの安全な環境変数代入を付けた実行とCodexが提示する`proposedExecpolicyAmendment`、`commandActions`を判定します。`&&`、`||`、パイプによる複合実行は、引用符を考慮して分割した全コマンドが許可対象の場合だけ承認します。`;`、コマンド置換、任意のリダイレクトを含む実行は自動承認しません。標準エラーの`2>&1`と`2>/dev/null`だけは許可します。

複数行スクリプトや括弧、コマンド置換などを含む文字列を`builtinAllowedCommands`へ明示した場合は、空白を正規化したコマンド全文が一致するときだけ承認します。この完全一致許可は一部一致や引数追加には適用しません。`git worktree remove --force`などの破壊操作を含むスクリプトは、個別コマンドを汎用許可せず全文一致で設定してください。

CopilotのACP承認要求に`path`または`fileName`が含まれる場合は`builtinAllowedPaths`と照合します。`diff`の場合は`diff --git`ヘッダーから変更対象を抽出し、全対象が許可globに一致する場合だけ自動承認します。既定値ではCopilotのセッション状態に作成される`plan.md`だけが対象です。

Issue実装・検証用Copilotセッションでは、`branches-<リポジトリ名>/<リポジトリ名>-<Issue番号>`として実際に作成または再利用したworktree配下を設定なしで自動承認します。通常のCLIセッションやworktree外のパスには適用しません。diffは全変更対象が同じworktree配下の場合だけ承認します。

許可リストに一致しない操作やファイル変更要求は画面へ表示します。未入力状態でEnterまたは`/approve`を入力すると今回だけ承認し、`/allow`は要求内容からアプリが判断したコマンドを承認します。`/allow-job`は現在のジョブ中のすべてのコマンド、`/allow-process`は現在のkoroconプロセス中のすべてのコマンドを一時許可します。`/allow <command>`を入力すると、指定コマンドを恒久的な許可リストとバイナリ横の`config.json`へ追加して承認します。Linuxの先頭環境変数代入は除去して保存するため、`GOCACHE=/tmp/cache go test ./...`は`go test ./...`として追加されます。一時許可は設定ファイルへ保存せず、ジョブまたはプロセスの終了時に破棄します。設定保存に失敗した場合は承認せず、承認待ちを継続します。`--dangerously-bypass-approvals-and-sandbox`は使用しません。

```text
AI:
  implementer     : codex / gpt-5.6-luna / codex
  verifier        : codex / gpt-5.4-mini / codex
  reviewer        : copilot / claude-sonnet-4.5 / copilot

GitHub:
  github reviewer : 未設定

Workflow:
  branch          : issue_#{{ issue_number }}
  base branch     : main
  startup command : 未設定
>
[job 1] 実行中...
[job 1] 完了（トークン数: 1234）
5
>
```

Codexの使用量イベントを受け取った場合、完了メッセージには入力・出力トークン数の合計が表示されます。

ログファイルは`--log-file`で変更できます。デフォルトは`korocon.log`で、既存ファイルには追記します。ファイル権限は所有者のみ読み書き可能です。`--stream-logs=false`の場合も、完了した結果は画面に表示します。

Ctrl+Cを押すとCLIを終了し、常駐AIプロセスと実行中のターンもキャンセルします。
`exit`と入力してEnterを押した場合も同様に終了します。

`--stream-logs`を指定すると、AI CLIの標準出力と標準エラーを実行中にログファイルへリアルタイム追記します。試験期間中は実装上のデフォルトがONですが、正式仕様ではデフォルトOFFに変更予定です。明示的に停止する場合は`--stream-logs=false`を指定してください。

端末での入力中は、`Shift+Enter`で改行、`Enter`でAIへ送信します。左右矢印で文字位置を移動し、上下矢印で入力行を移動できます。最上段より上へは移動せず、移動先が短い行の場合は行末へ移動します。

SIGINTまたはSIGTERMを受けると、新規入力の受付を停止し、常駐AIプロセスを終了します。

```sh
go build -o ./korocon ./cmd/korocon
./korocon --dir /path/to/repository
```

対話型CLIの詳細は [対話型CLI運用](daemon.md) を参照してください。
実装設計の詳細は [設計資料](design.md) を参照してください。

実装者のデフォルトProviderは`codex`、Modelは`gpt-5.6-luna`です。検証者とレビューアは、個別指定がなければ実装者と同じProvider・Modelを使用します。既存の`--provider`と`--model`は実装者設定を変更します。CLIの実行ファイル名は`--binary`で変更できます。コマンドはシェルを経由せず、引数を分離したまま起動します。

Codexは`--config sandbox_workspace_write.network_access=true app-server --stdio`、Copilotは`--acp --stdio --model <model>`で常駐起動します。CopilotのACP `session/request_permission`は既存の自動承認・手動承認へ変換します。危険な全自動実行フラグはどちらにも使用しません。
