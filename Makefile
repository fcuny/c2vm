FC_VERSION?=1.17.0
FC_ARCH?=x86_64
FC_RELEASE=v$(FC_VERSION)-$(FC_ARCH)
FC_BINARY?=hack/firecracker/release-$(FC_RELEASE)/firecracker-$(FC_RELEASE)

FCNET_CONFIG?=/etc/cni/conf.d/50-c2vm.conflist
CNI_BIN_ROOT?=/opt/cni/bin
CNI_TAP_PLUGIN?=$(CNI_BIN_ROOT)/tc-redirect-tap

.PHONY: build
build:
	@go build -o c2vm cmd/c2vm/main.go

$(FC_BINARY):
	@mkdir -p hack/firecracker
	@curl -fL -o hack/firecracker/firecracker.tgz -s https://github.com/firecracker-microvm/firecracker/releases/download/v$(FC_VERSION)/firecracker-$(FC_RELEASE).tgz
	@tar xvzf hack/firecracker/firecracker.tgz -C hack/firecracker

$(FCNET_CONFIG):
	@sudo mkdir -p $(dir $(FCNET_CONFIG))
	@sudo cp hack/cni/50-c2vm.conflist $(FCNET_CONFIG)

$(CNI_TAP_PLUGIN):
	@go install github.com/awslabs/tc-redirect-tap/cmd/tc-redirect-tap@latest
	@sudo cp $(shell go env GOPATH)/bin/tc-redirect-tap $(CNI_BIN_ROOT)

.PHONY: all
all: build $(FC_BINARY) $(FCNET_CONFIG) $(CNI_TAP_PLUGIN)
