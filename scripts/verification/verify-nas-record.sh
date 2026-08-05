#!/usr/bin/env sh
set -eu

RECORD=${1:-}

usage() {
  cat <<'USAGE'
Usage: scripts/verification/verify-nas-record.sh <record-file>

Validates a completed NAS deployment evidence record. Use the template in:
  docs/deployment/nas-verification.md

The record must contain these machine-readable PASS markers:
  - zspace_nas: PASS
  - generic_linux_compose: PASS
  - compose_config: PASS
  - external_reachability: PASS
  - backup_restore_rehearsal: PASS
USAGE
}

case "$RECORD" in
  -h|--help)
    usage
    exit 0
    ;;
  "")
    printf 'FAIL: record file is required\n' >&2
    usage >&2
    exit 1
    ;;
esac

[ -f "$RECORD" ] || {
  printf 'FAIL: NAS verification record not found: %s\n' "$RECORD" >&2
  exit 1
}

if grep -Eiq 'TODO|TBD|PENDING|待填|未验证' "$RECORD"; then
  printf 'FAIL: NAS verification record still contains placeholders or pending markers\n' >&2
  exit 1
fi

if grep -Eq '<[^>]+>' "$RECORD"; then
  printf 'FAIL: NAS verification record still contains angle-bracket placeholders\n' >&2
  exit 1
fi

for key in \
  zspace_nas \
  generic_linux_compose \
  compose_config \
  external_reachability \
  backup_restore_rehearsal
do
  grep -Eq "^- ${key}: PASS$" "$RECORD" ||
    {
      printf 'FAIL: NAS verification record missing marker: - %s: PASS\n' "$key" >&2
      exit 1
    }
done

for field in \
  'Release candidate' \
  'Verifier' \
  'Date' \
  'Image tag or digest' \
  'Build host' \
  'Device/model' \
  'CPU architecture' \
  'Docker/Compose version' \
  'Data path' \
  'External mount path' \
  'Member Web loaded' \
  'Admin Web loaded' \
  'Managed mount browse/upload/download' \
  'Reinstall preserves original managed/external files' \
  'Route-group configuration checked' \
  'Application log format and level' \
  'Container log driver and rotation' \
  'Reverse-proxy request ID forwarding' \
  'Failed request correlation by request ID' \
  'Base URL' \
  '`/healthz`' \
  '`/readyz`' \
  '`/` browser entry' \
  'Backup created through Admin Web or REST' \
  'Restore requested' \
  'Integrity checked' \
  'Old sessions invalidated' \
  'External mounts require re-verification'
do
  grep -Eq "^(- )?${field}: [^[:space:]].*" "$RECORD" ||
    {
      printf 'FAIL: NAS verification record missing non-empty evidence field: %s\n' "$field" >&2
      exit 1
    }
done

printf 'ok: NAS verification record passed: %s\n' "$RECORD"
