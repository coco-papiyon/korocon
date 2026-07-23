#!/bin/bash

PROVIDER=${PROVIDER:-codex}
MODEL=${MODEL:-gpt-5.6-luna}

go run ./cmd/korocon/ --implementer \
  --provider "$PROVIDER" --model "$MODEL" \
  --reviewer-provider "$PROVIDER" --reviewer-model "$MODEL" \
  --verifier-provider "$PROVIDER" --verifier-model "$MODEL" \
  --sync-dirty stash \
  $@
