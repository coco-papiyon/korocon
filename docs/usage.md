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
  "implementationDirectory": "../",
  "implementationLoopCount": 3,
  "builtinAllowedCommands": ["go test", "git diff", "git status"]
}
```

`workspaceName`は対象リポジトリ直下に作成する成果物ディレクトリの名前です。絶対パス、`..`、パス区切りを含む値は指定できません。

| 設定 | 既定値 | 内容 |
| --- | --- | --- |
| `branchNamePattern` | `issue_#<issue番号>` | 実装worktreeのブランチ名。`<issue番号>`または`<issueNumber>`を置換します。 |
| `implementationDirectory` | `../` | 実装worktreeを置く親ディレクトリ。相対パスは対象リポジトリ基準です。 |
| `implementationLoopCount` | `3` | 実装と検証の最大試行回数。最大10回です。 |
| `builtinAllowedCommands` | korobokcleと同じ既定リスト | Codexのコマンド実行要求を自動承認するコマンドです。省略または空配列では既定リストを使用します。 |

プロンプトを標準入力から渡すこともできます。

```sh
cat prompt.md | go run ./cmd/korocon
```

## 対話型CLIとしての実行

`korocon`は起動時に`codex app-server --stdio`を1回だけ起動してから標準入力を待機します。各入力を同じCodex threadへ順番に送り、AIの最終結果を画面に表示します。通常時の空行は送信しませんが、Issueの承認待ちでは空行を承認として扱います。CodexのJSONイベントと標準エラーはログファイルへリアルタイム追記します。

起動直後に、GitHubから取得する情報として`issue`または`pr`を選択します。`issue`の場合は続けてIssue番号を入力します。koroconは`gh issue view --json`で本文・ラベル・コメントなどを取得し、Issueの状態から設計または実装を判定してCodexへ初期ジョブとして投入します。`pr`の場合は`gh pr list`でPR一覧を表示します。事前にGitHub CLI (`gh`) のログインを完了してください。

標準入力から実行する場合も、最初の行に`issue`または`pr`を指定します。

### Issueの状態遷移

Issue処理ではkorobokcleと同じ状態ラベルを使います。対象ラベルが存在しない場合は`gh label create --force`で作成し、状態更新時は既存の非状態ラベルを保持します。

| 条件 | 処理 | 開始時 | 正常完了時 |
| --- | --- | --- | --- |
| `state:design_approved`がない | 設計 | `state:design_running` | `state:design_ready` |
| `state:design_approved`がある | 実装 | `state:implementation_running` | `state:implementation_ready` |

AI実行が失敗した場合は`state:failed`へ更新します。正常完了時は結果を表示して`state:design_ready`または`state:implementation_ready`へ更新し、同じCLIで承認または修正指示を待ちます。

結果と承認案内の間には区切り線を表示します。承認として扱う入力は、未入力状態でEnter、`承認`、`approve`、`a`などです。設計承認時は結果をIssueコメントへ保存して`state:design_approved`へ、実装承認時は`state:implementation_approved`へ更新します。それ以外の入力はフィードバックとしてAIへ送信し、同じ工程を再実行します。再実行の完了後も結果を表示して承認入力へ戻ります。

### 実装ジョブ

設計承認後は実装ジョブを自動投入します。対象リポジトリが`/home/user/project`、Issue番号が`42`、`implementationDirectory`が既定値の`../`の場合、worktreeは`/home/user/project-42`です。

```text
git -C <repository> worktree add -B <branchName> ../<repositoryName>-<issueNumber> HEAD
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
[job 1] 実行中(実装1回目)...
[job 2] 実行中(検証1回目)...
[job 3] 実行中(実装2回目)...
```

実装結果への修正指示では同じ2つのCodexセッションを継続利用します。実装承認または実装ジョブ失敗時に両セッションを停止します。

正常完了した結果は承認前にMarkdown成果物として保存します。再実行時は同じファイルを上書きします。

| 工程 | 保存先 |
| --- | --- |
| 設計 | `<repository>/<workspaceName>/design/<issue番号>_<正規化タイトル>.md` |
| 実装 | `<repository>/<workspaceName>/implementation/<issue番号>_<正規化タイトル>.md` |

ファイル名とMarkdownの整形はkorobokcleと同じです。タイトルは小文字化し、空白や`/`、`\\`、`:`, `#`, `.`, `,`, `(`, `)`を`-`へ置換します。成果物の先頭見出しは`# <Issueタイトル>`に統一します。

起動時点ですでに`state:design_ready`または`state:implementation_ready`のIssueは、別セッションで完了した承認待ちとして自動実行しません。`state:implementation_approved`または`state:pr_created`のIssueも処理済みとして実行しません。

Codexへ渡す内容は「設計または実装を行う」という工程指示と、Issue番号・タイトル・URL・作成者・本文・ラベル・コメントです。具体的な手順と成果物形式は対象リポジトリのスキルに委ねます。ラベル操作などのワークフロー制御はAIプロンプトへ含めず、korocon自身が実行します。

起動時は入力欄の上にProvider、Model、実行ファイル、ログファイルが表示され、その下に入力待ちの`> `が表示されます。

入力の先頭が `/` の行はコマンドとして扱われます。`/model` で選択可能なモデルを表示し、`/model 1` のように番号、または`/model gpt-5.6-terra` のようにモデル名を指定して切り替えます。koroconは常駐中のCodexへ`/model`相当のモデル変更要求を標準入力で送信し、Codexの成功応答後に表示中のモデルを更新します。選択したモデルは次のターンから同じthreadへ適用されます。先頭に空白がある行はコマンドではなくプロンプトです。

Codexがコマンド実行を要求した場合、`builtinAllowedCommands`に一致するコマンドは自動承認し、`[自動承認]`と対象を表示します。完全一致のほか、安全な引数やLinuxの安全な環境変数代入を付けた実行とCodexが提示する`proposedExecpolicyAmendment`、`commandActions`を判定します。`;`、`&&`、パイプ、コマンド置換などを含む複合実行は、許可コマンドから始まっていても自動承認しません。

許可リストに一致しない操作やファイル変更要求は画面へ表示します。未入力状態でEnterまたは`/approve`を入力すると今回だけ承認し、`/decline`で拒否します。`/allow`を入力すると今回の操作を承認し、Codexの`commandActions`から抽出した具体的なコマンドを実行中の許可リストとバイナリ横の`config.json`へ追加します。Linuxの先頭環境変数代入は除去して保存するため、`GOCACHE=/tmp/cache go test ./...`は`go test ./...`として追加されます。設定保存に失敗した場合は承認せず、承認待ちを継続します。`--dangerously-bypass-approvals-and-sandbox`は使用しません。

```text
provider: codex
model: gpt-5.6-luna
binary: codex
config: /path/to/config.json
workspace: .workspace
branch: issue_#<issue番号>
implementation directory: ../
implementation loops: 3
log: korocon.log
>
[job 1] 実行中（provider: codex, model: gpt-5.6-luna）...
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

デフォルトProviderは `codex` です。デフォルトモデルは
`gpt-5.6-sol`、`gpt-5.6-terra`、`gpt-5.6-luna`、`gpt-5.5`、`gpt-5.4`、
`gpt-5.4-mini`です。デフォルトは
`gpt-5.6-luna`で、`--model`で変更できます。CLI の実行ファイル名は
`--binary` で変更できます。コマンドはシェルを経由せず、引数を分離したまま起動します。

Codexは`app-server --stdio`で起動し、threadには`workspace-write` sandboxと`on-request`承認ポリシーを設定します。危険な全自動実行フラグは使用しません。Copilotを使う場合は`--provider copilot`を指定します。
