PYTHON ?= python3
GO ?= go
PLATFORM ?= mlp1

.PHONY: verify-upstream gateway-mlp1 package-platform package-mlp1 test clean

verify-upstream:
	$(PYTHON) scripts/verify_upstream.py \
		--lock upstream/syncthing-v2.1.2.lock.json \
		--output workdir/upstream/v2.1.2

gateway-mlp1:
	@mkdir -p build/mlp1/bin
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build \
		-trimpath -buildvcs=false -ldflags='-s -w -buildid=' \
		-o build/mlp1/bin/b0a-gateway-spike ./cmd/b0a-gateway-spike

package-platform:
	@case "$(PLATFORM)" in \
		mlp1) $(MAKE) package-mlp1 ;; \
		*) echo "unsupported Leaf-Syncthing-Pak platform: $(PLATFORM)" >&2; exit 1 ;; \
	esac

package-mlp1: verify-upstream gateway-mlp1
	$(PYTHON) scripts/package_mlp1.py

test:
	$(GO) test ./internal/leaf ./cmd/b0a-gateway-spike

clean:
	rm -rf build workdir
