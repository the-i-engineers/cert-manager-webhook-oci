OS ?= $(shell go env GOOS)
ARCH ?= $(shell go env GOARCH)

SERVER?=ghcr.io
OWNER?=the-i-engineers
IMG_NAME?=cert-manager-webhook-oci

PLATFORM?= "linux/amd64,linux/arm64"

Version := $(shell git describe --tags --dirty)
GitCommit := $(shell git rev-parse HEAD)

OUT := $(shell pwd)/deploy

KUBE_VERSION=1.32.0

$(shell mkdir -p "$(OUT)")
export TEST_ASSET_ETCD=_test/kubebuilder/bin/etcd
export TEST_ASSET_KUBE_APISERVER=_test/kubebuilder/bin/kube-apiserver
export TEST_ASSET_KUBECTL=_test/kubebuilder/bin/kubectl
export TEST_ZONE_NAME=example.com.

test: _test/kubebuilder
	@envsubst < testdata/oci/config.json.sample > testdata/oci/config.json && \
  envsubst < testdata/oci/oci-profile.yaml.sample > testdata/oci/oci-profile.yaml && \
	go test -timeout 30s -v .

_test/kubebuilder:
	curl -fsSL https://go.kubebuilder.io/test-tools/$(KUBE_VERSION)/$(OS)/$(ARCH) -o kubebuilder-tools.tar.gz
	mkdir -p _test/kubebuilder
	tar -xvf kubebuilder-tools.tar.gz
	mv kubebuilder/bin _test/kubebuilder/
	rm kubebuilder-tools.tar.gz
	rm -R kubebuilder

clean: clean-kubebuilder

clean-kubebuilder:
	rm -Rf _test/kubebuilder

.PHONY: build-local
build-local:
	@docker buildx build \
		--progress=plain \
		--build-arg Version=$(Version) --build-arg GitCommit=$(GitCommit) \
		--platform linux/amd64 \
		--output "type=docker,push=false" \
		--tag $(SERVER)/$(OWNER)/$(IMG_NAME):$(Version) .

.PHONY: build
build:
	@echo $(SERVER)/$(OWNER)/$(IMG_NAME):$(Version) && \
	docker buildx build \
		--progress=plain \
		--build-arg Version=$(Version) --build-arg GitCommit=$(GitCommit) \
		--platform $(PLATFORM) \
		--output "type=image,push=false" \
		--tag $(SERVER)/$(OWNER)/$(IMG_NAME):$(Version) .

.PHONY: rendered-manifest.yaml
rendered-manifest.yaml:
	helm template \
	    cert-manager-webhook-oci \
        --set image.repository=$(SERVER)/$(OWNER)/$(IMG_NAME) \
        --set image.tag=$(Version) \
		--namespace cert-manager \
        deploy/cert-manager-webhook-oci > "$(OUT)/rendered-manifest.yaml"
