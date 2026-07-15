#!/bin/sh
# Build a clean Linux test image. The source is copied into the image; no host
# directories, agent configuration, or Handshake database are mounted.
set -eu

ROOT_DIR=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
IMAGE=${HANDSHAKE_TEST_IMAGE:-handshake-test:local}

if ! command -v container >/dev/null 2>&1; then
	echo "Apple Container CLI is required. Install it, then run: container system start" >&2
	exit 1
fi

container build --tag "$IMAGE" --file "$ROOT_DIR/.container/Containerfile.test" "$ROOT_DIR"
container run --rm "$IMAGE"
