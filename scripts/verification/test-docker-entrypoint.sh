#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
ENTRYPOINT="$ROOT/deploy/docker-entrypoint.sh"
COMPOSE="$ROOT/deploy/docker-compose.yml"

grep -Fq 'stat -L' "$ENTRYPOINT" || {
	printf 'FAIL: Docker entrypoint must dereference files when comparing path and fd inodes\n' >&2
	exit 1
}
NAS_COMPOSE="$ROOT/deploy/docker-compose.nas.yml"
TMP_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/omnora-entrypoint-test.XXXXXX")

cleanup() {
	rm -rf "$TMP_ROOT"
}
trap cleanup EXIT INT TERM

for compose_file in "$COMPOSE" "$NAS_COMPOSE"; do
	grep -Fq 'cap_add:' "$compose_file" || {
		printf 'FAIL: %s does not grant the entrypoint minimal ownership capabilities\n' "$compose_file" >&2
		exit 1
	}
	for capability in CHOWN SETGID SETUID; do
		grep -Fq "      - $capability" "$compose_file" || {
			printf 'FAIL: %s does not grant CAP_%s\n' "$compose_file" "$capability" >&2
			exit 1
		}
	done
done

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
	OMNORA_AUDIT_HMAC_KEY= \
	sh "$ENTRYPOINT" /bin/sh -c 'printf ready'
}

run_entrypoint_without_secret_env() {
	config_dir="$1"
	data_dir="$2"
	managed_dir="$3"
	db_path="$data_dir/omnora.db"
	env -u OMNORA_INITIALIZATION_TOKEN -u OMNORA_TOTP_ENCRYPTION_KEY -u OMNORA_AUDIT_HMAC_KEY \
		OMNORA_CONFIG_DIR="$config_dir" \
		OMNORA_DATA_DIR="$data_dir" \
		OMNORA_DB_PATH="$db_path" \
		OMNORA_MANAGED_STORAGE_DIR="$managed_dir" \
		sh "$ENTRYPOINT" /bin/sh -c 'test -n "$OMNORA_INITIALIZATION_TOKEN" && test -n "$OMNORA_TOTP_ENCRYPTION_KEY" && test -n "$OMNORA_AUDIT_HMAC_KEY" && printf ready'
}

run_entrypoint_with_failure() {
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
	OMNORA_AUDIT_HMAC_KEY= \
	sh "$ENTRYPOINT" /bin/sh -c 'exit 1'
}

run_entrypoint_as_root() {
	config_dir="$1"
	data_dir="$2"
	managed_dir="$3"
	db_path="$data_dir/omnora.db"
	PATH="$fake_bin:$PATH" \
	CHOWN_RECORD="$chown_record" \
	CHOWN_FAIL_PATH=/mnt/omnora \
	MKDIR_RECORD="$mkdir_record" \
	SU_EXEC_RECORD="$su_exec_record" \
	PUID=2000 \
	PGID=2000 \
	OMNORA_PRIVILEGE_DROPPED=1 \
	OMNORA_CONFIG_DIR="$config_dir" \
	OMNORA_DATA_DIR="$data_dir" \
	OMNORA_DB_PATH="$db_path" \
	OMNORA_MANAGED_STORAGE_DIR="$managed_dir" \
	OMNORA_INITIALIZATION_TOKEN= \
	OMNORA_TOTP_ENCRYPTION_KEY= \
	OMNORA_AUDIT_HMAC_KEY= \
	sh "$ENTRYPOINT" /bin/sh -c 'printf root-ready'
}

fake_bin="$TMP_ROOT/fake-bin"
chown_record="$TMP_ROOT/chown-record"
mkdir_record="$TMP_ROOT/mkdir-record"
su_exec_record="$TMP_ROOT/su-exec-record"
mkdir -p "$fake_bin"

cat > "$fake_bin/id" <<'EOF'
#!/bin/sh
case "${1:-}" in
	-u)
		if [ "${FAKE_ID_DROPPED:-}" = 1 ]; then
			printf '1000\n'
		else
			printf '0\n'
		fi
		;;
	-g) printf '0\n' ;;
	*) exec /usr/bin/id "$@" ;;
esac
EOF

cat > "$fake_bin/chown" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$CHOWN_RECORD"
case " $* " in
	*" ${CHOWN_FAIL_PATH:-/path-that-never-matches} "*) exit 1 ;;
esac
EOF

cat > "$fake_bin/mkdir" <<'EOF'
#!/bin/sh
if [ "${FAKE_ID_DROPPED:-}" = 1 ]; then
	printf 'user:%s\n' "$*" >> "$MKDIR_RECORD"
else
	printf 'root:%s\n' "$*" >> "$MKDIR_RECORD"
fi
EOF

cat > "$fake_bin/su-exec" <<'EOF'
#!/bin/sh
printf '%s\n' "$1" > "$SU_EXEC_RECORD"
shift
export FAKE_ID_DROPPED=1
exec /bin/sh "$@"
EOF

chmod 755 "$fake_bin/id" "$fake_bin/chown" "$fake_bin/mkdir" "$fake_bin/su-exec"

root_bad_config_dir="$TMP_ROOT/root-bad-config"
root_bad_data_dir="$TMP_ROOT/root-bad-data"
root_bad_managed_dir="$TMP_ROOT/root-bad-managed"
mkdir -p "$root_bad_config_dir" "$root_bad_data_dir" "$root_bad_managed_dir"
printf '%s\n' aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa > "$root_bad_config_dir/.omnora-instance-id"
printf '%s\n' aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa > "$root_bad_data_dir/.omnora-instance-id"
printf '%s\n' bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb > "$root_bad_managed_dir/.omnora-instance-id"
printf 'OMNORA_INITIALIZATION_TOKEN=existing-init-token\nOMNORA_TOTP_ENCRYPTION_KEY=existing-totp-key\nOMNORA_AUDIT_HMAC_KEY=existing-audit-key\n' > "$root_bad_config_dir/runtime.env"
: > "$root_bad_data_dir/omnora.db"
: > "$chown_record"
: > "$su_exec_record"
if run_entrypoint_as_root "$root_bad_config_dir" "$root_bad_data_dir" "$root_bad_managed_dir" >/dev/null 2>&1; then
	printf 'FAIL: root entrypoint accepted mismatched persistent volumes\n' >&2
	exit 1
fi
if [ -s "$chown_record" ] || [ -s "$su_exec_record" ]; then
	printf 'FAIL: root entrypoint changed ownership before rejecting mismatched persistent volumes\n' >&2
	exit 1
fi

root_config_dir="$TMP_ROOT/root-config"
root_data_dir="$TMP_ROOT/root-data"
root_managed_dir="$TMP_ROOT/root-managed"
root_stderr="$TMP_ROOT/root-stderr"
mkdir -p "$root_config_dir" "$root_data_dir" "$root_managed_dir"
: > "$chown_record"
: > "$mkdir_record"
: > "$su_exec_record"

[ "$(run_entrypoint_as_root "$root_config_dir" "$root_data_dir" "$root_managed_dir" 2>"$root_stderr")" = root-ready ] || exit 1
if [ ! -s "$su_exec_record" ]; then
	printf 'FAIL: root entrypoint did not drop privileges before persistence setup\n' >&2
	exit 1
fi
[ "$(sed -n '1p' "$su_exec_record")" = 1000:1000 ] || exit 1
grep -Fx '1000:1000 /etc/omnora /var/lib/omnora /srv/omnora/managed' "$chown_record" >/dev/null || {
	printf 'FAIL: root entrypoint chowned configurable paths instead of fixed persistence roots\n' >&2
	exit 1
}
grep -Fx '1000:1000 /mnt/omnora' "$chown_record" >/dev/null || {
	printf 'FAIL: root entrypoint did not prepare the default external mount root for UID 1000\n' >&2
	exit 1
}
grep -Fq 'Omnora warning: cannot prepare external mount root ownership at /mnt/omnora' "$root_stderr" || {
	printf 'FAIL: root entrypoint did not warn when external mount ownership preparation failed\n' >&2
	exit 1
}
grep -Fx 'root:-p /etc/omnora /var/lib/omnora /srv/omnora/managed' "$mkdir_record" >/dev/null || {
	printf 'FAIL: root entrypoint created configurable paths instead of fixed persistence roots\n' >&2
	exit 1
}

config_dir="$TMP_ROOT/config"
data_dir="$TMP_ROOT/data"
managed_dir="$TMP_ROOT/managed"
db_path="$data_dir/omnora.db"
mkdir -p "$config_dir" "$data_dir" "$managed_dir"

runtime_stderr="$TMP_ROOT/runtime-stderr"
[ "$(run_entrypoint "$config_dir" "$data_dir" "$managed_dir" 2>"$runtime_stderr")" = ready ] || exit 1
cp "$config_dir/runtime.env" "$TMP_ROOT/runtime.env.before"
token_before=$(sed -n 's/^OMNORA_INITIALIZATION_TOKEN=//p' "$config_dir/runtime.env")
audit_before=$(sed -n 's/^OMNORA_AUDIT_HMAC_KEY=//p' "$config_dir/runtime.env")
config_instance=$(sed -n '1p' "$config_dir/.omnora-instance-id")
data_instance=$(sed -n '1p' "$data_dir/.omnora-instance-id")
managed_instance=$(sed -n '1p' "$managed_dir/.omnora-instance-id")
[ -n "$token_before" ] || exit 1
[ -n "$audit_before" ] || exit 1
[ "$(grep -c '^OMNORA_AUDIT_HMAC_KEY=' "$config_dir/runtime.env")" = 1 ] || exit 1
[ "${#config_instance}" = 64 ] || exit 1
case "$config_instance" in
	*[!0-9a-f]*) exit 1 ;;
esac
[ "$config_instance" = "$data_instance" ] || exit 1
[ "$config_instance" = "$managed_instance" ] || exit 1
if grep -Fq 'Initial setup token:' "$runtime_stderr" || grep -Fq "$token_before" "$runtime_stderr" || grep -Fq "$audit_before" "$runtime_stderr"; then
	printf 'FAIL: generated initialization token was written to entrypoint stderr\n' >&2
	exit 1
fi
grep -Fq "Omnora generated runtime secrets in $config_dir/runtime.env" "$runtime_stderr" || {
	printf 'FAIL: entrypoint did not report the protected runtime secrets path\n' >&2
	exit 1
}
: > "$db_path"

[ "$(run_entrypoint "$config_dir" "$data_dir" "$managed_dir")" = ready ] || exit 1
token_after=$(sed -n 's/^OMNORA_INITIALIZATION_TOKEN=//p' "$config_dir/runtime.env")
[ "$token_before" = "$token_after" ] || exit 1
[ "$(sed -n 's/^OMNORA_AUDIT_HMAC_KEY=//p' "$config_dir/runtime.env")" = "$audit_before" ] || exit 1
cmp -s "$TMP_ROOT/runtime.env.before" "$config_dir/runtime.env" || {
	printf 'FAIL: second startup rewrote the complete runtime secrets file\n' >&2
	exit 1
}
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

# Upgrade an instance whose old entrypoint persisted only initialization and
# TOTP keys. The upgraded entrypoint must append exactly one audit key while
# preserving the existing bytes and then reuse it on the next start.
old_config_dir="$TMP_ROOT/old-config"
old_data_dir="$TMP_ROOT/old-data"
old_managed_dir="$TMP_ROOT/old-managed"
old_db_path="$old_data_dir/omnora.db"
mkdir -p "$old_config_dir" "$old_data_dir" "$old_managed_dir"
old_instance_id=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
printf '%s\n' "$old_instance_id" > "$old_config_dir/.omnora-instance-id"
printf '%s\n' "$old_instance_id" > "$old_data_dir/.omnora-instance-id"
printf '%s\n' "$old_instance_id" > "$old_managed_dir/.omnora-instance-id"
printf 'OMNORA_INITIALIZATION_TOKEN=existing-init-token\nOMNORA_TOTP_ENCRYPTION_KEY=existing-totp-key\n' > "$old_config_dir/runtime.env"
: > "$old_db_path"
cp "$old_config_dir/runtime.env" "$TMP_ROOT/old-runtime.before"
env -u OMNORA_INITIALIZATION_TOKEN -u OMNORA_TOTP_ENCRYPTION_KEY -u OMNORA_AUDIT_HMAC_KEY \
	OMNORA_CONFIG_DIR="$old_config_dir" OMNORA_DATA_DIR="$old_data_dir" \
	OMNORA_DB_PATH="$old_db_path" OMNORA_MANAGED_STORAGE_DIR="$old_managed_dir" \
	sh "$ENTRYPOINT" /bin/sh -c 'test -n "$OMNORA_AUDIT_HMAC_KEY"' || exit 1
head -n 2 "$old_config_dir/runtime.env" | cmp -s - "$TMP_ROOT/old-runtime.before" || {
	printf 'FAIL: upgrade changed the original runtime secret lines\n' >&2
	exit 1
}
[ "$(grep -c '^OMNORA_AUDIT_HMAC_KEY=' "$old_config_dir/runtime.env")" = 1 ] || exit 1
old_audit=$(sed -n 's/^OMNORA_AUDIT_HMAC_KEY=//p' "$old_config_dir/runtime.env")
[ -n "$old_audit" ] || exit 1
cp "$old_config_dir/runtime.env" "$TMP_ROOT/old-runtime.after"
AUDIT_EXPECTED="$old_audit" env -u OMNORA_INITIALIZATION_TOKEN -u OMNORA_TOTP_ENCRYPTION_KEY -u OMNORA_AUDIT_HMAC_KEY \
	OMNORA_CONFIG_DIR="$old_config_dir" OMNORA_DATA_DIR="$old_data_dir" \
	OMNORA_DB_PATH="$old_db_path" OMNORA_MANAGED_STORAGE_DIR="$old_managed_dir" \
	sh "$ENTRYPOINT" /bin/sh -c '[ "$OMNORA_AUDIT_HMAC_KEY" = "$AUDIT_EXPECTED" ]' || exit 1
cmp -s "$TMP_ROOT/old-runtime.after" "$old_config_dir/runtime.env" || {
	printf 'FAIL: upgraded startup changed the runtime secrets file\n' >&2
	exit 1
}

# A failed atomic install must not truncate or partially replace the original
# file. The fake mv only rejects the validated runtime temporary path.
mv_fail_bin="$TMP_ROOT/mv-fail-bin"
mkdir -p "$mv_fail_bin"
cat > "$mv_fail_bin/mv" <<'EOF'
#!/bin/sh
case "${1:-}" in
	*/runtime.env.tmp.*) exit 1 ;;
	*) exec /bin/mv "$@" ;;
esac
EOF
chmod 755 "$mv_fail_bin/mv"
mvfail_config_dir="$TMP_ROOT/mvfail-config"
mvfail_data_dir="$TMP_ROOT/mvfail-data"
mvfail_managed_dir="$TMP_ROOT/mvfail-managed"
mvfail_db_path="$mvfail_data_dir/omnora.db"
mkdir -p "$mvfail_config_dir" "$mvfail_data_dir" "$mvfail_managed_dir"
mvfail_instance_id=dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd
printf '%s\n' "$mvfail_instance_id" > "$mvfail_config_dir/.omnora-instance-id"
printf '%s\n' "$mvfail_instance_id" > "$mvfail_data_dir/.omnora-instance-id"
printf '%s\n' "$mvfail_instance_id" > "$mvfail_managed_dir/.omnora-instance-id"
printf 'OMNORA_INITIALIZATION_TOKEN=existing-init-token\nOMNORA_TOTP_ENCRYPTION_KEY=existing-totp-key\n' > "$mvfail_config_dir/runtime.env"
: > "$mvfail_db_path"
cp "$mvfail_config_dir/runtime.env" "$TMP_ROOT/mvfail-runtime.before"
if env -u OMNORA_INITIALIZATION_TOKEN -u OMNORA_TOTP_ENCRYPTION_KEY -u OMNORA_AUDIT_HMAC_KEY \
	PATH="$mv_fail_bin:$PATH" OMNORA_CONFIG_DIR="$mvfail_config_dir" OMNORA_DATA_DIR="$mvfail_data_dir" \
	OMNORA_DB_PATH="$mvfail_db_path" OMNORA_MANAGED_STORAGE_DIR="$mvfail_managed_dir" \
	sh "$ENTRYPOINT" /bin/sh -c ':' >/dev/null 2>&1; then
	printf 'FAIL: injected runtime secrets install failure unexpectedly succeeded\n' >&2
	exit 1
fi
cmp -s "$TMP_ROOT/mvfail-runtime.before" "$mvfail_config_dir/runtime.env" || {
	printf 'FAIL: failed runtime secrets install changed the original file\n' >&2
	exit 1
}
if find "$mvfail_config_dir" -maxdepth 1 -name 'runtime.env.tmp.*' -print -quit | grep -q .; then
	printf 'FAIL: failed runtime secrets install left a temporary file\n' >&2
	exit 1
fi

retry_config_dir="$TMP_ROOT/retry-config"
retry_data_dir="$TMP_ROOT/retry-data"
retry_managed_dir="$TMP_ROOT/retry-managed"
retry_db_path="$retry_data_dir/omnora.db"
mkdir -p "$retry_config_dir" "$retry_data_dir" "$retry_managed_dir"
if run_entrypoint_with_failure "$retry_config_dir" "$retry_data_dir" "$retry_managed_dir" >/dev/null 2>&1; then
	printf 'FAIL: failed first process unexpectedly succeeded\n' >&2
	exit 1
fi
if [ -e "$retry_config_dir/.omnora-instance-id" ] || [ -e "$retry_data_dir/.omnora-instance-id" ] ||
	[ -e "$retry_managed_dir/.omnora-instance-id" ]; then
	printf 'FAIL: failed first process left an instance marker before database creation\n' >&2
	exit 1
fi

OMNORA_CONFIG_DIR="$retry_config_dir" \
OMNORA_DATA_DIR="$retry_data_dir" \
OMNORA_DB_PATH="$retry_db_path" \
OMNORA_MANAGED_STORAGE_DIR="$retry_managed_dir" \
OMNORA_INITIALIZATION_TOKEN= \
OMNORA_TOTP_ENCRYPTION_KEY= \
sh "$ENTRYPOINT" /bin/sh -c ': > "$OMNORA_DB_PATH"'
[ -f "$retry_config_dir/.omnora-instance-id" ] || exit 1
[ -f "$retry_data_dir/.omnora-instance-id" ] || exit 1
[ -f "$retry_managed_dir/.omnora-instance-id" ] || exit 1
[ -f "$retry_db_path" ] || exit 1

find_fail_bin="$TMP_ROOT/find-fail-bin"
mkdir -p "$find_fail_bin"
cat > "$find_fail_bin/find" <<'EOF'
#!/bin/sh
exit 73
EOF
chmod 755 "$find_fail_bin/find"
find_fail_config="$TMP_ROOT/find-fail-config"
find_fail_data="$TMP_ROOT/find-fail-data"
find_fail_managed="$TMP_ROOT/find-fail-managed"
mkdir -p "$find_fail_config" "$find_fail_data" "$find_fail_managed"
if PATH="$find_fail_bin:$PATH" OMNORA_CONFIG_DIR="$find_fail_config" OMNORA_DATA_DIR="$find_fail_data" \
	OMNORA_DB_PATH="$find_fail_data/omnora.db" OMNORA_MANAGED_STORAGE_DIR="$find_fail_managed" \
	OMNORA_INITIALIZATION_TOKEN= OMNORA_TOTP_ENCRYPTION_KEY= OMNORA_AUDIT_HMAC_KEY= \
	sh "$ENTRYPOINT" /bin/sh -c 'printf unexpected' >/dev/null 2>&1; then
	printf 'FAIL: directory enumeration failure was treated as an empty fresh instance\n' >&2
	exit 1
fi
[ ! -e "$find_fail_config/.omnora-instance-id" ] && [ ! -e "$find_fail_data/.omnora-instance-id" ] &&
	[ ! -e "$find_fail_managed/.omnora-instance-id" ] || {
	printf 'FAIL: directory enumeration failure created instance markers\n' >&2
	exit 1
}

temp_symlink_bin="$TMP_ROOT/temp-symlink-bin"
mkdir -p "$temp_symlink_bin"
cat > "$temp_symlink_bin/mktemp" <<'EOF'
#!/bin/sh
ln -s "$PREDICTABLE_MARKER_VICTIM" "$PREDICTABLE_MARKER_TEMP"
printf '%s\n' "$PREDICTABLE_MARKER_TEMP"
EOF
chmod 755 "$temp_symlink_bin/mktemp"
temp_symlink_config="$TMP_ROOT/temp-symlink-config"
temp_symlink_data="$TMP_ROOT/temp-symlink-data"
temp_symlink_managed="$TMP_ROOT/temp-symlink-managed"
temp_symlink_victim="$TMP_ROOT/temp-symlink-victim"
predictable_marker_temp="$temp_symlink_config/.omnora-instance-id.tmp.predictable"
mkdir -p "$temp_symlink_config" "$temp_symlink_data" "$temp_symlink_managed"
printf 'must-not-change\n' > "$temp_symlink_victim"
if PATH="$temp_symlink_bin:$PATH" PREDICTABLE_MARKER_TEMP="$predictable_marker_temp" \
	PREDICTABLE_MARKER_VICTIM="$temp_symlink_victim" \
	OMNORA_CONFIG_DIR="$temp_symlink_config" OMNORA_DATA_DIR="$temp_symlink_data" \
	OMNORA_DB_PATH="$temp_symlink_data/omnora.db" OMNORA_MANAGED_STORAGE_DIR="$temp_symlink_managed" \
	OMNORA_INITIALIZATION_TOKEN= OMNORA_TOTP_ENCRYPTION_KEY= OMNORA_AUDIT_HMAC_KEY= \
	sh "$ENTRYPOINT" /bin/sh -c 'printf unexpected' >/dev/null 2>&1; then
	printf 'FAIL: predictable marker temporary symlink was accepted\n' >&2
	exit 1
fi
[ "$(sed -n '1p' "$temp_symlink_victim")" = must-not-change ] || {
	printf 'FAIL: predictable marker temporary symlink target was truncated\n' >&2
	exit 1
}
[ ! -e "$predictable_marker_temp" ] && [ ! -L "$predictable_marker_temp" ] || {
	printf 'FAIL: rejected marker temporary symlink was not cleaned up\n' >&2
	exit 1
}

target_inject_bin="$TMP_ROOT/target-inject-bin"
mkdir -p "$target_inject_bin"
cat > "$target_inject_bin/ln" <<'EOF'
#!/bin/sh
for target_path do :; done
printf 'injected-concurrent\n' > "$target_path"
exec /bin/ln "$@"
EOF
chmod 755 "$target_inject_bin/ln"
target_inject_config="$TMP_ROOT/target-inject-config"
target_inject_data="$TMP_ROOT/target-inject-data"
target_inject_managed="$TMP_ROOT/target-inject-managed"
mkdir -p "$target_inject_config" "$target_inject_data" "$target_inject_managed"
if PATH="$target_inject_bin:$PATH" OMNORA_CONFIG_DIR="$target_inject_config" OMNORA_DATA_DIR="$target_inject_data" \
	OMNORA_DB_PATH="$target_inject_data/omnora.db" OMNORA_MANAGED_STORAGE_DIR="$target_inject_managed" \
	OMNORA_INITIALIZATION_TOKEN= OMNORA_TOTP_ENCRYPTION_KEY= OMNORA_AUDIT_HMAC_KEY= \
	sh "$ENTRYPOINT" /bin/sh -c 'printf unexpected' >/dev/null 2>&1; then
	printf 'FAIL: concurrent marker target creation was overwritten\n' >&2
	exit 1
fi
[ "$(sed -n '1p' "$target_inject_config/.omnora-instance-id")" = injected-concurrent ] || {
	printf 'FAIL: concurrent marker target content was changed\n' >&2
	exit 1
}
if find "$target_inject_config" -maxdepth 1 -name '.omnora-instance-id.tmp.*' -print -quit | grep -q .; then
	printf 'FAIL: target collision left a marker temporary file\n' >&2
	exit 1
fi

target_symlink_bin="$TMP_ROOT/target-symlink-bin"
target_symlink_count="$TMP_ROOT/target-symlink-count"
target_symlink_destination="$TMP_ROOT/target-symlink-destination"
mkdir -p "$target_symlink_bin" "$target_symlink_destination"
cat > "$target_symlink_bin/ln" <<'EOF'
#!/bin/sh
count=0
[ ! -f "$TARGET_SYMLINK_COUNT" ] || count=$(sed -n '1p' "$TARGET_SYMLINK_COUNT")
count=$((count + 1))
printf '%s\n' "$count" > "$TARGET_SYMLINK_COUNT"
for target_path do :; done
if [ "$count" -eq 1 ]; then
	/bin/ln -s "$TARGET_SYMLINK_DESTINATION" "$target_path"
fi
exec /bin/ln "$@"
EOF
chmod 755 "$target_symlink_bin/ln"
target_symlink_config="$TMP_ROOT/target-symlink-config"
target_symlink_data="$TMP_ROOT/target-symlink-data"
target_symlink_managed="$TMP_ROOT/target-symlink-managed"
mkdir -p "$target_symlink_config" "$target_symlink_data" "$target_symlink_managed"
if PATH="$target_symlink_bin:$PATH" TARGET_SYMLINK_COUNT="$target_symlink_count" \
	TARGET_SYMLINK_DESTINATION="$target_symlink_destination" \
	OMNORA_CONFIG_DIR="$target_symlink_config" OMNORA_DATA_DIR="$target_symlink_data" \
	OMNORA_DB_PATH="$target_symlink_data/omnora.db" OMNORA_MANAGED_STORAGE_DIR="$target_symlink_managed" \
	OMNORA_INITIALIZATION_TOKEN= OMNORA_TOTP_ENCRYPTION_KEY= OMNORA_AUDIT_HMAC_KEY= \
	sh "$ENTRYPOINT" /bin/sh -c 'printf unexpected' >/dev/null 2>&1; then
	printf 'FAIL: concurrent marker target symlink to a directory was followed\n' >&2
	exit 1
fi
[ -L "$target_symlink_config/.omnora-instance-id" ] || {
	printf 'FAIL: concurrent marker target symlink was overwritten\n' >&2
	exit 1
}
if find "$target_symlink_destination" -mindepth 1 -print -quit | grep -q .; then
	printf 'FAIL: marker hard link was created inside a concurrent target symlink directory\n' >&2
	exit 1
fi

partial_install_bin="$TMP_ROOT/partial-install-bin"
partial_install_count="$TMP_ROOT/partial-install-count"
mkdir -p "$partial_install_bin"
cat > "$partial_install_bin/ln" <<'EOF'
#!/bin/sh
count=0
[ ! -f "$PARTIAL_INSTALL_COUNT" ] || count=$(sed -n '1p' "$PARTIAL_INSTALL_COUNT")
count=$((count + 1))
printf '%s\n' "$count" > "$PARTIAL_INSTALL_COUNT"
[ "$count" -ne "$PARTIAL_INSTALL_FAIL_AT" ] || exit 74
exec /bin/ln "$@"
EOF
chmod 755 "$partial_install_bin/ln"
partial_install_config="$TMP_ROOT/partial-install-config"
partial_install_data="$TMP_ROOT/partial-install-data"
partial_install_managed="$TMP_ROOT/partial-install-managed"
mkdir -p "$partial_install_config" "$partial_install_data" "$partial_install_managed"
if PATH="$partial_install_bin:$PATH" PARTIAL_INSTALL_COUNT="$partial_install_count" PARTIAL_INSTALL_FAIL_AT=2 \
	OMNORA_CONFIG_DIR="$partial_install_config" OMNORA_DATA_DIR="$partial_install_data" \
	OMNORA_DB_PATH="$partial_install_data/omnora.db" OMNORA_MANAGED_STORAGE_DIR="$partial_install_managed" \
	OMNORA_INITIALIZATION_TOKEN= OMNORA_TOTP_ENCRYPTION_KEY= OMNORA_AUDIT_HMAC_KEY= \
	sh "$ENTRYPOINT" /bin/sh -c 'printf unexpected' >/dev/null 2>&1; then
	printf 'FAIL: injected second-volume marker install failure unexpectedly succeeded\n' >&2
	exit 1
fi
[ -f "$partial_install_config/.omnora-instance-id" ] || {
	printf 'FAIL: partial install removed the already published config marker\n' >&2
	exit 1
}
[ ! -e "$partial_install_data/.omnora-instance-id" ] && [ ! -e "$partial_install_managed/.omnora-instance-id" ] || {
	printf 'FAIL: partial install published markers after the injected failure\n' >&2
	exit 1
}
if find "$partial_install_config" "$partial_install_data" "$partial_install_managed" -name '.omnora-instance-id.tmp.*' -print -quit | grep -q .; then
	printf 'FAIL: partial marker install left a temporary file\n' >&2
	exit 1
fi
if run_entrypoint "$partial_install_config" "$partial_install_data" "$partial_install_managed" >/dev/null 2>&1; then
	printf 'FAIL: startup repaired a partially installed marker set\n' >&2
	exit 1
fi

third_install_count="$TMP_ROOT/third-install-count"
third_install_config="$TMP_ROOT/third-install-config"
third_install_data="$TMP_ROOT/third-install-data"
third_install_managed="$TMP_ROOT/third-install-managed"
mkdir -p "$third_install_config" "$third_install_data" "$third_install_managed"
if PATH="$partial_install_bin:$PATH" PARTIAL_INSTALL_COUNT="$third_install_count" PARTIAL_INSTALL_FAIL_AT=3 \
	OMNORA_CONFIG_DIR="$third_install_config" OMNORA_DATA_DIR="$third_install_data" \
	OMNORA_DB_PATH="$third_install_data/omnora.db" OMNORA_MANAGED_STORAGE_DIR="$third_install_managed" \
	OMNORA_INITIALIZATION_TOKEN= OMNORA_TOTP_ENCRYPTION_KEY= OMNORA_AUDIT_HMAC_KEY= \
	sh "$ENTRYPOINT" /bin/sh -c 'printf unexpected' >/dev/null 2>&1; then
	printf 'FAIL: injected third-volume marker install failure unexpectedly succeeded\n' >&2
	exit 1
fi
[ -f "$third_install_config/.omnora-instance-id" ] && [ -f "$third_install_data/.omnora-instance-id" ] || {
	printf 'FAIL: third-volume failure removed an already published marker\n' >&2
	exit 1
}
[ ! -e "$third_install_managed/.omnora-instance-id" ] || {
	printf 'FAIL: third-volume failure published the managed marker\n' >&2
	exit 1
}
if find "$third_install_config" "$third_install_data" "$third_install_managed" -name '.omnora-instance-id.tmp.*' -print -quit | grep -q .; then
	printf 'FAIL: third-volume marker failure left a temporary file\n' >&2
	exit 1
fi
if run_entrypoint "$third_install_config" "$third_install_data" "$third_install_managed" >/dev/null 2>&1; then
	printf 'FAIL: startup repaired a marker set missing the managed marker\n' >&2
	exit 1
fi

existing_id=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa

missing_managed_config="$TMP_ROOT/missing-managed-config"
missing_managed_data="$TMP_ROOT/missing-managed-data"
missing_managed_root="$TMP_ROOT/missing-managed-root"
mkdir -p "$missing_managed_config" "$missing_managed_data" "$missing_managed_root"
printf '%s\n' "$existing_id" > "$missing_managed_config/.omnora-instance-id"
printf '%s\n' "$existing_id" > "$missing_managed_data/.omnora-instance-id"
printf 'OMNORA_INITIALIZATION_TOKEN=existing-init-token\nOMNORA_TOTP_ENCRYPTION_KEY=existing-totp-key\nOMNORA_AUDIT_HMAC_KEY=existing-audit-key\n' > "$missing_managed_config/runtime.env"
: > "$missing_managed_data/omnora.db"
if run_entrypoint "$missing_managed_config" "$missing_managed_data" "$missing_managed_root" >/dev/null 2>&1; then
	printf 'FAIL: existing config/data without a managed marker was accepted\n' >&2
	exit 1
fi
[ ! -e "$missing_managed_root/.omnora-instance-id" ] || {
	printf 'FAIL: entrypoint silently adopted a managed volume for an existing instance\n' >&2
	exit 1
}

mismatch_config="$TMP_ROOT/mismatch-config"
mismatch_data="$TMP_ROOT/mismatch-data"
mismatch_managed="$TMP_ROOT/mismatch-managed"
mkdir -p "$mismatch_config" "$mismatch_data" "$mismatch_managed"
printf '%s\n' "$existing_id" > "$mismatch_config/.omnora-instance-id"
printf '%s\n' "$existing_id" > "$mismatch_data/.omnora-instance-id"
printf '%s\n' bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb > "$mismatch_managed/.omnora-instance-id"
printf 'OMNORA_INITIALIZATION_TOKEN=existing-init-token\nOMNORA_TOTP_ENCRYPTION_KEY=existing-totp-key\nOMNORA_AUDIT_HMAC_KEY=existing-audit-key\n' > "$mismatch_config/runtime.env"
: > "$mismatch_data/omnora.db"
if run_entrypoint "$mismatch_config" "$mismatch_data" "$mismatch_managed" >/dev/null 2>&1; then
	printf 'FAIL: mismatched managed marker was accepted\n' >&2
	exit 1
fi

partial_config="$TMP_ROOT/partial-config"
partial_data="$TMP_ROOT/partial-data"
partial_managed="$TMP_ROOT/partial-managed"
mkdir -p "$partial_config" "$partial_data" "$partial_managed"
printf '%s\n' "$existing_id" > "$partial_config/.omnora-instance-id"
if run_entrypoint "$partial_config" "$partial_data" "$partial_managed" >/dev/null 2>&1; then
	printf 'FAIL: a partially created marker set was accepted\n' >&2
	exit 1
fi
[ ! -e "$partial_data/.omnora-instance-id" ] && [ ! -e "$partial_managed/.omnora-instance-id" ] || {
	printf 'FAIL: entrypoint repaired a partially created marker set\n' >&2
	exit 1
}

unmarked_config="$TMP_ROOT/unmarked-config"
unmarked_data="$TMP_ROOT/unmarked-data"
unmarked_managed="$TMP_ROOT/unmarked-managed"
mkdir -p "$unmarked_config" "$unmarked_data" "$unmarked_managed"
printf 'managed content\n' > "$unmarked_managed/existing-file"
if run_entrypoint "$unmarked_config" "$unmarked_data" "$unmarked_managed" >/dev/null 2>&1; then
	printf 'FAIL: a non-empty unmarked managed volume was silently adopted\n' >&2
	exit 1
fi
[ ! -e "$unmarked_config/.omnora-instance-id" ] && [ ! -e "$unmarked_data/.omnora-instance-id" ] &&
	[ ! -e "$unmarked_managed/.omnora-instance-id" ] || {
	printf 'FAIL: entrypoint wrote markers while rejecting an unmarked managed volume\n' >&2
	exit 1
}

symlink_config="$TMP_ROOT/symlink-config"
symlink_data="$TMP_ROOT/symlink-data"
symlink_managed="$TMP_ROOT/symlink-managed"
mkdir -p "$symlink_config" "$symlink_data" "$symlink_managed"
printf '%s\n' "$existing_id" > "$symlink_config/.omnora-instance-id"
printf '%s\n' "$existing_id" > "$symlink_data/.omnora-instance-id"
ln -s "$symlink_data/.omnora-instance-id" "$symlink_managed/.omnora-instance-id"
printf 'OMNORA_INITIALIZATION_TOKEN=existing-init-token\nOMNORA_TOTP_ENCRYPTION_KEY=existing-totp-key\nOMNORA_AUDIT_HMAC_KEY=existing-audit-key\n' > "$symlink_config/runtime.env"
: > "$symlink_data/omnora.db"
if run_entrypoint "$symlink_config" "$symlink_data" "$symlink_managed" >/dev/null 2>&1; then
	printf 'FAIL: a symbolic-link marker was accepted\n' >&2
	exit 1
fi

single_fd_bin="$TMP_ROOT/single-fd-bin"
mkdir -p "$single_fd_bin"
for command_name in wc tail tr; do
	cat > "$single_fd_bin/$command_name" <<'EOF'
#!/bin/sh
exit 75
EOF
	chmod 755 "$single_fd_bin/$command_name"
done
if ! PATH="$single_fd_bin:$PATH" OMNORA_CONFIG_DIR="$old_config_dir" OMNORA_DATA_DIR="$old_data_dir" \
	OMNORA_DB_PATH="$old_db_path" OMNORA_MANAGED_STORAGE_DIR="$old_managed_dir" \
	OMNORA_INITIALIZATION_TOKEN= OMNORA_TOTP_ENCRYPTION_KEY= OMNORA_AUDIT_HMAC_KEY= \
	sh "$ENTRYPOINT" /bin/sh -c 'printf single-fd-ready' | grep -Fx single-fd-ready >/dev/null; then
	printf 'FAIL: marker validation reopened marker paths through wc/tail/tr\n' >&2
	exit 1
fi

new_data_dir="$TMP_ROOT/new-data"
mkdir -p "$new_data_dir"
if run_entrypoint "$config_dir" "$new_data_dir" "$managed_dir" >/dev/null 2>&1; then
	printf 'FAIL: mismatched config/data bind mounts were accepted\n' >&2
	exit 1
fi

printf 'ok: Docker entrypoint preserves runtime secrets and rejects mismatched persistent mounts\n'
