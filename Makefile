# Where go install puts binaries: GOBIN, or the first GOPATH entry's bin.
# := so a BINDIR in the environment doesn't change it; make install
# BINDIR=... still does.
BINDIR := $(or $(shell go env GOBIN),$(firstword $(subst :, ,$(shell go env GOPATH)))/bin)

.PHONY: build
build:
	@go build -o c2vm ./cmd/c2vm
# The init runs in the VM: a static Linux binary, for the host's
# architecture, which is the only one the hypervisor can run.
	@CGO_ENABLED=0 GOOS=linux go build -o c2vm-init ./cmd/c2vm-init
# Virtualization.framework only lets binaries with this entitlement
# create VMs. An ad-hoc signature is enough to run it locally.
ifeq ($(shell uname -s),Darwin)
	@codesign --force --sign - --entitlements hack/vz.entitlements c2vm
endif

# Copies what make build built, without building, so sudo make install
# doesn't leave root-owned files in the checkout and the build cache.
# c2vm looks for c2vm-init next to itself. install writes new files
# rather than overwriting the old ones in place, which on Apple silicon
# gets a re-signed binary killed when it starts.
.PHONY: install
install:
	@test -f c2vm -a -f c2vm-init || { echo "run make build first"; exit 1; }
	@mkdir -p "$(BINDIR)"
	@install -m 755 c2vm c2vm-init "$(BINDIR)"

.PHONY: test
test:
	@go test -race ./...

.PHONY: lint
lint:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then echo "gofmt needed on:"; echo "$$unformatted"; exit 1; fi
	@go vet ./...

# Boots VMs: needs macOS on Apple silicon, and the network. The test
# builds its own c2vm and c2vm-init.
.PHONY: e2e
e2e:
	@C2VM_E2E=1 go test -count=1 -timeout 30m -v ./e2e

# Tag and push a release: make release VERSION=v0.2.0. CI can't boot
# VMs, so this runs the e2e tests too, on the default kernel; SKIP_E2E=1
# skips them.
.PHONY: release
release:
	@test -n "$(VERSION)" || { echo "usage: make release VERSION=vX.Y.Z"; exit 1; }
	@echo "$(VERSION)" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$$' || { echo "VERSION must look like vX.Y.Z"; exit 1; }
	@test "$$(git rev-parse --abbrev-ref HEAD)" = main || { echo "not on main"; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "working tree is not clean"; exit 1; }
	git fetch origin --tags
	git merge --ff-only origin/main
	@test "$$(git rev-parse HEAD)" = "$$(git rev-parse origin/main)" || { echo "main does not match origin/main"; exit 1; }
	@! git rev-parse -q --verify "refs/tags/$(VERSION)" >/dev/null || { echo "tag $(VERSION) already exists"; exit 1; }
	$(MAKE) lint test build
ifneq ($(SKIP_E2E),1)
	env -u C2VM_E2E_KERNEL $(MAKE) e2e
endif
	git tag -a $(VERSION) -m "$(VERSION)"
	git push origin $(VERSION)
