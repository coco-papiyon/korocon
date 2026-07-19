#!/bin/bash

go run ./cmd/korocon/ --reviewer --assignee "" \
  --provider copilot --model claude-sonnet-4.6 \
  $@
