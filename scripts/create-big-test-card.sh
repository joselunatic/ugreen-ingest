#!/usr/bin/env bash
set -euo pipefail
DIR="$HOME/repos/ugreen-ingest/dev/cards/${1:-CARD_BIG_CANCEL_TEST}"
mkdir -p "$DIR"
for i in $(seq 1 150); do
  dd if=/dev/urandom of="$DIR/CLIP_$i.MOV" bs=1M count=2 status=none
done
du -sh "$DIR"
