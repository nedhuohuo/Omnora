#!/bin/sh
set -eu

CONFIG_DIR="${OMNORA_CONFIG_DIR:-/etc/omnora}"
DATA_DIR="${OMNORA_DATA_DIR:-/var/lib/omnora}"
DB_PATH="${OMNORA_DB_PATH:-$DATA_DIR/omnora.db}"
MANAGED_DIR="${OMNORA_MANAGED_STORAGE_DIR:-/srv/omnora/managed}"
SECRETS_FILE="${OMNORA_SECRETS_FILE:-$CONFIG_DIR/runtime.env}"
CONFIG_INSTANCE_FILE="${OMNORA_CONFIG_INSTANCE_FILE:-$CONFIG_DIR/.omnora-instance-id}"
DATA_INSTANCE_FILE="${OMNORA_DATA_INSTANCE_FILE:-$DATA_DIR/.omnora-instance-id}"
MANAGED_INSTANCE_FILE="${OMNORA_MANAGED_INSTANCE_FILE:-$MANAGED_DIR/.omnora-instance-id}"
ROOT_CONFIG_DIR=/etc/omnora
ROOT_DATA_DIR=/var/lib/omnora
ROOT_MANAGED_DIR=/srv/omnora/managed
ROOT_PREDECLARED_MOUNT_ROOT=/mnt/omnora

fail_persistence_check() {
	printf 'Omnora persistent state check failed: %s\n' "$1" >&2
	exit 1
}

directory_has_entries() {
	directory_entries=''
	if ! directory_entries="$(find "$1" -mindepth 1 -maxdepth 1 -print -quit)"; then
		fail_persistence_check "cannot inspect a persistent directory"
	fi
	[ -n "$directory_entries" ]
}

path_exists() {
	[ -e "$1" ] || [ -L "$1" ]
}

inode_number() {
	if inode_value="$(stat -L -c '%i' "$1" 2>/dev/null)"; then
		printf '%s' "$inode_value"
		return 0
	fi
	stat -L -f '%i' "$1" 2>/dev/null
}

read_instance_file() {
	volume="$1"
	file="$2"
	[ ! -L "$file" ] || fail_persistence_check "$volume instance marker is a symbolic link; use the offline migration command to repair persistent volume identity"
	[ -f "$file" ] || fail_persistence_check "$volume instance marker is missing or is not a regular file; use the offline migration command to repair persistent volume identity"
	if ! exec 3< "$file"; then
		fail_persistence_check "$volume instance marker cannot be opened"
	fi
	marker_path_inode="$(inode_number "$file")" || marker_path_inode=''
	marker_fd_inode="$(inode_number /dev/fd/3)" || marker_fd_inode=''
	if [ -L "$file" ] || [ ! -f "$file" ] || [ -z "$marker_path_inode" ] ||
		[ "$marker_path_inode" != "$marker_fd_inode" ]; then
		exec 3<&-
		fail_persistence_check "$volume instance marker changed during validation"
	fi

	marker_id=''
	if IFS= read -r marker_id <&3; then
		extra_marker_data=''
		if IFS= read -r extra_marker_data <&3 || [ -n "$extra_marker_data" ]; then
			exec 3<&-
			fail_persistence_check "$volume instance marker is invalid"
		fi
	fi
	exec 3<&-
	[ "${#marker_id}" = 64 ] || fail_persistence_check "$volume instance marker is invalid"
	case "$marker_id" in
		*[!0-9a-f]*) fail_persistence_check "$volume instance marker is invalid" ;;
	esac
	printf '%s' "$marker_id"
}

validate_persistent_state() {
	persistent_instance_exists=false
	for persistent_dir in "$CONFIG_DIR" "$DATA_DIR" "$MANAGED_DIR"; do
		if path_exists "$persistent_dir"; then
			[ ! -L "$persistent_dir" ] && [ -d "$persistent_dir" ] ||
				fail_persistence_check "persistent root is not a real directory"
			if directory_has_entries "$persistent_dir"; then
				persistent_instance_exists=true
			fi
		fi
	done
	if path_exists "$CONFIG_INSTANCE_FILE" || path_exists "$DATA_INSTANCE_FILE" ||
		path_exists "$MANAGED_INSTANCE_FILE" || path_exists "$SECRETS_FILE" ||
		path_exists "$DB_PATH" || path_exists "$DB_PATH-wal" || path_exists "$DB_PATH-shm"; then
		persistent_instance_exists=true
	fi

	[ "$persistent_instance_exists" = true ] || return 0
	config_instance_id="$(read_instance_file config "$CONFIG_INSTANCE_FILE")"
	data_instance_id="$(read_instance_file data "$DATA_INSTANCE_FILE")"
	managed_instance_id="$(read_instance_file managed "$MANAGED_INSTANCE_FILE")"
	[ "$config_instance_id" = "$data_instance_id" ] && [ "$config_instance_id" = "$managed_instance_id" ] ||
		fail_persistence_check "config, data, and managed volumes belong to different Omnora instances"
	[ -f "$DB_PATH" ] && [ ! -L "$DB_PATH" ] ||
		fail_persistence_check "database is missing or is not a regular file; check the data bind mount before redeploying"
	INSTANCE_ID="$config_instance_id"
}

drop_privileges_for_persistence() {
	[ "$(id -u)" = 0 ] || return 0

	validate_persistent_state
	command -v su-exec >/dev/null 2>&1 || fail_persistence_check "su-exec is required to drop root privileges"
	mkdir -p "$ROOT_CONFIG_DIR" "$ROOT_DATA_DIR" "$ROOT_MANAGED_DIR" ||
		fail_persistence_check "cannot prepare persistent directories"
	chown 1000:1000 "$ROOT_CONFIG_DIR" "$ROOT_DATA_DIR" "$ROOT_MANAGED_DIR" ||
		fail_persistence_check "cannot prepare persistent directory ownership"
	if ! chown 1000:1000 "$ROOT_PREDECLARED_MOUNT_ROOT"; then
		printf 'Omnora warning: cannot prepare external mount root ownership at %s; read-write mounts may be unavailable\n' "$ROOT_PREDECLARED_MOUNT_ROOT" >&2
	fi
	exec su-exec 1000:1000 "$0" "$@"
}

drop_privileges_for_persistence "$@"

mkdir -p "$CONFIG_DIR" "$DATA_DIR" "$MANAGED_DIR"

random_hex() {
	od -An -N32 -tx1 /dev/urandom | tr -d ' \n'
}

write_instance_file() {
	file="$1"
	if [ -e "$file" ] || [ -L "$file" ]; then
		fail_persistence_check "refusing to replace an existing instance marker"
	fi
	instance_tmp_file="$(mktemp "$file.tmp.XXXXXX")" || fail_persistence_check "cannot create a temporary instance marker"
	case "$instance_tmp_file" in
		"$file".tmp.*) ;;
		*) fail_persistence_check "temporary instance marker is outside its persistent directory" ;;
	esac
	[ ! -L "$instance_tmp_file" ] && [ -f "$instance_tmp_file" ] ||
		fail_persistence_check "temporary instance marker is not a regular file"
	printf '%s\n' "$INSTANCE_ID" > "$instance_tmp_file" || fail_persistence_check "cannot write a temporary instance marker"
	chmod 600 "$instance_tmp_file" || fail_persistence_check "cannot protect a temporary instance marker"
	ln -n "$instance_tmp_file" "$file" || fail_persistence_check "instance marker target already exists or cannot be installed"
	temporary_inode="$(inode_number "$instance_tmp_file")" || temporary_inode=''
	published_inode="$(inode_number "$file")" || published_inode=''
	if [ -L "$file" ] || [ ! -f "$file" ] || [ -z "$temporary_inode" ] ||
		[ "$temporary_inode" != "$published_inode" ]; then
		fail_persistence_check "instance marker changed during installation"
	fi
	rm -f "$instance_tmp_file" || fail_persistence_check "cannot remove a temporary instance marker"
	instance_tmp_file=''
}

instance_tmp_file=''
cleanup_instance_tmp() {
	if [ -n "${instance_tmp_file:-}" ]; then
		case "$instance_tmp_file" in
			"$CONFIG_INSTANCE_FILE".tmp.*|"$DATA_INSTANCE_FILE".tmp.*|"$MANAGED_INSTANCE_FILE".tmp.*)
				rm -f "$instance_tmp_file" || true
				;;
		esac
		instance_tmp_file=''
	fi
}

trap 'cleanup_instance_tmp' EXIT

instance_markers_created=false
validate_persistent_state

if [ "$persistent_instance_exists" = true ]; then
	:
else
	INSTANCE_ID="$(random_hex)"
	umask 077
	write_instance_file "$CONFIG_INSTANCE_FILE"
	write_instance_file "$DATA_INSTANCE_FILE"
	write_instance_file "$MANAGED_INSTANCE_FILE"
	instance_markers_created=true
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
runtime_secrets_created=false

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

if [ -z "${OMNORA_AUDIT_HMAC_KEY:-}" ]; then
	OMNORA_AUDIT_HMAC_KEY="$(random_hex)"
	export OMNORA_AUDIT_HMAC_KEY
	save_runtime_secrets=true
fi
if [ ! -f "$SECRETS_FILE" ]; then
	# A new instance persists explicitly supplied values too, so a restart does
	# not silently switch to a different audit namespace.
	save_runtime_secrets=true
fi

runtime_tmp_file=''
cleanup_runtime_tmp() {
	if [ -n "${runtime_tmp_file:-}" ]; then
		case "$runtime_tmp_file" in
			"$SECRETS_FILE".tmp.*) rm -f "$runtime_tmp_file" || true ;;
		esac
		runtime_tmp_file=''
	fi
}

append_secret_line() {
	name="$1"
	value="$2"
	file="$3"
	if grep -q "^${name}=" "$file"; then
		existing_value="$(sed -n "s/^${name}=//p" "$file" | head -n 1)"
		[ -n "$existing_value" ] || fail_persistence_check "$name is empty in $SECRETS_FILE"
		return 0
	fi
	if [ -s "$file" ]; then
		last_byte="$(tail -c 1 "$file" | od -An -t x1 | tr -d ' \n\r\t')"
		[ "$last_byte" = 0a ] || printf '\n' >> "$file"
	fi
	printf '%s=%s\n' "$name" "$value" >> "$file"
}

# Remove only the validated same-directory temporary path if any persistence
# step exits before the atomic rename completes.
trap 'cleanup_instance_tmp; cleanup_runtime_tmp' EXIT

if [ "$save_runtime_secrets" = true ]; then
	umask 077
	runtime_tmp_file="$(mktemp "$SECRETS_FILE.tmp.XXXXXX")" || fail_persistence_check "cannot create a temporary runtime secrets file"
	if [ -f "$SECRETS_FILE" ]; then
		cp "$SECRETS_FILE" "$runtime_tmp_file" || fail_persistence_check "cannot copy $SECRETS_FILE to a temporary file"
	else
		: > "$runtime_tmp_file" || fail_persistence_check "cannot initialize a temporary runtime secrets file"
	fi
	# Append only lines that are absent. This preserves every existing secret
	# byte and keeps old initialization/TOTP keys stable across upgrades.
	append_secret_line OMNORA_INITIALIZATION_TOKEN "$OMNORA_INITIALIZATION_TOKEN" "$runtime_tmp_file"
	append_secret_line OMNORA_TOTP_ENCRYPTION_KEY "$OMNORA_TOTP_ENCRYPTION_KEY" "$runtime_tmp_file"
	append_secret_line OMNORA_AUDIT_HMAC_KEY "$OMNORA_AUDIT_HMAC_KEY" "$runtime_tmp_file"
	chmod 600 "$runtime_tmp_file" || fail_persistence_check "cannot protect temporary runtime secrets file"
	mv "$runtime_tmp_file" "$SECRETS_FILE" || fail_persistence_check "cannot install $SECRETS_FILE"
	runtime_tmp_file=''
	runtime_secrets_created=true
	printf 'Omnora generated runtime secrets in %s\n' "$SECRETS_FILE" >&2
fi

forward_child_signal() {
	if [ -n "${child_pid:-}" ]; then
		kill -TERM "$child_pid" 2>/dev/null || true
	fi
}

trap 'cleanup_instance_tmp; cleanup_runtime_tmp; forward_child_signal' HUP INT TERM
set +e
"$@" &
child_pid=$!
wait "$child_pid"
child_status=$?
set -e
trap - HUP INT TERM
child_pid=''

if [ "$child_status" -ne 0 ] && [ "$instance_markers_created" = true ] && [ ! -f "$DB_PATH" ]; then
	rm -f "$CONFIG_INSTANCE_FILE" "$DATA_INSTANCE_FILE" "$MANAGED_INSTANCE_FILE"
	if [ "$runtime_secrets_created" = true ]; then
		rm -f "$SECRETS_FILE"
	fi
fi

exit "$child_status"
