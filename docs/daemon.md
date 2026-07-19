# 対話型CLI運用

`korocon`は端末上のCLIとして起動し、起動時にCodex app-serverを常駐させます。標準入力から受け取った指示は、同じCodex threadへ1ターンずつ順番に渡します。

AIの結果本文とCLI自身の案内を区別するため、システムメッセージは`---`を1回だけ出力し、各行の先頭に`[システム] `を付けます。`[job N]`や`[承認待ち]`など既に明確な状態表示はそのまま表示し、AI本文には接頭辞を付けません。TTY・非TTYとも入力プロンプトは従来どおり`> `です。

```text
AIの複数行メッセージ
最後の行
---
[システム] フィードバックをAIへ送信し、再設計します。
[job 2] 実行中...
```

## ビルド

Linux上で実行ファイルを作成します。

```sh
go build -o ./korocon ./cmd/korocon
./korocon doctor
```

実行バイナリと同じディレクトリに`config.json`を配置します。

手動作成の代わりに`korocon config init`で対話初期化できます。`baseBranch`、`branchNamePattern`、`startupCommand`を入力した後、役割別のProviderとModelを設定します。モデル設定だけを変更する場合は`korocon config model`を使用します。

`builtinAllowedCommands`へコマンドを追加する場合は`korocon config allow "go test ./..."`を実行します。引数を省略すると追加するコマンドを対話入力できます。

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
  "builtinAllowedCommands": ["git add", "git diff", "git status", "go test"]
}
```

検証者・レビューアのProviderまたはModelを省略すると、実装者の値を使用します。同名のCLI引数は設定ファイルより優先されます。

`doctor`が成功しない場合は、OpenAI Codex CLIをインストールしてログインしてください。

## 起動

フォアグラウンドのCLIとして起動します。CLI自身をバックグラウンド化しません。

```sh
./korocon --dir /path/to/repository
```

オプションは`run`と共通です。

```sh
./korocon \
  --binary /usr/local/bin/codex \
  --model gpt-5.6-luna \
  --dir /path/to/repository \
  --stream-logs
```

## ジョブ投入

すべてのジョブは、AIへ投入する前に対象リポジトリで`git fetch --prune origin`と`git pull --ff-only`を実行します。同期に失敗したジョブはAIを開始せず失敗として表示します。

起動時にIssueを選択した場合は、取得したIssueが最初のジョブとして自動投入されます。`state:design_approved`がなければ設計、あれば実装です。設計・実装の具体的な方法はリポジトリのスキルへ委ねられます。

起動時にPRを選択した場合は、状態付きPR一覧から番号を入力し、選択PRのレビューを初期ジョブとして投入します。レビュー承認は動作確認へ進み、`/rerun`は再レビュー、その他の入力はレビュー修正指示としてPRへ登録します。レビュー指摘修正PRを再選択すると全コメントを保存・表示して修正方針を待ち、入力後にPR head worktreeで実装者と検証者が最大3回の実装・検証を行います。修正承認後はPR headへpushして再レビューします。動作確認後にPRがCLOSEDまたはMERGEDであることを確認すると完了し、最初の選択へ戻ります。

Issueジョブの結果を表示すると、区切り線を挟んで承認または修正指示の案内と`> `を表示します。未入力状態でEnter、`承認`、`approve`、`a`などは承認です。それ以外の入力はAIへのフィードバックとなり、同じ工程を再実行します。実装完了時も同じ入力フローです。

結果は案内表示の前に、対象リポジトリの`<workspaceName>/design/`または`<workspaceName>/implementation/`へ保存します。ファイル名は`<issue番号>_<正規化タイトル>.md`です。

実装・検証ループの各応答は、`implementation/<issue番号>/<回数>回目_実装.md`と`implementation/<issue番号>/<回数>回目_検討.md`へそれぞれ保存します。

設計承認後は`<implementationDirectory>/<リポジトリ名>-<Issue番号>`へworktreeを作成します。既定の親ディレクトリは`../<リポジトリ名>-branches/`です。そのworktreeで役割設定に従った実装用と検証用の2つのAIセッションを起動し、実装、読み取り専用検証、指摘反映を`implementationLoopCount`回まで繰り返して、検証合格後に実装承認を待ちます。

実装承認時はworktreeの変更をcommitし、ブランチをpushして`baseBranch`向けのPRを作成します。成功するとPR URLを表示してIssueを`state:pr_created`へ更新し、実装用・検証用Codexを停止して最初の`issue`/`pr`選択へ戻ります。失敗時は承認待ちとCodexセッションを維持します。

標準入力へプロンプトを1行ずつ書き込みます。入力中もCLIは操作できますが、Codexのターンは入力順に実行されます。

```sh
printf '%s\n' 'リポジトリの構成を説明して' | ./korocon --binary /bin/echo
```

実際の常駐運用では、FIFOを入力に使えます。

```sh
mkfifo /tmp/korocon-prompts
./korocon --dir /path/to/repository < /tmp/korocon-prompts
```

別のシェルからジョブを投入します。

```sh
printf '%s\n' 'テストの不足箇所を調べて' > /tmp/korocon-prompts
```

入力を閉じると、投入済みターンの完了後にCLIも終了します。`Ctrl+C`またはSIGTERMを送ると、実行中のターンをキャンセルし、常駐Codexプロセスを終了します。

## 出力

ターン完了時にCodexの最終メッセージを表示します。処理は入力順です。失敗時は`[job ID] error: ...`を表示します。

直前に完了したジョブの作業ツリー差分は、`/diff`で表示できます。`/diff ファイル名`と入力すると、差分を作業ディレクトリ配下の指定ファイルへ保存します。完了したジョブに差分がない場合は、その旨を表示します。

## 注意事項

- `builtinAllowedCommands`に一致する安全なコマンド実行要求は自動承認します。省略または空配列の場合はkorobokcleと同じ既定リストです。
- 許可外の承認要求が表示された場合は未入力Enterまたは`/approve`で今回だけ承認し、`/allow`で承認して`config.json`の自動承認リストへ追加し、`/decline`で拒否します。
- CLIはプロンプトをシェルに渡しませんが、AI CLIにはそのまま渡されます。
- FIFOやCLIの標準出力には、必要に応じて適切なUnix権限を設定してください。
- 実行するユーザーには、作業ディレクトリとAI CLIを実行する権限が必要です。
