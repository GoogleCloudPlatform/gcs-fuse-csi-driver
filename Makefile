# Copyright 2022 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
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
BUILD_GCSFUSE_FROM_SOURCE ?= false
LDFLAGS ?= -s -w -X main.version=${STAGINGVERSION} -extldflags '-static'
OVERLAY ?= stable
PROJECT ?= $(shell gcloud config get-value project 2>&1 | head -n 1)

DRIVER_BINARY = gcs-fuse-csi-driver
SIDECAR_BINARY = gcs-fuse-csi-driver-sidecar-mounter
WEBHOOK_BINARY = gcs-fuse-csi-driver-webhook

DRIVER_IMAGE = ${REGISTRY}/${DRIVER_BINARY}
SIDECAR_IMAGE = ${REGISTRY}/${SIDECAR_BINARY}
WEBHOOK_IMAGE = ${REGISTRY}/${WEBHOOK_BINARY}

RAND := $(shell od -An -N2 -i /dev/random | tr -d ' ')
E2E_TEST_GCP_PROJECT ?= $(shell gcloud config get-value project 2>&1 | head -n 1)
E2E_TEST_CREATE_CLUSTER ?= false
E2E_TEST_CLUSTER_NAME ?= gcs-fuse-csi-driver-test-${RAND}
E2E_TEST_CLUSTER_VERSION ?=
E2E_TEST_NODE_VERSION ?=
E2E_TEST_CLUSTER_NUM_NODES ?= 3
E2E_TEST_CLUSTER_REGION ?= us-central1
E2E_TEST_IMAGE_TYPE ?= cos_containerd
E2E_TEST_CLEANUP_CLUSTER ?= false
E2E_TEST_CREATE_CLUSTER_ARGS = --quiet --region ${E2E_TEST_CLUSTER_REGION} --num-nodes ${E2E_TEST_CLUSTER_NUM_NODES} --machine-type n1-standard-2 --image-type ${E2E_TEST_IMAGE_TYPE} --workload-pool=${E2E_TEST_GCP_PROJECT}.svc.id.goog
ifneq ("${E2E_TEST_CLUSTER_VERSION}", "")
E2E_TEST_CREATE_CLUSTER_ARGS += --cluster-version ${E2E_TEST_CLUSTER_VERSION}
endif
ifneq ("${E2E_TEST_NODE_VERSION}", "")
E2E_TEST_CREATE_CLUSTER_ARGS += --node-version ${E2E_TEST_NODE_VERSION}
endif

export E2E_TEST_API_ENV ?= prod
ifeq (${E2E_TEST_API_ENV}, staging)
	export CLOUDSDK_API_ENDPOINT_OVERRIDES_CONTAINER = https://staging-container.sandbox.googleapis.com/
else ifeq (${E2E_TEST_API_ENV}, staging2)
	export CLOUDSDK_API_ENDPOINT_OVERRIDES_CONTAINER = https://staging2-container.sandbox.googleapis.com/
else ifeq (${E2E_TEST_API_ENV}, test)
	export CLOUDSDK_API_ENDPOINT_OVERRIDES_CONTAINER = https://test-container.sandbox.googleapis.com/
else ifeq (${E2E_TEST_API_ENV}, sandbox)
	export CLOUDSDK_API_ENDPOINT_OVERRIDES_CONTAINER = ${CLOUDSDK_API_ENDPOINT_OVERRIDES_CONTAINER}
endif

E2E_TEST_USE_MANAGED_DRIVER ?= false
E2E_TEST_BUILD_DRIVER ?= false

E2E_TEST_FOCUS ?=
E2E_TEST_SKIP ?= Dynamic.PV
E2E_TEST_GINKGO_PROCS ?= 5
E2E_TEST_GINKGO_FLAGS ?= --procs ${E2E_TEST_GINKGO_PROCS} -v --flake-attempts 2 --timeout 20m
ifneq ("${E2E_TEST_FOCUS}", "")
E2E_TEST_GINKGO_FLAGS+= --focus "${E2E_TEST_FOCUS}"
endif
ifneq ("${E2E_TEST_SKIP}", "")
E2E_TEST_GINKGO_FLAGS+= --skip "${E2E_TEST_SKIP}"
endif
E2E_TEST_ARTIFACTS_PATH ?= ../../_artifacts

$(info OVERLAY is ${OVERLAY})
$(info STAGINGVERSION is ${STAGINGVERSION})
$(info DRIVER_IMAGE is ${DRIVER_IMAGE})
$(info SIDECAR_IMAGE is ${SIDECAR_IMAGE})
$(info WEBHOOK_IMAGE is ${WEBHOOK_IMAGE})

all: build-image-and-push-multi-arch

driver:
	mkdir -p ${BINDIR}
	CGO_ENABLED=0 GOOS=linux go build -mod vendor -ldflags "${LDFLAGS}" -o ${BINDIR}/${DRIVER_BINARY} cmd/csi_driver/main.go

sidecar-mounter:
	mkdir -p ${BINDIR}
	CGO_ENABLED=0 GOOS=linux go build -mod vendor -ldflags "${LDFLAGS}" -o ${BINDIR}/${SIDECAR_BINARY} cmd/sidecar_mounter/main.go

webhook:
	mkdir -p ${BINDIR}
	CGO_ENABLED=0 GOOS=linux go build -mod vendor -ldflags "${LDFLAGS} -X main.sidecarImageVersion=${STAGINGVERSION}" -o ${BINDIR}/${WEBHOOK_BINARY} cmd/webhook/main.go

download-gcsfuse:
	mkdir -p ${BINDIR}
ifeq (${BUILD_GCSFUSE_FROM_SOURCE}, true)
	docker build --file ./cmd/sidecar_mounter/Dockerfile.gcsfuse --build-arg STAGINGVERSION=${STAGINGVERSION} --tag local/gcsfuse:latest .
	docker create --name local_gcsfuse local/gcsfuse:latest
	docker cp local_gcsfuse:/tmp/gcsfuse/bin/gcsfuse ${BINDIR}/gcsfuse
	docker rm -f local_gcsfuse
else
	gsutil cp ${GCSFUSE_PATH} ${BINDIR}/gcsfuse
	chmod +x ${BINDIR}/gcsfuse
endif

	chmod 0555 ${BINDIR}/gcsfuse

build-image-and-push-multi-arch: build-image-and-push-linux-amd64 build-image-and-push-linux-arm64

build-image-and-push-linux-amd64: init-buildx download-gcsfuse
	docker buildx build \
		--file ./cmd/csi_driver/Dockerfile \
		--tag ${DRIVER_IMAGE}:${STAGINGVERSION}_linux_amd64 \
		--platform linux/amd64 \
		--build-arg STAGINGVERSION=${STAGINGVERSION} \
		--build-arg BUILDPLATFORM=linux/amd64 \
		--build-arg REGISTRY=${REGISTRY} \
		--push .
	docker manifest create \
		--amend ${DRIVER_IMAGE}:${STAGINGVERSION} ${DRIVER_IMAGE}:${STAGINGVERSION}_linux_amd64
	docker manifest push --purge ${DRIVER_IMAGE}:${STAGINGVERSION}

	docker buildx build \
		--file ./cmd/sidecar_mounter/Dockerfile \
		--tag ${SIDECAR_IMAGE}:${STAGINGVERSION}_linux_amd64 \
		--platform linux/amd64 \
		--build-arg STAGINGVERSION=${STAGINGVERSION} \
		--build-arg BUILDPLATFORM=linux/amd64 \
		--push .
	docker manifest create \
		--amend ${SIDECAR_IMAGE}:${STAGINGVERSION} ${SIDECAR_IMAGE}:${STAGINGVERSION}_linux_amd64
	docker manifest push --purge ${SIDECAR_IMAGE}:${STAGINGVERSION}

	docker buildx build \
		--file ./cmd/webhook/Dockerfile \
		--tag ${WEBHOOK_IMAGE}:${STAGINGVERSION}_linux_amd64 \
		--platform linux/amd64 \
		--build-arg STAGINGVERSION=${STAGINGVERSION} \
		--build-arg BUILDPLATFORM=linux/amd64 \
		--build-arg REGISTRY=${REGISTRY} \
		--push .
	docker manifest create \
		--amend ${WEBHOOK_IMAGE}:${STAGINGVERSION} ${WEBHOOK_IMAGE}:${STAGINGVERSION}_linux_amd64
	docker manifest push --purge ${WEBHOOK_IMAGE}:${STAGINGVERSION}

build-image-and-push-linux-arm64: init-buildx download-gcsfuse
	docker buildx build \
		--file ./cmd/csi_driver/Dockerfile \
		--tag ${DRIVER_IMAGE}:${STAGINGVERSION}_linux_arm64 \
		--platform linux/arm64 \
		--build-arg STAGINGVERSION=${STAGINGVERSION} \
		--build-arg BUILDPLATFORM=linux/arm64 \
		--build-arg REGISTRY=${REGISTRY} \
		--push .
	docker manifest create \
		--amend ${DRIVER_IMAGE}:${STAGINGVERSION} ${DRIVER_IMAGE}:${STAGINGVERSION}_linux_arm64
	docker manifest push --purge ${DRIVER_IMAGE}:${STAGINGVERSION}

	docker buildx build \
		--file ./cmd/sidecar_mounter/Dockerfile \
		--tag ${SIDECAR_IMAGE}:${STAGINGVERSION}_linux_arm64 \
		--platform linux/arm64 \
		--build-arg STAGINGVERSION=${STAGINGVERSION} \
		--build-arg BUILDPLATFORM=linux/arm64 \
		--push .
	docker manifest create \
		--amend ${SIDECAR_IMAGE}:${STAGINGVERSION} ${SIDECAR_IMAGE}:${STAGINGVERSION}_linux_arm64
	docker manifest push --purge ${SIDECAR_IMAGE}:${STAGINGVERSION}

install:	
	./deploy/base/webhook/patch-ca-bundle.sh
	make generate-spec-yaml OVERLAY=${OVERLAY} REGISTRY=${REGISTRY} STAGINGVERSION=${STAGINGVERSION}
	kubectl apply -f ${BINDIR}/gcs-fuse-csi-driver-specs-generated.yaml
	./deploy/base/webhook/create-cert.sh
	git restore ./deploy/base/webhook/mutatingwebhook.yaml

uninstall:
	kubectl delete -k deploy/overlays/${OVERLAY} --wait

generate-spec-yaml:
	mkdir -p ${BINDIR}
	cd ./deploy/overlays/${OVERLAY}; kustomize edit set image gke.gcr.io/gcs-fuse-csi-driver=${DRIVER_IMAGE}:${STAGINGVERSION};
	cd ./deploy/overlays/${OVERLAY}; kustomize edit set image gke.gcr.io/gcs-fuse-csi-driver-webhook=${WEBHOOK_IMAGE}:${STAGINGVERSION};
	echo "[{\"op\": \"replace\",\"path\": \"/spec/template/spec/containers/0/env/1/value\",\"value\": \"${REGISTRY}\"}]" > ./deploy/overlays/${OVERLAY}/registry_patch_node.json
	echo "[{\"op\": \"replace\",\"path\": \"/spec/template/spec/containers/0/env/1/value\",\"value\": \"${REGISTRY}\"}]" > ./deploy/overlays/${OVERLAY}/registry_patch_webhook.json
	echo "[{\"op\": \"replace\",\"path\": \"/spec/tokenRequests/0/audience\",\"value\": \"${PROJECT}.svc.id.goog\"}]" > ./deploy/overlays/${OVERLAY}/project_patch_csi_driver.json
	kubectl kustomize deploy/overlays/${OVERLAY} | tee ${BINDIR}/gcs-fuse-csi-driver-specs-generated.yaml > /dev/null
	git restore ./deploy/overlays/${OVERLAY}/kustomization.yaml
	git restore ./deploy/overlays/${OVERLAY}/registry_patch_node.json
	git restore ./deploy/overlays/${OVERLAY}/registry_patch_webhook.json
	git restore ./deploy/overlays/${OVERLAY}/project_patch_csi_driver.json

verify:
	hack/verify-all.sh

unit-test:
	go test -v -mod=vendor -timeout 30s "./pkg/..." -cover

sanity-test:
	go test -v -mod=vendor -timeout 30s "./test/sanity/" -run TestSanity

e2e-test: init-ginkgo
ifeq (${E2E_TEST_CREATE_CLUSTER}, true)
	gcloud container clusters create ${E2E_TEST_CLUSTER_NAME} ${E2E_TEST_CREATE_CLUSTER_ARGS}
	gcloud container clusters get-credentials ${E2E_TEST_CLUSTER_NAME} --region ${E2E_TEST_CLUSTER_REGION}
endif

ifeq (${E2E_TEST_USE_MANAGED_DRIVER}, false)
ifeq (${E2E_TEST_BUILD_DRIVER}, true)
	make build-image-and-push-linux-amd64 REGISTRY=${REGISTRY} STAGINGVERSION=${STAGINGVERSION}
endif

	make uninstall OVERLAY=${OVERLAY} || true
	make install OVERLAY=${OVERLAY} REGISTRY=${REGISTRY} STAGINGVERSION=${STAGINGVERSION}
endif

	ginkgo run ${E2E_TEST_GINKGO_FLAGS} "./test/e2e/" -- -report-dir ${E2E_TEST_ARTIFACTS_PATH} 

ifeq (${E2E_TEST_CLEANUP_CLUSTER}, true)
	gcloud container clusters delete ${E2E_TEST_CLUSTER_NAME} --quiet --region ${E2E_TEST_CLUSTER_REGION}
endif

init-ginkgo:
	export PATH=${PATH}:$(go env GOPATH)/bin
	go install github.com/onsi/ginkgo/v2/ginkgo@v2.9.2

init-buildx:
	# Ensure we use a builder that can leverage it (the default on linux will not)
	-docker buildx rm multiarch-multiplatform-builder
	docker buildx create --use --name=multiarch-multiplatform-builder
	docker run --rm --privileged multiarch/qemu-user-static --reset --credential yes --persistent yes
	# Register gcloud as a Docker credential helper.
	# Required for "docker buildx build --push".
	gcloud auth configure-docker --quiet