# Run these from inside the devcontainer (VS Code's integrated terminal
# once you've reopened in it) -- it has the Go toolchain, your host doesn't
# need to.
#
# Host-side operations (the scan-net demo stack) are plain podman-compose /
# podman network commands instead of Make targets -- see the README's
# "Setup order" section. Kalpa doesn't ship `make` by default, and there's
# no reason to require it just for the host side of this.

.PHONY: run build test tidy

tidy:
	go mod tidy

run:
	go run ./cmd/netwatch

build:
	go build -o bin/netwatch ./cmd/netwatch

test:
	go test ./...