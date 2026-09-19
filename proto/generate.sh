#!/bin/sh
set -eu

# Keep generation reproducible without depending on a globally installed plugin.
protoc_version=36.1
plugin_version=v1.34.2
module_path=github.com/Takapu-Labs/takapu-protocol-sdk
schema=marketstream.proto
output=pkg/types/marketstream.pb.go

usage() {
    printf '%s\n' "Usage: $0 [--check]" \
        "Generate checked-in Go bindings, or check that they are up to date." \
        "Requires protoc $protoc_version (override its path with PROTOC)." \
        "Installs protoc-gen-go $plugin_version into .bin/ on first use."
}

check=false
if [ "$#" -gt 1 ]; then
    usage >&2
    exit 2
fi
case "${1-}" in
    "") ;;
    --check) check=true ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
esac

module_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$module_root"

protoc_bin=${PROTOC:-protoc}
if ! actual_version=$("$protoc_bin" --version); then
    printf '%s\n' "Install protoc $protoc_version or set PROTOC to its executable." >&2
    exit 1
fi
if [ "$actual_version" != "libprotoc $protoc_version" ]; then
    printf '%s\n' "Expected protoc $protoc_version; found $actual_version." >&2
    exit 1
fi

tool_dir="$module_root/.bin/protoc-gen-go/$plugin_version"
plugin="$tool_dir/protoc-gen-go"
if [ ! -x "$plugin" ]; then
    mkdir -p "$tool_dir"
    GOBIN="$tool_dir" GOWORK=off go install "google.golang.org/protobuf/cmd/protoc-gen-go@$plugin_version"
fi
if [ "$("$plugin" --version)" != "protoc-gen-go $plugin_version" ]; then
    printf '%s\n' "Unexpected plugin version at $plugin; remove it and rerun." >&2
    exit 1
fi

temporary_dir=$(mktemp -d "$module_root/.bin/proto-generation.XXXXXX")
trap 'rm -rf "$temporary_dir"' EXIT
trap 'exit 1' HUP INT TERM

"$protoc_bin" -I proto \
    --plugin="protoc-gen-go=$plugin" \
    --go_out="$temporary_dir" \
    --go_opt="module=$module_path" \
    "proto/$schema"

if [ "$check" = true ]; then
    if ! cmp -s "$output" "$temporary_dir/$output"; then
        printf '%s\n' "Generated bindings are out of date. Run make generate." >&2
        diff -u "$output" "$temporary_dir/$output" || true
        exit 1
    fi
    printf '%s\n' "Generated bindings are up to date."
else
    mkdir -p "$(dirname -- "$output")"
    mv -f "$temporary_dir/$output" "$output"
    printf '%s\n' "Generated $output"
fi
