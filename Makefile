# Copyright 2022 Google LLC
#
# Licensed under the Apache License, STAGINGVERSION 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

REV = $(shell git describe --long --tags --match='v*' --dirty 2>/dev/null || git rev-list -n1 HEAD)
BINDIR ?= bin
REGISTRY ?= jiaxun
STAGINGVERSION ?= ${REV}
GCSFUSE_PATH ?= $(shell cat cmd/sidecar_mounter/gcsfuse_binary)
LDFLAGS ?= -s -w -X main.version=${STAGINGVERSION} -extldflags '-static'
OVERLAY ?= stable

DRIVER_BINARY = gcs-fuse-csi-driver
SIDECAR_BINARY = gcs-fuse-csi-driver-sidecar-mounter
WEBHOOK_BINARY = gcs-fuse-csi-driver-webhook

DRIVER_IMAGE = ${REGISTRY}/${DRIVER_BINARY}
SIDECAR_IMAGE = ${REGISTRY}/${SIDECAR_BINARY}
WEBHOOK_IMAGE = ${REGISTRY}/${WEBHOOK_BINARY}

E2E_TEST_CREATE_CLUSTER ?= false
E2E_TEST_USE_MANAGED_DRIVER ?= false
E2E_TEST_BUILD_DRIVER ?= false
E2E_TEST_ARTIFACTS_PATH ?= ../../_artifacts

$(info OVERLAY is ${OVERLAY})
$(info STAGINGVERSION is ${STAGINGVERSION})
$(info DRIVER_IMAGE is ${DRIVER_IMAGE})
$(info SIDECAR_IMAGE is ${SIDECAR_IMAGE})
$(info WEBHOOK_IMAGE is ${WEBHOOK_IMAGE})

all: build-image-and-push-multi-arch

driver:
	mkdir -p ${BINDIR}
	CGO_ENABLED=0 GOOS=linux go build -mod vendor -ldflags "${LDFLAGS} -X main.sidecarImageName=${SIDECAR_IMAGE}" -o ${BINDIR}/${DRIVER_BINARY} cmd/csi_driver/main.go

sidecar-mounter:
	mkdir -p ${BINDIR}
	CGO_ENABLED=0 GOOS=linux go build -mod vendor -ldflags "${LDFLAGS}" -o ${BINDIR}/${SIDECAR_BINARY} cmd/sidecar_mounter/main.go

webhook:
	mkdir -p ${BINDIR}
	CGO_ENABLED=0 GOOS=linux go build -mod vendor -ldflags "${LDFLAGS} -X main.sidecarImageName=${SIDECAR_IMAGE} -X main.sidecarImageVersion=${STAGINGVERSION}" -o ${BINDIR}/${WEBHOOK_BINARY} cmd/webhook/main.go

download-gcsfuse:
	mkdir -p ${BINDIR}
	gsutil cp ${GCSFUSE_PATH} ${BINDIR}/gcsfuse
	chmod +x ${BINDIR}/gcsfuse

build-image-and-push-multi-arch: build-image-and-push-linux-amd64 build-image-and-push-linux-arm64
	docker manifest create \
		--amend ${DRIVER_IMAGE}:${STAGINGVERSION} ${DRIVER_IMAGE}:${STAGINGVERSION}_linux_amd64 ${DRIVER_IMAGE}:${STAGINGVERSION}_linux_arm64
	docker manifest push --purge ${DRIVER_IMAGE}:${STAGINGVERSION}

	docker manifest create \
		--amend ${SIDECAR_IMAGE}:${STAGINGVERSION} ${SIDECAR_IMAGE}:${STAGINGVERSION}_linux_amd64 ${SIDECAR_IMAGE}:${STAGINGVERSION}_linux_arm64
	docker manifest push --purge ${SIDECAR_IMAGE}:${STAGINGVERSION}

	docker manifest create \
		--amend ${WEBHOOK_IMAGE}:${STAGINGVERSION} ${WEBHOOK_IMAGE}:${STAGINGVERSION}_linux_amd64
	docker manifest push --purge ${WEBHOOK_IMAGE}:${STAGINGVERSION}

build-image-and-push-linux-amd64: init-buildx download-gcsfuse
	docker buildx build \
		--file ./cmd/csi_driver/Dockerfile \
		--tag ${DRIVER_IMAGE}:${STAGINGVERSION}_linux_amd64 \
		--platform linux/amd64 \
		--build-arg STAGINGVERSION=${STAGINGVERSION} \
		--build-arg BUILDPLATFORM=linux/amd64 \
		--build-arg REGISTRY=${REGISTRY} \
		--push .

	docker buildx build \
		--file ./cmd/sidecar_mounter/Dockerfile \
		--tag ${SIDECAR_IMAGE}:${STAGINGVERSION}_linux_amd64 \
		--platform linux/amd64 \
		--build-arg STAGINGVERSION=${STAGINGVERSION} \
		--build-arg BUILDPLATFORM=linux/amd64 \
		--push .

	docker buildx build \
		--file ./cmd/webhook/Dockerfile \
		--tag ${WEBHOOK_IMAGE}:${STAGINGVERSION}_linux_amd64 \
		--platform linux/amd64 \
		--build-arg STAGINGVERSION=${STAGINGVERSION} \
		--build-arg BUILDPLATFORM=linux/amd64 \
		--build-arg REGISTRY=${REGISTRY} \
		--push .

build-image-and-push-linux-arm64: init-buildx download-gcsfuse
	docker buildx build \
		--file ./cmd/csi_driver/Dockerfile \
		--tag ${DRIVER_IMAGE}:${STAGINGVERSION}_linux_arm64 \
		--platform linux/arm64 \
		--build-arg STAGINGVERSION=${STAGINGVERSION} \
		--build-arg BUILDPLATFORM=linux/arm64 \
		--build-arg REGISTRY=${REGISTRY} \
		--push .

	docker buildx build \
		--file ./cmd/sidecar_mounter/Dockerfile \
		--tag ${SIDECAR_IMAGE}:${STAGINGVERSION}_linux_arm64 \
		--platform linux/arm64 \
		--build-arg STAGINGVERSION=${STAGINGVERSION} \
		--build-arg BUILDPLATFORM=linux/arm64 \
		--push .

install:	
	./deploy/base/webhook/patch-ca-bundle.sh
	make generate-spec-yaml
	kubectl apply -f ${BINDIR}/gcs-fuse-csi-driver-specs-generated.yaml
	./deploy/base/webhook/create-cert.sh
	git restore ./deploy/base/webhook/mutatingwebhook.yaml

uninstall:
	kubectl delete -k deploy/overlays/${OVERLAY}

generate-spec-yaml:
	mkdir -p ${BINDIR}
	cd ./deploy/overlays/${OVERLAY}; kustomize edit set image gke.gcr.io/gcs-fuse-csi-driver=${DRIVER_IMAGE}:${STAGINGVERSION};
	cd ./deploy/overlays/${OVERLAY}; kustomize edit set image gke.gcr.io/gcs-fuse-csi-driver-webhook=${WEBHOOK_IMAGE}:${STAGINGVERSION};
	kubectl kustomize deploy/overlays/${OVERLAY} | tee ${BINDIR}/gcs-fuse-csi-driver-specs-generated.yaml > /dev/null
	git restore ./deploy/overlays/${OVERLAY}/kustomization.yaml

verify:
	hack/verify-all.sh

unit-test:
	go test -v -mod=vendor -timeout 30s "./pkg/..." -cover

sanity-test:
	go test -v -mod=vendor -timeout 30s "./test/sanity/" -run TestSanity

e2e-test:
ifeq (${E2E_TEST_USE_MANAGED_DRIVER}, false)
ifeq (${E2E_TEST_BUILD_DRIVER}, true)
	make build-image-and-push-multi-arch
endif
	make uninstall || true
	make install
endif
	
	go test -v -mod=vendor -timeout 600s "./test/e2e/" -run TestE2E -report-dir ${E2E_TEST_ARTIFACTS_PATH}

init-buildx:
	# Ensure we use a builder that can leverage it (the default on linux will not)
	-docker buildx rm multiarch-multiplatform-builder
	docker buildx create --use --name=multiarch-multiplatform-builder
	docker run --rm --privileged multiarch/qemu-user-static --reset --credential yes --persistent yes
	# Register gcloud as a Docker credential helper.
	# Required for "docker buildx build --push".
	gcloud auth configure-docker --quiet