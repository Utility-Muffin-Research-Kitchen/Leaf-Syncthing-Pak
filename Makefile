PYTHON ?= python3
GO ?= go
CC ?= cc
PLATFORM ?= mlp1

.PHONY: verify-upstream gateway-mlp1 controller-mlp1 package-platform package-mlp1 test test-ui-control-c clean

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

package-platform:
	@case "$(PLATFORM)" in \
		mlp1) $(MAKE) package-mlp1 ;; \
		*) echo "unsupported Leaf-Syncthing-Pak platform: $(PLATFORM)" >&2; exit 1 ;; \
	esac

package-mlp1: verify-upstream controller-mlp1
	$(PYTHON) scripts/package_mlp1.py

test:
	$(GO) test ./...
	$(MAKE) test-ui-control-c

test-ui-control-c:
	@mkdir -p build/tests
	$(CC) -std=c11 -Wall -Wextra -Werror \
		-DUI_CONTROL_FIXTURES_ROOT='"$(CURDIR)/tests/fixtures/ui-control-v1"' \
		-o build/tests/ui-control-v1 tests/ui_control_v1_test.c
	build/tests/ui-control-v1

clean:
	rm -rf build workdir
