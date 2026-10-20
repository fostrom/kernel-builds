version := `cat VERSION`
commit := `git rev-parse --verify -q HEAD || echo unknown`
epoch := `git log -1 --format=%ct 2>/dev/null || echo 0`
export VERSION := version
export COMMIT := commit
export SOURCE_DATE_EPOCH := epoch

_default:
    @just --list --unsorted

# Build every target or [amd64, arm64, rpi] individually, into .release/
build *targets:
    docker buildx bake {{targets}}
