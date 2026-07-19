#!/bin/bash

go run ./cmd/korocon/ --implementer \
  --provider copilot --model claude-sonnet-4.6 \
  $@
