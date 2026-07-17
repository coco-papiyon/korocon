# korocon

Go から GitHub Copilot CLI などの AI CLI を起動するための薄いオーケストレーターです。
`korobokcle` の worker 実行・作業ディレクトリ・許可制御の考え方を参考にしています。

## 必要なもの

- Go 1.22 以上
- GitHub Copilot CLI (`copilot`) とログイン済みの GitHub アカウント

## 使い方

```powershell
go run ./cmd/korocon doctor
go run ./cmd/korocon run "このリポジトリの構成を説明して"
go run ./cmd/korocon run --model gpt-5.2-codex --dir . "テストの不足箇所を調べて"
```

プロンプトを標準入力から渡すこともできます。

```powershell
Get-Content prompt.md | go run ./cmd/korocon run
```

初期版で対応する Provider は `copilot` です。CLI の実行ファイル名は
`--binary` で変更できます。コマンドはシェルを経由せず、引数を分離したまま起動します。

`--allow-all-tools` は Copilot にツール実行を無条件で許可するため、必要なときだけ明示してください。
