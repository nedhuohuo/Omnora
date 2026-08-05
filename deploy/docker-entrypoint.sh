#!/bin/sh
set -eu

CONFIG_DIR="${OMNORA_CONFIG_DIR:-/etc/omnora}"
DATA_DIR="${OMNORA_DATA_DIR:-/var/lib/omnora}"
MANAGED_DIR="${OMNORA_MANAGED_STORAGE_DIR:-/srv/omnora/managed}"
SECRETS_FILE="${OMNORA_SECRETS_FILE:-$CONFIG_DIR/runtime.env}"

mkdir -p "$CONFIG_DIR" "$DATA_DIR" "$MANAGED_DIR"

if [ -f "$SECRETS_FILE" ]; then
	# shellcheck disable=SC1090
	. "$SECRETS_FILE"
fi

random_hex() {
	od -An -N32 -tx1 /dev/urandom | tr -d ' \n'
}

save_runtime_secrets=false

if [ -z "${OMNORA_INITIALIZATION_TOKEN:-}" ]; then
	OMNORA_INITIALIZATION_TOKEN="$(random_hex)"
	export OMNORA_INITIALIZATION_TOKEN
	save_runtime_secrets=true
fi

if [ -z "${OMNORA_TOTP_ENCRYPTION_KEY:-}" ]; then
	OMNORA_TOTP_ENCRYPTION_KEY="$(random_hex)"
	export OMNORA_TOTP_ENCRYPTION_KEY
	save_runtime_secrets=true
fi

if [ "$save_runtime_secrets" = true ]; then
	tmp_file="$SECRETS_FILE.tmp"
	umask 077
	{
		printf 'OMNORA_INITIALIZATION_TOKEN=%s\n' "$OMNORA_INITIALIZATION_TOKEN"
		printf 'OMNORA_TOTP_ENCRYPTION_KEY=%s\n' "$OMNORA_TOTP_ENCRYPTION_KEY"
	} > "$tmp_file"
	mv "$tmp_file" "$SECRETS_FILE"
	chmod 600 "$SECRETS_FILE"
	printf 'Omnora generated runtime secrets in %s\n' "$SECRETS_FILE" >&2
	printf 'Initial setup token: %s\n' "$OMNORA_INITIALIZATION_TOKEN" >&2
fi

exec "$@"
