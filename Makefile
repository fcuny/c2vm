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

# Boots VMs: needs macOS on Apple silicon, and the network. The test
# builds its own c2vm and c2vm-init.
.PHONY: e2e
e2e:
	@C2VM_E2E=1 go test -count=1 -timeout 30m -v ./e2e
