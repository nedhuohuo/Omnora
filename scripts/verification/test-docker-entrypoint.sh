#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
ENTRYPOINT="$ROOT/deploy/docker-entrypoint.sh"
TMP_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/omnora-entrypoint-test.XXXXXX")

cleanup() {
	rm -rf "$TMP_ROOT"
}
trap cleanup EXIT INT TERM

run_entrypoint() {
	config_dir="$1"
	data_dir="$2"
	managed_dir="$3"
	db_path="$data_dir/omnora.db"
	OMNORA_CONFIG_DIR="$config_dir" \
	OMNORA_DATA_DIR="$data_dir" \
	OMNORA_DB_PATH="$db_path" \
	OMNORA_MANAGED_STORAGE_DIR="$managed_dir" \
	OMNORA_INITIALIZATION_TOKEN= \
	OMNORA_TOTP_ENCRYPTION_KEY= \
	sh "$ENTRYPOINT" /bin/sh -c 'printf ready'
}

run_entrypoint_without_secret_env() {
	config_dir="$1"
	data_dir="$2"
	managed_dir="$3"
	db_path="$data_dir/omnora.db"
	env -u OMNORA_INITIALIZATION_TOKEN -u OMNORA_TOTP_ENCRYPTION_KEY \
		OMNORA_CONFIG_DIR="$config_dir" \
		OMNORA_DATA_DIR="$data_dir" \
		OMNORA_DB_PATH="$db_path" \
		OMNORA_MANAGED_STORAGE_DIR="$managed_dir" \
		sh "$ENTRYPOINT" /bin/sh -c 'test -n "$OMNORA_INITIALIZATION_TOKEN" && test -n "$OMNORA_TOTP_ENCRYPTION_KEY" && printf ready'
}

config_dir="$TMP_ROOT/config"
data_dir="$TMP_ROOT/data"
managed_dir="$TMP_ROOT/managed"
db_path="$data_dir/omnora.db"
mkdir -p "$config_dir" "$data_dir" "$managed_dir"

[ "$(run_entrypoint "$config_dir" "$data_dir" "$managed_dir")" = ready ] || exit 1
token_before=$(sed -n 's/^OMNORA_INITIALIZATION_TOKEN=//p' "$config_dir/runtime.env")
config_instance=$(sed -n '1p' "$config_dir/.omnora-instance-id")
data_instance=$(sed -n '1p' "$data_dir/.omnora-instance-id")
[ -n "$token_before" ] || exit 1
[ "$config_instance" = "$data_instance" ] || exit 1
: > "$db_path"

[ "$(run_entrypoint "$config_dir" "$data_dir" "$managed_dir")" = ready ] || exit 1
token_after=$(sed -n 's/^OMNORA_INITIALIZATION_TOKEN=//p' "$config_dir/runtime.env")
[ "$token_before" = "$token_after" ] || exit 1
[ "$(run_entrypoint_without_secret_env "$config_dir" "$data_dir" "$managed_dir")" = ready ] || exit 1

mv "$db_path" "$db_path.saved"
if run_entrypoint "$config_dir" "$data_dir" "$managed_dir" >/dev/null 2>&1; then
	printf 'FAIL: missing database was accepted for an existing instance\n' >&2
	exit 1
fi
mv "$db_path.saved" "$db_path"

mv "$config_dir/runtime.env" "$config_dir/runtime.env.saved"
if run_entrypoint "$config_dir" "$data_dir" "$managed_dir" >/dev/null 2>&1; then
	printf 'FAIL: missing runtime secrets were regenerated for an existing instance\n' >&2
	exit 1
fi
mv "$config_dir/runtime.env.saved" "$config_dir/runtime.env"

new_data_dir="$TMP_ROOT/new-data"
mkdir -p "$new_data_dir"
if run_entrypoint "$config_dir" "$new_data_dir" "$managed_dir" >/dev/null 2>&1; then
	printf 'FAIL: mismatched config/data bind mounts were accepted\n' >&2
	exit 1
fi

printf 'ok: Docker entrypoint preserves runtime secrets and rejects mismatched persistent mounts\n'
