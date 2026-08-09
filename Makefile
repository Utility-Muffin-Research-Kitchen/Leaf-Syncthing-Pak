PYTHON ?= python3
GO ?= go
CC ?= cc
PLATFORM ?= mlp1
WORKSPACE_ROOT ?= $(abspath ..)
CATASTROPHE_DIR ?= $(WORKSPACE_ROOT)/Catastrophe
MLP1_TOOLCHAIN_IMAGE ?= ghcr.io/utility-muffin-research-kitchen/mlp1-toolchain:local
MLP1_UI := build/mlp1/bin/leaf-syncthing-ui

.PHONY: verify-upstream gateway-mlp1 controller-mlp1 ui-mlp1 package-platform package-mlp1 test test-ui-control-c test-ui-client-c clean

verify-upstream:
	$(PYTHON) scripts/verify_upstream.py \
		--lock upstream/syncthing-v2.1.2.lock.json \
		--output workdir/upstream/v2.1.2

gateway-mlp1:
	@mkdir -p build/mlp1/bin
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build \
		-trimpath -buildvcs=false -ldflags='-s -w -buildid=' \
		-o build/mlp1/bin/b0a-gateway-spike ./cmd/b0a-gateway-spike

controller-mlp1:
	@mkdir -p build/mlp1/bin
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build \
		-trimpath -buildvcs=false -ldflags='-s -w -buildid=' \
		-o build/mlp1/bin/leaf-syncthing ./cmd/leaf-syncthing

ui-mlp1:
	docker run --rm \
		-v "$(WORKSPACE_ROOT):/workspace" \
		-w /workspace/Leaf-Syncthing-Pak \
		"$(MLP1_TOOLCHAIN_IMAGE)" \
		make -f ports/mlp1/Makefile BUILD_DIR=build/mlp1 CATASTROPHE_DIR=/workspace/Catastrophe

package-platform:
	@case "$(PLATFORM)" in \
		mlp1) $(MAKE) package-mlp1 ;; \
		*) echo "unsupported Leaf-Syncthing-Pak platform: $(PLATFORM)" >&2; exit 1 ;; \
	esac

package-mlp1: verify-upstream controller-mlp1 ui-mlp1
	$(PYTHON) scripts/package_mlp1.py

test:
	$(GO) test ./...
	$(MAKE) test-ui-control-c
	$(MAKE) test-ui-client-c

test-ui-control-c:
	@mkdir -p build/tests
	$(CC) -std=c11 -Wall -Wextra -Werror \
		-DUI_CONTROL_FIXTURES_ROOT='"$(CURDIR)/tests/fixtures/ui-control-v1"' \
		-o build/tests/ui-control-v1 tests/ui_control_v1_test.c
	build/tests/ui-control-v1

test-ui-client-c:
	@mkdir -p build/tests
	$(CC) -std=c11 -Wall -Wextra -Werror \
		-Iinclude -I"$(CATASTROPHE_DIR)/include/cjson" \
		-DUI_CONTROL_FIXTURES_ROOT='"$(CURDIR)/tests/fixtures/ui-control-v1"' \
		-o build/tests/ui-control-client tests/ui_control_client_test.c \
		src/ui_control.c src/framed_socket.c "$(CATASTROPHE_DIR)/include/cjson/cJSON.c"
	build/tests/ui-control-client

clean:
	rm -rf build workdir
