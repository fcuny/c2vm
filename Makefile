# The oldest macOS c2vm runs on. Without it, the linker stamps the SDK's
# version, and c2vm only runs on the macOS it was built on or newer. As
# CGO flags, unlike MACOSX_DEPLOYMENT_TARGET, it's part of the build
# cache's key.
MACOS_MIN := 26.0
ifeq ($(shell uname -s),Darwin)
export CGO_CFLAGS := -O2 -g -mmacosx-version-min=$(MACOS_MIN)
export CGO_LDFLAGS := -mmacosx-version-min=$(MACOS_MIN)
endif

.PHONY: build
build:
# The init runs in the VM: a static Linux binary for arm64, the only
# architecture VMs can run, whatever GOARCH the shell has. c2vm embeds
# it, with the embedinit tag.
	@CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o cmd/c2vm/c2vm-init ./cmd/c2vm-init
	@go build -tags embedinit -o c2vm ./cmd/c2vm
# Virtualization.framework only lets binaries with this entitlement
# create VMs. An ad-hoc signature is enough to run it locally.
ifeq ($(shell uname -s),Darwin)
	@codesign --force --sign - --entitlements hack/vz.entitlements c2vm
endif

# Boots VMs: needs macOS on Apple silicon, and the network. The test
# builds its own c2vm, with c2vm-init embedded.
.PHONY: e2e
e2e:
	@C2VM_E2E=1 go test -count=1 -timeout 30m -v ./e2e

# For the release workflow, which checks the binary against it.
.PHONY: macos-min
macos-min:
	@echo $(MACOS_MIN)
