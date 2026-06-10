#!/usr/bin/env bash

# backup.sh - Timestamped compressed backups of files and directories
# Uses tar + the fastest available compressor (pigz fast profile preferred,
# then zstd, lz4, etc.) for maximum speed on standard Linux distros.
# Falls back gracefully to gzip if nothing faster is installed.

set -o pipefail

if [ $# -eq 0 ]; then
  cat <<EOF
Usage: $(basename "$0") <file|directory> [file|directory] ...

  Creates timestamped tar archives (.tar.gz, .tar.zst, .tar.lz4 etc.)
  compressed with the fastest compressor available on the system.

  Compressor priority (fastest practical first):
    - pigz (with fast profile -1, multi-threaded)
    - zstd (-T0 -1, multi-threaded)
    - lz4 (very fast)
    - xz (fast mode, multi-threaded)
    - gzip (single-threaded fallback)

  Wildcards allowed (via shell): $0 data/*.csv  data/

EOF
  exit 2
fi

T=$(date +%Y%m%d%H%M%S)
NCORES=$(nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 2)

get_compressor() {
  # Order chosen for speed (wall time) + reasonable compression.
  # pigz first per request; zstd is excellent all-rounder on modern distros.
  local candidates=(
    "pigz -p${NCORES} -1 -c:.tar.gz"
    "zstd -T0 -1 -c:.tar.zst"
    "lz4 -1 -c:.tar.lz4"
    "xz -T0 -0 -c:.tar.xz"
    "gzip -1 -c:.tar.gz"
  )
  for cand in "${candidates[@]}"; do
    local prog=${cand%% *}
    if command -v "$prog" >/dev/null 2>&1; then
      echo "$cand"
      return 0
    fi
  done
  echo "gzip -1 -c:.tar.gz"
}

COMPRESSOR_INFO=$(get_compressor)
COMPRESSOR=${COMPRESSOR_INFO%%:*}
EXT=${COMPRESSOR_INFO#*:}

echo "Compressor: $COMPRESSOR  (cores: $NCORES)"
echo

lastError=0

for INPUT in "$@"; do
  # Normalize: strip trailing slash (for directories)
  INPUT=${INPUT%/}
  if [ -z "$INPUT" ]; then
    INPUT="."
  fi

  if [ ! -e "$INPUT" ]; then
    echo "Error: Not found: $INPUT"
    lastError=1
    continue
  fi

  BASE=$(basename "$INPUT")
  if [ "$BASE" = "." ] || [ "$BASE" = ".." ] || [ -z "$BASE" ]; then
    echo "Error: Cannot backup '.' or '..' directly. Pass a named file or directory instead."
    lastError=1
    continue
  fi

  DIR=$(dirname "$INPUT")
  if [ "$DIR" = "." ]; then
    OUTPUT="${BASE}.${T}${EXT}"
  else
    OUTPUT="${DIR}/${BASE}.${T}${EXT}"
  fi

  ORIG_SIZE=$(du -sh "$INPUT" 2>/dev/null | awk '{print $1}' || echo "?")
  echo "Backing up: $INPUT ($ORIG_SIZE) -> $OUTPUT"

  if tar -C "$DIR" -cvf - "$BASE" | $COMPRESSOR > "$OUTPUT"; then
    SIZE=$(du -sh "$OUTPUT" 2>/dev/null | awk '{print $1}' || echo "?")
    echo "Done: $OUTPUT ($SIZE)"
  else
    RET=$?
    echo "Error ($RET): failed to backup $INPUT -> $OUTPUT"
    rm -f "$OUTPUT" 2>/dev/null || true
    lastError=1
  fi
  echo
done

exit $lastError
