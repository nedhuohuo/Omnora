#!/bin/sh
set -eu

CONFIG_DIR="${OMNORA_CONFIG_DIR:-/etc/omnora}"
DATA_DIR="${OMNORA_DATA_DIR:-/var/lib/omnora}"
MANAGED_DIR="${OMNORA_MANAGED_STORAGE_DIR:-/srv/omnora/managed}"
SECRETS_FILE="${OMNORA_SECRETS_FILE:-$CONFIG_DIR/runtime.env}"
CONFIG_INSTANCE_FILE="${OMNORA_CONFIG_INSTANCE_FILE:-$CONFIG_DIR/.omnora-instance-id}"
DATA_INSTANCE_FILE="${OMNORA_DATA_INSTANCE_FILE:-$DATA_DIR/.omnora-instance-id}"

mkdir -p "$CONFIG_DIR" "$DATA_DIR" "$MANAGED_DIR"

fail_persistence_check() {
	printf 'Omnora persistent state check failed: %s\n' "$1" >&2
	exit 1
}

random_hex() {
	od -An -N32 -tx1 /dev/urandom | tr -d ' \n'
}

write_instance_file() {
	file="$1"
	tmp_file="$file.tmp"
	{
		printf '%s\n' "$INSTANCE_ID"
	} > "$tmp_file" || fail_persistence_check "cannot write $file"
	mv "$tmp_file" "$file" || fail_persistence_check "cannot install $file"
}

config_instance_id=''
data_instance_id=''
persistent_instance_exists=false
if [ -f "$CONFIG_INSTANCE_FILE" ] || [ -f "$DATA_INSTANCE_FILE" ]; then
	persistent_instance_exists=true
	[ -f "$CONFIG_INSTANCE_FILE" ] || fail_persistence_check "missing $CONFIG_INSTANCE_FILE; check the config bind mount"
	[ -f "$DATA_INSTANCE_FILE" ] || fail_persistence_check "missing $DATA_INSTANCE_FILE; check the data bind mount"
	config_instance_id="$(tr -d ' \r\n\t' < "$CONFIG_INSTANCE_FILE")"
	data_instance_id="$(tr -d ' \r\n\t' < "$DATA_INSTANCE_FILE")"
	[ -n "$config_instance_id" ] || fail_persistence_check "$CONFIG_INSTANCE_FILE is empty"
	[ "$config_instance_id" = "$data_instance_id" ] || fail_persistence_check "config and data bind mounts belong to different Omnora instances"
	INSTANCE_ID="$config_instance_id"
else
	INSTANCE_ID="$(random_hex)"
	umask 077
	write_instance_file "$CONFIG_INSTANCE_FILE"
	write_instance_file "$DATA_INSTANCE_FILE"
fi

if [ -f "$SECRETS_FILE" ]; then
	# shellcheck disable=SC1090
	set -a
	. "$SECRETS_FILE"
	set +a
fi

if [ "$persistent_instance_exists" = true ] && [ ! -f "$SECRETS_FILE" ] &&
	{ [ -z "${OMNORA_INITIALIZATION_TOKEN:-}" ] || [ -z "${OMNORA_TOTP_ENCRYPTION_KEY:-}" ]; }; then
	fail_persistence_check "missing $SECRETS_FILE; restore the original config bind mount or provide both original secrets"
fi

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
