#!/bin/bash

go run ./cmd/korocon/ --reviewer --assignee "" \
  --provider copilot --model claude-sonnet-4.6 \
  --reviewer-provider copilot --reviewer-model claude-sonnet-4.6 \
  --verifier-provider copilot --verifier-model claude-sonnet-4.6 \
  $@
