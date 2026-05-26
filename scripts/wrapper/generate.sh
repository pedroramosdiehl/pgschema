lij#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"
PROPERTIES_FILE="$REPO_ROOT/.pgschema-wrapper.properties"
WRAPPER_SH="$REPO_ROOT/pgschemaw"
WRAPPER_PS1="$REPO_ROOT/pgschemaw.ps1"

print_help() {
  cat <<'EOF'
Usage:
  ./scripts/wrapper/generate.sh --version <x.y.z> [--source remote|local] [--download-now]

Options:
  --version <x.y.z>  Required wrapper version to pin.
  --source <mode>    Wrapper source mode: remote or local (default: remote).
  --download-now     Download binary immediately after generating config.
  --help             Show this help.
EOF
}

VERSION=""
DOWNLOAD_NOW="false"
SOURCE_MODE="remote"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      shift
      VERSION="${1:-}"
      ;;
    --download-now)
      DOWNLOAD_NOW="true"
      ;;
    --source)
      shift
      SOURCE_MODE="${1:-}"
      ;;
    --help|-h)
      print_help
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      print_help
      exit 1
      ;;
  esac
  shift || true
done

if [[ -z "$VERSION" ]]; then
  echo "--version is required." >&2
  print_help
  exit 1
fi

VERSION="${VERSION#v}"
if [[ "$SOURCE_MODE" != "remote" && "$SOURCE_MODE" != "local" ]]; then
  echo "--source must be 'remote' or 'local'." >&2
  exit 1
fi

if [[ ! -f "$WRAPPER_SH" || ! -f "$WRAPPER_PS1" ]]; then
  echo "Wrapper runners are missing. Expected files:" >&2
  echo "  - $WRAPPER_SH" >&2
  echo "  - $WRAPPER_PS1" >&2
  exit 1
fi

cat > "$PROPERTIES_FILE" <<EOF
# pgschema wrapper configuration
pgschema.version=${VERSION}
pgschema.source=${SOURCE_MODE}
pgschema.baseUrl=https://github.com/pgplex/pgschema/releases/download
pgschema.cacheDir=.pgschema/wrapper/bin

# Optional checksums by target (enable when you want strict verification):
# pgschema.sha256.linux-amd64=
# pgschema.sha256.linux-arm64=
# pgschema.sha256.darwin-amd64=
# pgschema.sha256.darwin-arm64=
EOF

chmod +x "$WRAPPER_SH"
chmod +x "$SCRIPT_DIR/generate.sh"

echo "Updated $PROPERTIES_FILE (version=$VERSION, source=$SOURCE_MODE)"
echo "Wrapper runners are ready:"
echo "  ./pgschemaw"
echo "  ./pgschemaw.ps1"

if [[ "$DOWNLOAD_NOW" == "true" ]]; then
  echo "Preparing wrapper binary..."
  "$WRAPPER_SH" --version
fi
