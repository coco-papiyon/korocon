# 対話型CLI運用

`korocon`は端末上のCLIとして起動し、起動時にCodex app-serverを常駐させます。標準入力から受け取った指示は、同じCodex threadへ1ターンずつ順番に渡します。

## ビルド

Linux上で実行ファイルを作成します。

```sh
go build -o ./korocon ./cmd/korocon
./korocon doctor
```

実行バイナリと同じディレクトリに`config.json`を配置します。

```json
{
  "workspaceName": ".workspace",
  "branchNamePattern": "issue_#<issue番号>",
  "implementationDirectory": "../",
  "implementationLoopCount": 3,
  "builtinAllowedCommands": ["go test", "git diff", "git status"]
}
```

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

起動時にIssueを選択した場合は、取得したIssueが最初のジョブとして自動投入されます。`state:design_approved`がなければ設計、あれば実装です。設計・実装の具体的な方法はリポジトリのスキルへ委ねられます。

Issueジョブの結果を表示すると、区切り線を挟んで承認または修正指示の案内と`> `を表示します。未入力状態でEnter、`承認`、`approve`、`a`などは承認です。それ以外の入力はAIへのフィードバックとなり、同じ工程を再実行します。実装完了時も同じ入力フローです。

結果は案内表示の前に、対象リポジトリの`<workspaceName>/design/`または`<workspaceName>/implementation/`へ保存します。ファイル名は`<issue番号>_<正規化タイトル>.md`です。

設計承認後は`<implementationDirectory>/<リポジトリ名>-<Issue番号>`へworktreeを作成し、実装用と検証用の2つのCodexを起動します。実装、読み取り専用検証、指摘反映を`implementationLoopCount`回まで繰り返し、検証合格後に実装承認を待ちます。

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
