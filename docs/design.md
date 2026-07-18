# korocon 設計資料

## 1. 目的

koroconは、Codex CLIをバックグラウンドで常駐させ、同じ会話セッションへ端末から継続的に指示を送る対話型CLIです。korocon自身は端末上でフォアグラウンド動作します。

## 2. 設計方針

- korocon起動時にCodexプロセスを1回だけ起動する
- Codexとは標準入力・標準出力のJSONLプロトコルで通信する
- 入力はキューへ積み、同じthreadで1ターンずつ入力順に実行する
- 各ジョブの開始前に対象リポジトリをfetch・fast-forward pullする
- シェルを経由せず、`exec.CommandContext`で直接プロセスを起動する
- 定義済みの安全なコマンドだけを自動承認し、それ以外の承認要求を利用者へ返す
- Ctrl+CまたはSIGTERMでCodexプロセスも停止する
- バイナリと同じディレクトリの`config.json`から設定を読む
- 設計・実装成果物をkorobokcle互換のworkspace構造へ保存する
- 実装は専用worktree上で行い、実装用と検証用のCodexセッションを分離する
- 実装と検証を設定回数まで反復する

## 3. 対象範囲

### 対応する機能

- `codex app-server --stdio`の常駐実行
- 同じCodex threadへの複数ターン送信
- モデル、作業ディレクトリ、sandboxの指定
- JSONイベントと標準エラーのログ記録
- Codexの最終回答とトークン数の表示
- 許可コマンドの自動承認と、それ以外の操作承認・拒否
- SIGINT/SIGTERMによる停止

### 対象外

- AI CLI自体の認証や設定管理
- HTTP API、データベース、永続キュー
- CLI再起動後のthread復元
- 同一thread内のターン並列実行
- Codex以外のproviderに共通する常駐プロトコル

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
                       codex app-server --stdio
                                  |
                         one thread, many turns
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

`--issue <番号>`または`--pr <番号>`が指定された場合は起動時の選択入力を省略して対象を取得します。取得に失敗した場合はエラー終了せず、理由を表示して通常のIssue/PR選択へフォールバックします。両方の同時指定は入力エラーとします。

### `internal/config`

`os.Executable`から実行バイナリのディレクトリを求め、同じディレクトリの`config.json`を読みます。ファイルがない場合は`workspaceName: .workspace`を使用します。不正なJSON、未知の設定項目、パスとして解釈できる`workspaceName`は起動エラーです。

実装設定として、ブランチ名規則、worktree親ディレクトリ、実装・検証ループ回数、PRのbaseブランチ、動作確認用の`startupCommand`も保持します。既定値は`issue_#<issue番号>`、`../<リポジトリ名>-branches/`、3回、`main`、動作確認コマンド未設定です。worktree親ディレクトリの`<リポジトリ名>`または`<repositoryName>`は実行時に置換します。`builtinAllowedCommands`は自動承認するコマンドを保持し、省略または空の場合はkorobokcleと同じ既定リストを補完します。

AI設定は実装者、検証者、レビューアごとにProviderとModelを保持します。検証者・レビューアの各未指定値は実装者の解決済み設定を継承し、CLI引数、設定ファイル、既定値の順で解決します。Issue設計・実装、PRコンフリクト解消、レビュー指摘修正は実装者、実装検証は検証者、PRレビューはレビューアを使用します。

役割ごとの担当工程は次のとおりです。

| 対象 | 工程 | 担当 |
| --- | --- | --- |
| Issue | 設計・実装 | 実装者 |
| Issue | 実装結果の検証 | 検証者 |
| PR | レビュー・動作確認 | レビューア |
| PR | レビュー指摘修正 | 実装者 |
| PR | コンフリクト解消 | 実装者 |

異なる役割の工程は同じAIプロセス内で続けて実行しません。PRレビュー結果の`## 結果`が`要修正`または`コメントあり`の場合は、レビュー結果をPRコメントへ登録して`state:pr_review_comment`へ更新し、入力待ちを挟まずレビューアを停止して最初のIssue/PR選択へ戻ります。形式不明の結果に対して利用者が修正指示を入力した場合も同様です。次に同じPRを選択した時点で実装者による修正を開始します。レビュー指摘修正を承認した場合も実装者を停止して選択へ戻り、次回選択時にレビューアで再レビューします。コンフリクト解消の承認後も同様に選択へ戻ります。これにより、各工程では設定された担当者のProvider、Model、独立したAIセッションだけを使用します。

### `internal/daemon`

端末入力、`/model`などの内部コマンド、ターンキュー、結果表示を管理します。入力は受け付け時にジョブIDを採番し、Codex用の単一workerが順番に処理します。

ジョブ共通の`BeforeJob`フックをAI投入とIssue状態更新より先に呼び出します。リポジトリ同期に失敗した場合はジョブを失敗表示し、後続処理を開始しません。

Codexからコマンド実行の承認要求を受信すると許可リストを判定し、安全な一致なら即座に承認します。それ以外は内容を画面へ表示し、未入力Enter、`/approve`、`/allow`、`/decline`の入力を待ってapp-serverへ応答します。`/allow`は要求の具体的なコマンドを実行中の許可リストと`config.json`へ追加してから承認します。

### `internal/runner`

`Session`がCodexプロセスとJSONL通信を所有します。`AgentSession`は役割ごとのProvider差を吸収し、Codexは常駐app-server、常駐プロトコルを持たないProviderはCLIターンとして実行します。

### `internal/issue`

選択したIssueをGitHub CLIからJSONで取得し、GitHubの`state`でOPEN判定を行ったうえで、状態ラベルから設計・実装を判定します。`state:design_ready`/`state:implementation_ready`は承認待ちとして保存済み成果物を読み込み、再開可能な状態を返します。Codexへ渡すIssueコンテキストの生成と、ジョブ開始・完了時の状態ラベル同期を担当します。具体的な設計・実装手順は扱いません。

起動時の選択失敗、PRなし、OPENでないIssueは初期ジョブを投入せず、`cmd/korocon`の選択画面へ戻します。承認待ちの再開では成果物の存在を確認し、新規実行による上書きは行いません。

### `internal/implementation`

実装worktreeの作成、実装用・検証用Codexセッション、反復制御、検証JSONの判定を担当します。実装用threadは`workspace-write`、検証用threadは`read-only`で、どちらも承認ポリシーは`on-request`です。

実装用または検証用Codexから応答を受信するたび、JSON解析や次工程へ進む前に回数付きMarkdown成果物を保存します。保存失敗はその実装ジョブの失敗として扱います。

実装承認時はworktreeの変更をstage・commitし、リモート同名ブランチが存在する場合は`pull --rebase`してからpushします。既存PRがなければ実装成果物と`Closes #<Issue番号>`を本文にして`gh pr create`を実行します。

フェーズ変更時はdaemonへ`実装N回目`または`検証N回目`を通知します。daemonはフェーズ開始時に表示ジョブ番号を採番し、`[job N] 実行中(<フェーズ>)...`を描画します。フェーズ変更前の現在行は`[job N] 完了(<フェーズ>)`として改行確定し、履歴として残します。同じフェーズ内の進捗更新では現在行だけを上書きします。

## 5. 実行フロー

1. CLIがオプションとログファイルを準備する
2. `codex app-server --stdio`を起動する
3. `initialize`を送信し、`initialized`を通知する
4. `thread/start`を送信してthread IDを保持する
5. Issueが選択されていれば、状態を判定して初期ジョブをキューへ追加する
6. ジョブ開始前に`git fetch --prune origin`と`git pull --ff-only`を実行する
7. GitHubの状態ラベルをrunningへ更新する
8. workerが`turn/start`をCodexの標準入力へ送る
9. JSONL通知をログへ記録し、`turn/completed`まで待つ
10. 最終`agentMessage`と使用トークン数を画面へ表示する
11. 成果物を`<workspaceName>/<工程>/<番号>_<タイトル>.md`へ保存する
12. 正常完了時はready、失敗時はfailedへIssueラベルを更新する
13. Issue処理の正常完了後は保存先、承認または修正指示の案内と`> `を表示する
14. 設計承認時は設計用Codexを停止し、実装worktreeを準備する
15. 実装用・検証用Codexを起動し、各応答を回数付き成果物へ保存しながら設定回数まで反復する
16. 検証合格時は実装成果物を保存して承認待ちへ進む
17. 実装承認時は変更をcommit・rebase・pushしてPRを作成する
18. PR作成成功後にIssueを`state:pr_created`へ更新し、2つのCodexを停止する
19. PR URLを表示した後、最初の`issue`/`pr`選択へ戻る
20. 修正指示なら同じ工程の再実行をキューへ追加する
21. 終了時にContextをキャンセルし、すべてのCodexプロセスを停止する

### PR処理

1. `gh pr list --state all --json`でPRを取得し、MERGEDまたはDraftを除外して番号、工程ステータス、タイトルを表示する
2. `mergeable=CONFLICTING`または`mergeStateStatus=DIRTY`はラベル由来の未レビュー・レビュー修正状態より優先してコンフリクトと判定する
3. コンフリクトPRはhead worktreeへbaseをmergeし、実装者AIと`resolve-pr-conflicts`スキルで解消して`pr_conflict/`へ成果物を保存する
4. コンフリクト解消の承認時は未解消ファイルを検査し、merge commitをPR headへpushして`state:pr_conflict_resolved`へ更新する
5. 通常PRはレビューアと`review-pull-request`スキルを使ってレビューし、`review/`へ成果物を保存する
6. レビュー承認時はレビューアの工程として`startupCommand`があればPR headのworktreeで自動起動して`state:review_approved`の動作確認へ進み、未設定ならPR処理を終了して最初の選択へ戻る。`/rerun`は同じレビューを再実行する
7. レビュー結果が`要修正`または`コメントあり`なら結果をPRへコメントして`state:pr_review_comment`へ更新し、修正を続けずレビューアを停止して最初の選択へ戻る。利用者がレビュー修正指示を入力した場合も同様とする
8. `state:pr_review_comment`などレビュー修正状態のPRを選択すると、実装者と`review-comment-fix`スキルで設計検討、実装、テストを行う
9. 修正結果を`review_fix_implementation/`へ保存し、承認後にcommit・PR headへpushして`state:review_fixed`へ更新する。再レビューは続けず実装者を停止して最初の選択へ戻る
10. `state:review_fixed`のPRを再選択すると、レビューアで再レビューする
11. 動作確認後、PRがCLOSEDまたはMERGEDなら`state:completed`へ更新して最初の選択へ戻る

## 6. 入出力仕様

端末がTTYの場合はLinuxのraw mode入力エディタを使用します。`Shift+Enter`は入力中の改行、`Enter`は送信です。TTYでない標準入力は行単位のScannerへフォールバックします。

入力の先頭が`/`の場合はkorocon内部コマンドです。それ以外はCodexの`turn/start.params.input`へテキストとして格納されます。

Issueのレビュー待ちでは入力解釈を優先します。空行、`承認`、`approve`、`a`などは承認、それ以外は修正フィードバックです。フィードバックから再設計または再実装用のプロンプトを生成し、Issue情報とともにCodexへ送ります。

- 空行は無視
- 非TTY入力の1行は最大4 MiB
- ターンは入力順に実行
- 同じthreadを利用するため、それ以前の会話コンテキストを継続

Codexの標準出力はプロトコル用なので、そのまま画面へは表示しません。JSONLはログへ保存し、画面にはジョブ状態、承認要求、最終回答だけを表示します。

## 7. モデル切替

`/model`は利用可能なモデルを表示します。番号またはモデル名を指定すると、Codexの標準入力へTUIの`/model`に相当する`thread/settings/update`要求を送信します。このAPIを利用するため、初期化時に`experimentalApi` capabilityを宣言します。Codexが成功応答を返した場合だけkorocon側の現在モデルを更新します。Codexプロセスやthreadは再起動しません。

通常ターンではモデルを毎回上書きせず、常駐threadの現在設定を使用します。このため、モデル切替後に実行されるキュー済みジョブにも新しいモデルが適用されます。切替時点ですでに実行中のターンのモデルは変更されません。

## 8. 承認とsandbox

threadは`sandbox: workspace-write`、`approvalPolicy: on-request`で開始します。`--dangerously-bypass-approvals-and-sandbox`は使用しません。

Codexが`item/commandExecution/requestApproval`を送信すると、`command`、`proposedExecpolicyAmendment`、`commandActions`を許可リストと照合します。完全一致、安全な引数付き実行、LinuxまたはPowerShellの安全な先頭環境変数代入付き実行のみ`accept`を自動応答し、シェルの連結、パイプ、リダイレクト、コマンド置換を含む実行は除外します。

許可リストに一致しない`requestApproval`はターン処理を待機させます。未入力Enterまたは`/approve`は`accept`、`/decline`は`decline`をJSON応答として返します。`/allow`は`commandActions`、ポリシー候補、要求コマンドの順で追加対象を抽出し、安全な先頭環境変数代入を除去して`config.json`へ保存します。保存成功後に実行中の許可リストへ反映して`accept`を返し、保存失敗時は承認待ちを維持します。未対応形式のサーバー要求は自動承認せず、エラー応答します。

## 9. 並行性と終了処理

端末入力とCodexターン処理は別goroutineです。ターンキューの容量は128件で、単一workerが入力順に処理します。同じthreadへ複数のアクティブターンは作りません。

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
- `runner.BuildArgs`: 常駐プロトコルを持たないproviderの引数を検証
- `daemon.Run`: 入力、モデル切替、内部コマンド、結果表示を検証
- `go test -race ./...`: キュー、応答待ち、共有状態の競合を検査
- `GOOS=linux GOARCH=amd64 go build ./cmd/korocon`: Linuxバイナリを検証
