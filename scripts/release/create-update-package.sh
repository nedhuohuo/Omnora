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

ROOT_DIR=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
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
		go build -trimpath -ldflags='-s -w' -o "$BIN_DIR/omnora" ./cmd/omnora
	CGO_ENABLED=0 GOOS="${GOOS:-linux}" GOARCH="${GOARCH:-$(go env GOARCH)}" \
		go build -trimpath -ldflags='-s -w' -o "$BIN_DIR/omnora-recovery" ./cmd/omnora-recovery
)

APP_SHA=$(hash_file "$BIN_DIR/omnora")
APP_SIZE=$(size_file "$BIN_DIR/omnora")
RECOVERY_SHA=$(hash_file "$BIN_DIR/omnora-recovery")
RECOVERY_SIZE=$(size_file "$BIN_DIR/omnora-recovery")
TARGET_OS="${GOOS:-linux}"
TARGET_ARCH="${GOARCH:-$(go env GOARCH)}"
printf '%s\n' "{\"formatVersion\":1,\"version\":\"$VERSION\",\"targetOS\":\"$TARGET_OS\",\"targetArch\":\"$TARGET_ARCH\",\"artifacts\":{\"omnora\":{\"path\":\"omnora\",\"sha256\":\"$APP_SHA\",\"size\":$APP_SIZE},\"omnora-recovery\":{\"path\":\"omnora-recovery\",\"sha256\":\"$RECOVERY_SHA\",\"size\":$RECOVERY_SIZE}}}" > "$BIN_DIR/manifest.json"

mkdir -p "$(dirname "$OUTPUT_PATH")"
tar -czf "$OUTPUT_PATH" -C "$BIN_DIR" manifest.json omnora omnora-recovery
printf 'Created %s (%s/%s)\n' "$OUTPUT_PATH" "$TARGET_OS" "$TARGET_ARCH"
