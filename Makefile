PYTHON ?= python3
GO ?= go
CC ?= cc
PLATFORM ?= mlp1
WORKSPACE_ROOT ?= $(abspath ..)
CATASTROPHE_DIR ?= $(WORKSPACE_ROOT)/Catastrophe
MLP1_TOOLCHAIN_IMAGE ?= ghcr.io/utility-muffin-research-kitchen/mlp1-toolchain:local
MLP1_UI := build/mlp1/bin/leaf-syncthing-ui
MLP1_FLOOR_UI := build/mlp1/bin/leaf-syncthing-floor

.PHONY: verify-upstream gateway-mlp1 controller-mlp1 ui-mlp1 package-platform package-mlp1 package-floor-mlp1 b4b-fixture b4b-local-smoke b4b-device-floor-smoke b4b-device-pre-gating-smoke b4b-device-transition-smoke test test-ui-control-c test-ui-client-c test-service-view-c test-version-gate clean

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
	$(PYTHON) scripts/package_mlp1.py \
		$(if $(PAK_VERSION),--pak-version "$(PAK_VERSION)") \
		$(if $(MIN_LEAF_VERSION),--min-leaf-version "$(MIN_LEAF_VERSION)")

package-floor-mlp1: ui-mlp1
	@test -n "$(MIN_LEAF_VERSION)" || { echo "MIN_LEAF_VERSION is required" >&2; exit 2; }
	$(PYTHON) scripts/package_floor.py \
		--pak-version "$(if $(FLOOR_PAK_VERSION),$(FLOOR_PAK_VERSION),0.0.1)" \
		--min-leaf-version "$(MIN_LEAF_VERSION)"

b4b-fixture:
	$(PYTHON) scripts/build_b4b_fixture.py \
		--floor-version "$(if $(FLOOR_PAK_VERSION),$(FLOOR_PAK_VERSION),0.0.1)" \
		--real-version "$(if $(PAK_VERSION),$(PAK_VERSION),0.0.2)" \
		--min-leaf-version "$(if $(MIN_LEAF_VERSION),$(MIN_LEAF_VERSION),99.99.99)"

b4b-local-smoke:
	bash scripts/b4b-local-smoke.sh

b4b-device-floor-smoke: b4b-fixture
	bash scripts/adb-mlp1-b4b-floor-smoke.sh

b4b-device-pre-gating-smoke: b4b-fixture
	bash scripts/adb-mlp1-b4b-pre-gating-smoke.sh

b4b-device-transition-smoke:
	bash scripts/adb-mlp1-b4b-transition-smoke.sh

test:
	$(GO) test ./...
	$(MAKE) test-ui-control-c
	$(MAKE) test-ui-client-c
	$(MAKE) test-service-view-c
	$(MAKE) test-version-gate

test-version-gate:
	bash scripts/leaf-version-gate-test.sh

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

test-service-view-c:
	@mkdir -p build/tests
	$(CC) -std=c11 -Wall -Wextra -Werror \
		-Iinclude -I"$(CATASTROPHE_DIR)/include/cjson" \
		-o build/tests/service-view tests/service_view_test.c \
		src/ctl1.c src/framed_socket.c "$(CATASTROPHE_DIR)/include/cjson/cJSON.c"
	build/tests/service-view

clean:
	rm -rf build workdir
