#!/bin/bash

go run ./cmd/korocon/ --implementer \
  --provider copilot --model claude-sonnet-4.6 \
  --reviewer-provider copilot --reviewer-model claude-sonnet-4.6 \
  --verifier-provider copilot --verifier-model claude-sonnet-4.6 \
  $@
