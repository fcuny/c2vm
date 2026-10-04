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
