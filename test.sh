#!/usr/bin/env bash
# Build the beacon-chain and validator OCI images for linux/amd64 and load them
# into the local Docker daemon.
#
# Note: `docker rmi ... || true` ignores the "No such image" error when the
# image isn't present yet, so it doesn't break the && chain.
set -euo pipefail

PLATFORM="@io_bazel_rules_go//go/toolchain:linux_amd64_cgo"

# Beacon chain
bazel build //cmd/beacon-chain:oci_image_tarball --platforms="${PLATFORM}" --config=release --nouse_action_cache
docker rmi gcr.io/offchainlabs/prysm/beacon-chain || true
docker load -i bazel-bin/cmd/beacon-chain/oci_image_tarball/tarball.tar

# Validator
bazel build //cmd/validator:oci_image_tarball --platforms="${PLATFORM}" --config=release --nouse_action_cache
docker rmi gcr.io/offchainlabs/prysm/validator || true
docker load -i bazel-bin/cmd/validator/oci_image_tarball/tarball.tar
