# korocon 設計資料

## 1. 目的

koroconは、Codex CLIまたはGitHub Copilot CLIをバックグラウンドで常駐させ、同じ会話セッションへ端末から継続的に指示を送る対話型CLIです。korocon自身は端末上でフォアグラウンド動作します。

## 2. 設計方針

- korocon起動時に選択Providerのプロセスを1回だけ起動する
- Codex app-serverまたはCopilot ACPと標準入力・標準出力のJSONLプロトコルで通信する
- 入力はキューへ積み、同じthreadで1ターンずつ入力順に実行する
- 各ジョブの開始前に対象リポジトリをfetch・fast-forward pullする
- シェルを経由せず、`exec.CommandContext`で直接プロセスを起動する
- 定義済みの安全なコマンドだけを自動承認し、それ以外の承認要求を利用者へ返す
- Ctrl+CまたはSIGTERMでAIプロセスも停止する
- バイナリと同じディレクトリの`config.json`から設定を読む
- 設計・実装成果物をkorobokcle互換のworkspace構造へ保存する
- 実装は専用worktree上で行い、実装用と検証用のCodexセッションを分離する
- 実装と検証を設定回数まで反復する

## 3. 対象範囲

### 対応する機能

- Codex app-serverとCopilot ACPサーバーの常駐実行
- 同じ会話セッションへの複数ターン送信
- 入力受付開始後のCopilot初回ターン直前に`/ide`を実行
- モデル、作業ディレクトリ、sandboxの指定
- JSONイベントと標準エラーのログ記録
- AIの最終回答と、Providerが提供する場合のトークン数表示
- 許可コマンドの自動承認と、それ以外の操作承認・拒否
- SIGINT/SIGTERMによる停止

### 対象外

- AI CLI自体の認証や設定管理
- HTTP API、データベース、永続キュー
- CLI再起動後のthread復元
- 同一thread内のターン並列実行
- ACP以外の追加Providerプロトコル

## 4. 構成

```text
                         user input
                             |
                             v
+-------------------+  +--------------------+
| cmd/korocon       |->| internal/daemon    |
| options / signals |  | line editor        |
+-------------------+  | command / job queue|
                       +----------+---------+
                                  |
                                  | RunTurn
                                  v
                       +--------------------+
                       | internal/runner    |
                       | resident Session   |
                       | JSONL / stdio      |
                       +----------+---------+
                                  |
                                  v
                       codex app-server / copilot --acp --stdio
                                  |
                         one session, many turns
```

対象リポジトリには次の成果物構造を作成します。

```text
<repository>/
  <workspaceName>/
    design/
      <issue番号>_<正規化タイトル>.md
    implementation/
      <issue番号>_<正規化タイトル>.md
      <issue番号>/
        <実装回数>回目_実装.md
        <検討回数>回目_検討.md
```

### `cmd/korocon`

コマンドライン引数、ログファイル、標準入出力を準備し、SIGINT/SIGTERMをContextへ変換します。

`--issue <番号>`または`--pr <番号>`が指定された場合は起動時の選択入力を省略して対象を取得します。取得に失敗した場合はエラー終了せず、理由を表示して通常のIssue/PR選択へフォールバックします。`--implementer`と`--reviewer`は担当者別の選択モードで、同時指定は入力エラーとします。

通常の選択画面は`ISSUE/PR`を大文字で表示し、入力は大文字・小文字を区別しない。`i`/`I`と`p`/`P`を短縮入力として受け付け、未入力EnterはIssueを選択したものとして扱う。PRが存在しない場合、または指定したIssueがopenでない場合は理由を表示してこの選択画面へ戻る。`state:design_ready`または`state:implementation_ready`のIssueを選択した場合は保存済み成果物を表示し、承認または修正指示を受け付ける。対応する成果物がない場合は承認待ちにせず、該当工程を再実行する。

`--implementer`（`-i`）では実装者が担当するIssue一覧と、コンフリクトまたはレビュー指摘修正状態のPR一覧を表示します。`--reviewer`（`-r`）ではIssue/PR種別の選択を省略し、PR自身に`state:*`ステータスがない未レビューPRと`state:review_fixed`の再レビュー対象PRを表示します。Issue側のラベルは判定に使用しません。実装者モードとレビューアモードはCLI引数でのみ指定し、各工程で使用するProviderとModelは既存の役割別設定に従います。

担当者別のIssue/PR一覧は番号の降順で表示され、番号入力を未入力または空白のみで確定すると一覧先頭の対象を選択します。番号を入力した場合は指定番号を選択し、不正な番号や対象なしなどの既存エラー処理を維持します。自動選択の対象は既存フィルタ適用後の一覧です。

`--auto`は担当者モードの対象を連続処理します。各ワークフローが`daemon.ErrRestart`で終了するたびにGitHub一覧とProject情報を再取得し、番号降順の先頭を選択します。実装者モードはIssue候補を先に評価し、Issueが0件の場合のみPR候補を評価します。レビューアモードはPR候補だけを評価します。候補が0件の場合は「Enterで再取得、`autoPollingInterval`後に再取得します」と表示し、Enterまたは`autoPollingInterval`の満了までcontext対応で待機してGitHub一覧とProject情報を再取得します。既定値は`5m`で、待機中のCtrl+Cは正常終了として扱います。承認待ち状態は自動承認せず、各ワークフローの入力処理を維持します。

`--assignee <ユーザー名>`はIssueとPRの担当者フィルタです。省略時は`gh api user --jq .login`のログインユーザーを使用し、`--assignee ""`のように空白を明示した場合はフィルタしません。直接指定した`--issue`または`--pr`も同じフィルタで検査します。

追加フィルタは`--label`、`--exclude-label`、`--title`、`--author`、`--search`で指定します。構造化フィルタは取得結果へ適用し、`--search`はIssue/PR一覧取得時のGitHub検索式として使用します。複数条件のうち、ラベル包含はAND、タイトルと作成者の複数指定はOR、異なる種類の条件同士はANDで評価します。

GitHub Projects v2のフィルタは`--project <番号>`、`--project-owner <owner>`、`--project-status <Status>`、`--project-query <検索式>`で指定します。`--project-status`は`status:"<Status>"`へ変換し、`--project-query`があればAND条件として連結します。選択のたびに`gh project item-list <番号> --owner <owner> --query <検索式> --format json`を実行し、返されたIssue/PRのURLと通常一覧を交差させます。これにより、Statusは専用引数、Priorityなどの任意フィールドはProjects検索構文で絞り込めます。

### `internal/config`

`os.Executable`から実行バイナリのディレクトリを求め、同じディレクトリの`config.json`を読みます。ファイルがない場合は`workspaceName: .workspace`を使用します。不正なJSON、未知の設定項目、パスとして解釈できる`workspaceName`は起動エラーです。

`korocon config init`は`baseBranch`、`branchNamePattern`、`startupCommand`を対話入力し、空入力を既定値として扱います。続けて共通のモデル設定処理を呼び出し、実装者・検証者・レビューアのProviderとModelを設定してから一度だけ保存します。モデル候補は各役割で選択されたProviderから解決し、Copilotは`auto`、`gpt-5.6-sol`、`gpt-5.6-terra`、`gpt-5.6-luna`、`gpt-5-mini`、`cloade-sonnet-4.6`、`claude-opus-4.6`を表示します。Copilotの既定値は`auto`で、具体的なモデル名は直接入力も許可します。ProviderをCodexからCopilotへ変更した場合、空入力時のModel既定値は`auto`です。既存ファイルは`--force`なしでは上書きしません。`korocon config model`は同じモデル設定処理を既存設定へ適用します。

`korocon config allow [COMMAND]`は既存の自動承認コマンド正規化処理を使って`builtinAllowedCommands`へ追加し、重複時は設定ファイルを書き換えません。引数がない場合は標準入力からコマンドを対話取得します。

`korocon config allow-path [GLOB]`は`builtinAllowedPaths`へCopilotの自動承認パスを追加します。既定値は`~/.copilot/session-state/*/plan.md`です。

実装設定として、ブランチ名規則、worktree親ディレクトリ、実装・検証ループ回数、自動処理の再取得間隔、PRのbaseブランチ、動作確認用の`startupCommand`も保持します。`builtinAllowedCommands`はコマンド許可、`builtinAllowedPaths`はCopilotのパス・diff許可として独立して保持します。

AI設定は実装者、検証者、レビューアごとにProviderとModelを保持します。検証者・レビューアの各未指定値は実装者の解決済み設定を継承し、CLI引数、設定ファイル、既定値の順で解決します。Issue設計・実装、PRコンフリクト解消、レビュー指摘修正は実装者、Issue実装とレビュー指摘修正の検証は検証者、PRレビューはレビューアを使用します。

役割ごとの担当工程は次のとおりです。

| 対象 | 工程 | 担当 |
| --- | --- | --- |
| Issue | 設計・実装 | 実装者 |
| Issue | 実装結果の検証 | 検証者 |
| PR | レビュー・動作確認 | レビューア |
| PR | レビュー指摘修正 | 実装者 |
| PR | レビュー指摘修正の検証 | 検証者 |
| PR | コンフリクト解消 | 実装者 |

異なる役割の工程は同じAIプロセス内で続けて実行しません。PRレビュー結果の`## 結果`が`要修正`または`コメントあり`の場合も、レビュー結果を表示して利用者の承認待ちにします。承認入力ではレビューOKとして動作確認または終了へ進み、指摘内容の入力ではレビュー結果と指示をPRコメントへ登録して`state:pr_review_comment`へ更新し、レビューアを停止して最初のIssue/PR選択へ戻ります。`/rerun`ではレビューを再実行します。次に同じPRを選択した時点で実装者による修正を開始します。レビュー指摘修正を承認した場合も実装者を停止して選択へ戻り、次回選択時にレビューアで再レビューします。コンフリクト解消の承認後も同様に選択へ戻ります。これにより、各工程では設定された担当者のProvider、Model、独立したAIセッションだけを使用します。

### `internal/daemon`

端末入力、`/model`などの内部コマンド、ターンキュー、結果表示を管理します。入力は受け付け時にジョブIDを採番し、単一workerが順番に処理します。

ジョブ共通の`BeforeJob`フックをAI投入とIssue状態更新より先に呼び出します。リポジトリ同期に失敗した場合はジョブを失敗表示し、後続処理を開始しません。

AIからコマンド実行の承認要求を受信すると許可リストを判定し、安全な一致なら即座に承認します。それ以外は内容を画面へ表示し、未入力Enter、`/approve`、`/allow`、`/decline`の入力を待ってProviderへ応答します。CopilotのACP `session/request_permission`はこの共通形式へ変換します。

### `internal/runner`

`Session`がCodex app-server、`CopilotSession`がCopilot ACPプロセスとのJSONL通信を所有します。`AgentSession`は役割ごとのProvider差を吸収し、どちらもプロセスと会話を複数ターンで再利用します。Copilotは`initialize`、`session/new`を起動時に完了させ、入力ループ開始後の初回ターン直前に`/ide`を`session/prompt`として送ります。これにより、`/ide`が承認を要求しても端末入力で応答できます。

### `internal/issue`

選択したIssueをGitHub CLIからJSONで取得し、状態ラベルから設計・実装を判定します。Codexへ渡すIssueコンテキストの生成と、ジョブ開始・完了時の状態ラベル同期を担当します。具体的な設計・実装手順は扱いません。

### `internal/implementation`

実装worktreeの作成、実装用・検証用Codexセッション、反復制御、検証JSONの判定を担当します。実装用threadは`workspace-write`、検証用threadは`read-only`で、どちらも承認ポリシーは`on-request`です。

実装用または検証用Codexから応答を受信するたび、JSON解析や次工程へ進む前に回数付きMarkdown成果物を保存します。保存失敗はその実装ジョブの失敗として扱います。

実装承認時はworktreeの変更をstage・commitし、リモート同名ブランチが存在する場合は`pull --rebase`してからpushします。既存PRがなければ実装成果物と`Closes #<Issue番号>`を本文にして`gh pr create`を実行します。新規PRのassigneeは現在のGitHubユーザー（`@me`）とし、configの`reviewer`が設定されている場合はそのGitHubユーザーをreviewerに指定します。

フェーズ変更時はdaemonへ`実装N回目`または`検証N回目`を通知します。daemonはフェーズ開始時に表示ジョブ番号を採番し、`[job N] 実行中(<フェーズ>)...`を描画します。フェーズ変更前の現在行は`[job N] 完了(<フェーズ>)`として改行確定し、履歴として残します。同じフェーズ内の進捗更新では現在行だけを上書きします。

## 5. 実行フロー

1. CLIがオプションとログファイルを準備する
2. 選択Providerのapp-serverまたはACPサーバーを起動する
3. ProviderのJSONプロトコルで`initialize`を送信する
4. Codexは`thread/start`、Copilotは`session/new`を実行する
5. Issueが選択されていれば、状態を判定して初期ジョブをキューへ追加する
6. ジョブ開始前に`git fetch --prune origin`と`git pull --ff-only`を実行する
7. GitHubの状態ラベルをrunningへ更新する
8. 状態ラベル更新に成功したジョブについて、Issue番号と工程の開始メッセージおよび`---`を表示する
9. workerがProviderのターン開始要求を標準入力へ送り、Copilotの初回ターンでは先に`/ide`を送る
10. JSONL通知をログへ記録し、ターン完了応答まで待つ
11. 最終`agentMessage`と使用トークン数を画面へ表示する
12. 成果物を`<workspaceName>/<工程>/<番号>_<タイトル>.md`へ保存する
13. 正常完了時はready、失敗時はfailedへIssueラベルを更新する
14. Issue処理の正常完了後は保存先、承認または修正指示の案内と`> `を表示する
15. 設計承認時は設計用Codexを停止し、実装worktreeを準備する
16. 実装用・検証用Codexを起動し、各応答を回数付き成果物へ保存しながら設定回数まで反復する
17. 検証合格時は実装成果物を保存して承認待ちへ進む
18. 実装承認時は変更をcommit・rebase・pushしてPRを作成する
19. PR作成成功後にIssueを`state:pr_created`へ更新し、2つのCodexを停止する
20. PR URLを表示した後、最初の`issue`/`pr`選択へ戻る
21. 修正指示なら同じ工程の再実行をキューへ追加する
22. 終了時にContextをキャンセルし、すべてのCodexプロセスを停止する

### PR処理

1. `gh pr list --state all --json`でPRを取得し、MERGEDまたはDraftを除外して番号、工程ステータス、タイトルを表示する
2. `mergeable=CONFLICTING`または`mergeStateStatus=DIRTY`はラベル由来の未レビュー・レビュー修正状態より優先してコンフリクトと判定する
3. コンフリクトPRはhead worktreeへbaseをmergeし、実装者AIと`resolve-pr-conflicts`スキルで解消して`pr_conflict/`へ成果物を保存する
4. コンフリクト解消の承認時は未解消ファイルを検査し、merge commitをPR headへpushして`state:pr_conflict_resolved`へ更新する
5. 通常PRはレビューアと`review-pull-request`スキルを使ってレビューし、`review/`へ成果物を保存する
6. レビュー承認時はレビューアの工程として`startupCommand`があればPR headのworktreeで自動起動して`state:review_approved`の動作確認へ進み、未設定なら「動作確認後にPRをマージしてください。」という案内とPR URLを表示してPR処理を終了し、最初の選択へ戻る。`/rerun`は同じレビューを再実行する
7. レビュー結果が`要修正`または`コメントあり`でも承認待ちにし、承認入力ならレビューOKとして次工程へ進める。指摘内容の入力なら結果と指示をPRへコメントして`state:pr_review_comment`へ更新し、レビューアを停止して最初の選択へ戻す。`/rerun`はレビューを再実行する
8. `state:review_approved`のPRを選択した場合はレビュー指摘承認済みと表示して入力を待つ。未入力Enterなら最初の選択へ戻り、文字入力ならその内容を補足としてレビューアで再レビューする
9. `state:pr_review_comment`などレビュー修正状態のPRを選択すると、一般コメント、レビュー本文、行単位レビューコメントをすべて取得して`review_fix/<PR番号>_<正規化タイトル>_レビュー指摘.md`へ保存・表示し、利用者の修正方針を待つ。未入力Enterは保存済み内容をそのまま修正対象とし、文字入力は追加の修正方針として扱う
10. 修正方針の入力後、実装者と検証者で実装・検証を最大`implementationLoopCount`回繰り返し、各結果を`review_fix/<PR番号>/<回数>回目_実装.md`および`<回数>回目_検証.md`へ保存する
11. 検証合格後の修正結果を`review_fix_implementation/`へ保存し、承認後にcommit・PR headへpushして`state:review_fixed`へ更新する。再レビューは続けず両セッションを停止して最初の選択へ戻る
12. `state:review_fixed`のPRを再選択すると、レビューアで再レビューする
13. 動作確認後、PRがCLOSEDまたはMERGEDなら`state:completed`へ更新して最初の選択へ戻る

## 6. 入出力仕様

端末がTTYの場合はLinuxのraw mode入力エディタを使用します。`Shift+Enter`は入力中の改行、`Enter`は送信です。TTYでない標準入力は行単位のScannerへフォールバックします。

入力の先頭が`/`の場合はkorocon内部コマンドです。それ以外はCodexの`turn/start.params.input`へテキストとして格納されます。

Issueのレビュー待ちでは入力解釈を優先します。空行、`承認`、`approve`、`a`などは承認、それ以外は修正フィードバックです。フィードバックから再設計または再実装用のプロンプトを生成し、Issue情報とともにCodexへ送ります。

- 空行は無視
- 非TTY入力の1行は最大4 MiB
- ターンは入力順に実行
- 同じthreadを利用するため、それ以前の会話コンテキストを継続

AIの標準出力はプロトコル用なので、そのまま画面へは表示しません。JSONLはログへ保存し、画面にはジョブ状態、承認要求、最終回答だけを表示します。

## 7. モデル切替

`/model`はProvider別の利用可能モデルを表示します。Codexには`thread/settings/update`、CopilotにはACPの`session/prompt`で`/model <モデル名>`を送ります。成功応答を返した場合だけkorocon側の現在モデルを更新し、プロセスや会話セッションは再起動しません。

通常ターンではモデルを毎回上書きせず、常駐threadの現在設定を使用します。このため、モデル切替後に実行されるキュー済みジョブにも新しいモデルが適用されます。切替時点ですでに実行中のターンのモデルは変更されません。

## 8. 承認とsandbox

app-server起動時に`sandbox_workspace_write.network_access=true`を明示し、threadは`sandbox: workspace-write`、`approvalPolicy: on-request`で開始します。これによりファイル・コマンドのsandboxと承認制御を維持したまま、GitHub APIなどへのネットワーク接続を許可します。`--dangerously-bypass-approvals-and-sandbox`は使用しません。

Codexの`item/commandExecution/requestApproval`とCopilot ACPの`session/request_permission`を共通承認フローへ接続します。完全一致、安全な引数付き実行、LinuxまたはPowerShellの安全な先頭環境変数代入付き実行のみ自動承認し、シェルの連結、パイプ、リダイレクト、コマンド置換を含む実行は除外します。

Copilot要求の`rawInput`に`path`または`fileName`がある場合は`builtinAllowedPaths`のglobと照合します。`diff`は`diff --git`ヘッダーの全変更対象が許可パスに一致した場合だけ自動承認し、許可対象と対象外が混在するdiffは手動承認へ送ります。

許可リストに一致しない`requestApproval`はターン処理を待機させます。未入力Enterまたは`/approve`は`accept`、`/decline`は`decline`をJSON応答として返します。`/allow`は`commandActions`、ポリシー候補、要求コマンドの順で追加対象を抽出し、安全な先頭環境変数代入を除去して`config.json`へ保存します。保存成功後に実行中の許可リストへ反映して`accept`を返し、保存失敗時は承認待ちを維持します。未対応形式のサーバー要求は自動承認せず、エラー応答します。

## 9. 並行性と終了処理

端末入力とAIターン処理は別goroutineです。ターンキューの容量は128件で、単一workerが入力順に処理します。同じ会話へ複数のアクティブターンは作りません。

入力EOFではキューを閉じ、投入済みターンの完了後に終了します。Ctrl+CまたはSIGTERMではContextをキャンセルし、実行中のターンとCodex子プロセスを停止します。

## 10. エラー処理

- Codex未検出・起動失敗: 入力受付前にCLIエラーとして終了
- initialize/thread開始失敗: Codexを停止してCLIエラーとして終了
- ターン失敗: `[job ID] error: ...`と失敗状態を表示
- Codexプロセスの異常終了: 実行中ターンを失敗として処理
- 作業ディレクトリ不正: Codex起動前にエラー
- 未対応承認要求: 自動承認せずJSON-RPCエラーを返却
- 許可外の承認要求: 手動承認入力を待機
- PR公開失敗: `state:implementation_ready`と実装Codexを維持し、再承認可能なエラーとして表示

## 11. テスト方針

- `runner.Session`: 1プロセス・1threadで複数ターンを処理することを検証
- `runner.copilotACPArgs`: ACP常駐起動引数とモデル指定を検証
- `runner.CopilotSession`: 1プロセス再利用、入力受付前にプロンプトを送らないこと、初回ターンの`/ide`初期化、モデル変更、承認変換を偽ACPサーバーで検証
- `daemon.Run`: 入力、モデル切替、内部コマンド、結果表示を検証
- `go test -race ./...`: キュー、応答待ち、共有状態の競合を検査
- `GOOS=linux GOARCH=amd64 go build ./cmd/korocon`: Linuxバイナリを検証
