.PHONY: build
build:
# The init runs in the VM: a static Linux binary, for the host's
# architecture, which is the only one the hypervisor can run. c2vm
# embeds it, with the embedinit tag.
	@CGO_ENABLED=0 GOOS=linux go build -o cmd/c2vm/c2vm-init ./cmd/c2vm-init
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
