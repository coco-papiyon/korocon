---
name: verify-runtime
description: koroconの実行時動作を確認する。実装後のスモークテストとして、ビルド、Issue一覧表示、設定一覧表示が正常に動作するか確認するときに使う。
---

# Verify Runtime

## 目的

実装済みのkoroconを実際に起動し、最低限のCLI動作を確認する。検証対象は`go build`、`korocon issue list`、`korocon config list`とする。

## 前提

- 作業ディレクトリはkoroconリポジトリのルートとする。
- GitHub CLI (`gh`)が認証済みであること。
- ネットワーク接続が利用できること。
- 実行ファイルはリポジトリ内の一時ディレクトリへ出力し、既存の`korocon`実行ファイルを上書きしない。

## 手順

### 1. ビルド

リポジトリルートで実行する。

```sh
build_dir="$(mktemp -d /tmp/korocon-verify.XXXXXX)"
go build -o "$build_dir/korocon" ./cmd/korocon
```

ビルドが失敗した場合は、後続の確認を行わず失敗として報告する。

### 2. Issue一覧

ビルドした実行ファイルを使い、次を実行する。

```sh
"$build_dir/korocon" issue list --dir .
```

確認項目:

- コマンドが終了コード0で完了すること
- 標準出力にIssue一覧、または`表示対象のIssueがありません`が出力されること
- ビルド失敗やGitHub CLIエラーがないこと

### 3. 設定一覧

同じ実行ファイルで次を実行する。

```sh
"$build_dir/korocon" config list
```

確認項目:

- コマンドが終了コード0で完了すること
- `設定一覧`と`config:`が出力されること
- 設定値の表示中にエラーが発生しないこと

## 報告

結果は次の順で簡潔にまとめる。

```markdown
## 判定結果
## 実行コマンド
## 確認結果
## 指摘事項
## 残課題
```

一時ビルドディレクトリは検証終了後に削除してよい。ソース、設定ファイル、GitHub上のIssue/PRは変更しない。
