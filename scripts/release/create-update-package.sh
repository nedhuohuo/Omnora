#!/bin/sh
set -eu

VERSION="${1:?usage: $0 VERSION [OUTPUT_PATH]}"
OUTPUT_PATH="${2:-$PWD/omnora-update-$VERSION.tar.gz}"
case "$VERSION" in
	''|*[!A-Za-z0-9._-]*)
		echo "VERSION may contain only letters, digits, dot, underscore, and dash" >&2
		exit 2
		;;
esac
case "$VERSION" in
	[0-9]*|v[0-9]*) ;;
	*)
		echo "VERSION must start with a digit or v followed by a digit" >&2
		exit 2
		;;
esac

ROOT_DIR=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
SIGNING_KEY_FILE="${OMNORA_UPDATE_SIGNING_PRIVATE_KEY_FILE:?set OMNORA_UPDATE_SIGNING_PRIVATE_KEY_FILE to an Ed25519 private PEM key}"
[ -f "$SIGNING_KEY_FILE" ] || { echo "signing private key is not a regular file" >&2; exit 2; }
[ ! -L "$SIGNING_KEY_FILE" ] || { echo "signing private key must not be a symlink" >&2; exit 2; }
command -v openssl >/dev/null 2>&1 || { echo "openssl is required to sign update packages" >&2; exit 2; }
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/omnora-update.XXXXXX")
cleanup() {
	rm -rf "$WORK_DIR"
}
trap cleanup EXIT HUP INT TERM

hash_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
		return
	fi
	shasum -a 256 "$1" | awk '{print $1}'
}

size_file() {
	wc -c < "$1" | tr -d '[:space:]'
}

# Build the browser bundle first. The temporary source tree lets go:embed pick
# up production assets without changing the tracked placeholder files.
npm --prefix "$ROOT_DIR/web" run build
cp -R "$ROOT_DIR" "$WORK_DIR/source"
rm -rf "$WORK_DIR/source/.git" "$WORK_DIR/source/web/node_modules" "$WORK_DIR/source/web/dist"
cp -R "$ROOT_DIR/web/dist/." "$WORK_DIR/source/web/dist"
rm -rf "$WORK_DIR/source/internal/server/static"
mkdir -p "$WORK_DIR/source/internal/server/static"
cp -R "$ROOT_DIR/web/dist/." "$WORK_DIR/source/internal/server/static"

BIN_DIR="$WORK_DIR/bin"
mkdir -p "$BIN_DIR"
(
	cd "$WORK_DIR/source"
	CGO_ENABLED=0 GOOS="${GOOS:-linux}" GOARCH="${GOARCH:-$(go env GOARCH)}" \
		go build -trimpath -ldflags="-s -w -X omnora/internal/buildinfo.Version=$VERSION" -o "$BIN_DIR/omnora" ./cmd/omnora
	CGO_ENABLED=0 GOOS="${GOOS:-linux}" GOARCH="${GOARCH:-$(go env GOARCH)}" \
		go build -trimpath -ldflags='-s -w' -o "$BIN_DIR/omnora-recovery" ./cmd/omnora-recovery
)

APP_SHA=$(hash_file "$BIN_DIR/omnora")
APP_SIZE=$(size_file "$BIN_DIR/omnora")
RECOVERY_SHA=$(hash_file "$BIN_DIR/omnora-recovery")
RECOVERY_SIZE=$(size_file "$BIN_DIR/omnora-recovery")
TARGET_OS="${GOOS:-linux}"
TARGET_ARCH="${GOARCH:-$(go env GOARCH)}"
SCHEMA_VERSION=$(printf '%s\n' "$ROOT_DIR"/internal/store/migrations/[0-9][0-9][0-9]_*.sql | sed 's#.*/##; s/_.*//; s/^0*//' | sort -n | tail -1)
case "$SCHEMA_VERSION" in
	''|*[!0-9]*) echo "cannot determine the embedded database schema version" >&2; exit 2 ;;
esac
printf '%s\n' "{\"formatVersion\":1,\"version\":\"$VERSION\",\"targetOS\":\"$TARGET_OS\",\"targetArch\":\"$TARGET_ARCH\",\"schemaVersion\":$SCHEMA_VERSION,\"artifacts\":{\"omnora\":{\"path\":\"omnora\",\"sha256\":\"$APP_SHA\",\"size\":$APP_SIZE},\"omnora-recovery\":{\"path\":\"omnora-recovery\",\"sha256\":\"$RECOVERY_SHA\",\"size\":$RECOVERY_SIZE}}}" > "$BIN_DIR/manifest.json"
openssl pkeyutl -sign -rawin -inkey "$SIGNING_KEY_FILE" -in "$BIN_DIR/manifest.json" -out "$BIN_DIR/manifest.sig"
[ "$(size_file "$BIN_DIR/manifest.sig")" -eq 64 ] || { echo "Ed25519 signature must be exactly 64 bytes" >&2; exit 2; }

mkdir -p "$(dirname "$OUTPUT_PATH")"
tar -czf "$OUTPUT_PATH" -C "$BIN_DIR" manifest.json manifest.sig omnora omnora-recovery
printf 'Created %s (%s/%s)\n' "$OUTPUT_PATH" "$TARGET_OS" "$TARGET_ARCH"
