#!/usr/bin/env bash
# Simulates inserting a card: creates a directory under dev/cards with a
# few small dummy files. The running app picks it up on its next scan
# (every 2s) with no restart needed.
set -euo pipefail

NAME="${1:-CARD_TEST_$(date +%s)}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CARD_DIR="$ROOT_DIR/dev/cards/$NAME"

if [ -e "$CARD_DIR" ]; then
  echo "Card $NAME already exists at $CARD_DIR" >&2
  exit 1
fi

mkdir -p "$CARD_DIR/DCIM/100CANON" "$CARD_DIR/AUDIO"
for i in 1 2 3; do
  head -c $((1024 * (RANDOM % 500 + 50))) /dev/urandom > "$CARD_DIR/DCIM/100CANON/IMG_000${i}.JPG"
done
head -c $((1024 * 1024)) /dev/urandom > "$CARD_DIR/AUDIO/take1.wav"

echo "Created mock card: $CARD_DIR"
