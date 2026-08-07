#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
ENTRYPOINT="$ROOT/deploy/docker-entrypoint.sh"
COMPOSE="$ROOT/deploy/docker-compose.yml"
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

root_config_dir="$TMP_ROOT/root-config"
root_data_dir="$TMP_ROOT/root-data"
root_managed_dir="$TMP_ROOT/root-managed"
root_stderr="$TMP_ROOT/root-stderr"
mkdir -p "$root_config_dir" "$root_data_dir" "$root_managed_dir"

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
[ -n "$token_before" ] || exit 1
[ -n "$audit_before" ] || exit 1
[ "$(grep -c '^OMNORA_AUDIT_HMAC_KEY=' "$config_dir/runtime.env")" = 1 ] || exit 1
[ "$config_instance" = "$data_instance" ] || exit 1
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
printf 'old-instance\n' > "$old_config_dir/.omnora-instance-id"
printf 'old-instance\n' > "$old_data_dir/.omnora-instance-id"
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
printf 'mvfail-instance\n' > "$mvfail_config_dir/.omnora-instance-id"
printf 'mvfail-instance\n' > "$mvfail_data_dir/.omnora-instance-id"
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
if [ -e "$retry_config_dir/.omnora-instance-id" ] || [ -e "$retry_data_dir/.omnora-instance-id" ]; then
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
[ -f "$retry_db_path" ] || exit 1

new_data_dir="$TMP_ROOT/new-data"
mkdir -p "$new_data_dir"
if run_entrypoint "$config_dir" "$new_data_dir" "$managed_dir" >/dev/null 2>&1; then
	printf 'FAIL: mismatched config/data bind mounts were accepted\n' >&2
	exit 1
fi

printf 'ok: Docker entrypoint preserves runtime secrets and rejects mismatched persistent mounts\n'
