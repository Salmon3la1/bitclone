#!/bin/sh
# Build the ISO inside a privileged Debian container (needs Docker on Linux,
# or Docker Desktop / WSL2 on Windows). live-build needs loop devices and
# chroot, hence --privileged.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
docker run --rm --privileged -v "$ROOT:/src" -w /src debian:trixie sh scripts/build-iso.sh
