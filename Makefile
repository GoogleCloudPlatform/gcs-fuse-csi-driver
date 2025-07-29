# Copyright 2018 The Kubernetes Authors.
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

export REGISTRY ?= jiaxun
export STAGINGVERSION ?= $(shell git describe --long --tags --match='v*' --dirty 2>/dev/null || git rev-list -n1 HEAD)
export OVERLAY ?= stable
export BUILD_GCSFUSE_FROM_SOURCE ?= false
export BUILD_ARM ?= false
export SKIP_WI_NODE_LABEL_CHECK ?= false
BINDIR ?= $(shell pwd)/bin
GCSFUSE_PATH ?= $(shell cat cmd/sidecar_mounter/gcsfuse_binary)
LDFLAGS ?= -s -w -X main.version=${STAGINGVERSION} -extldflags '-static'
# assume that a GKE cluster identifier follows the format gke_{project-name}_{location}_{cluster-name}
PROJECT ?= $(shell kubectl config current-context | cut -d '_' -f 2)
CA_BUNDLE ?= $(shell kubectl config view --raw -o json | jq '.clusters[]' | jq "select(.name == \"$(shell kubectl config current-context)\")" | jq '.cluster."certificate-authority-data"' | head -n 1)
IDENTITY_PROVIDER ?= $(shell kubectl get --raw /.well-known/openid-configuration | jq -r .issuer)
IDENTITY_POOL ?= ${PROJECT}.svc.id.goog


DRIVER_BINARY = gcs-fuse-csi-driver
SIDECAR_BINARY = gcs-fuse-csi-driver-sidecar-mounter
WEBHOOK_BINARY = gcs-fuse-csi-driver-webhook
PREFETCH_BINARY = gcs-fuse-csi-driver-metadata-prefetch

DRIVER_IMAGE = ${REGISTRY}/${DRIVER_BINARY}
SIDECAR_IMAGE = ${REGISTRY}/${SIDECAR_BINARY}
WEBHOOK_IMAGE = ${REGISTRY}/${WEBHOOK_BINARY}
PREFETCH_IMAGE = ${REGISTRY}/${PREFETCH_BINARY}

DOCKER_BUILDX_ARGS ?= --push --builder multiarch-multiplatform-builder --build-arg STAGINGVERSION=${STAGINGVERSION}
ifneq ("$(shell docker buildx build --help | grep 'provenance')", "")
DOCKER_BUILDX_ARGS += --provenance=false
endif

DOCKER_BUILDX_ARGS += --quiet

$(info PROJECT is ${PROJECT})
$(info OVERLAY is ${OVERLAY})
$(info STAGINGVERSION is ${STAGINGVERSION})
$(info DRIVER_IMAGE is ${DRIVER_IMAGE})
$(info SIDECAR_IMAGE is ${SIDECAR_IMAGE})
$(info WEBHOOK_IMAGE is ${WEBHOOK_IMAGE})

all: driver sidecar-mounter webhook metadata-prefetch

driver:
	mkdir -p ${BINDIR}
	CGO_ENABLED=0 GOOS=linux go build -mod vendor -ldflags "${LDFLAGS}" -o ${BINDIR}/${DRIVER_BINARY} cmd/csi_driver/main.go

sidecar-mounter:
	mkdir -p ${BINDIR}
	CGO_ENABLED=0 GOOS=linux go build -mod vendor -ldflags "${LDFLAGS}" -o ${BINDIR}/${SIDECAR_BINARY} cmd/sidecar_mounter/main.go

metadata-prefetch:
	mkdir -p ${BINDIR}
	CGO_ENABLED=0 GOOS=linux go build -mod vendor -ldflags "${LDFLAGS}" -o ${BINDIR}/${PREFETCH_BINARY} cmd/metadata_prefetch/main.go

webhook:
	mkdir -p ${BINDIR}
	CGO_ENABLED=0 GOOS=linux go build -mod vendor -ldflags "${LDFLAGS}" -o ${BINDIR}/${WEBHOOK_BINARY} cmd/webhook/main.go

download-gcsfuse:
	mkdir -p ${BINDIR}/linux/amd64 ${BINDIR}/linux/arm64

ifeq (${BUILD_GCSFUSE_FROM_SOURCE}, true)
	rm -f ${BINDIR}/Dockerfile.gcsfuse
	curl https://raw.githubusercontent.com/GoogleCloudPlatform/gcsfuse/master/tools/package_gcsfuse_docker/Dockerfile -o ${BINDIR}/Dockerfile.gcsfuse
	$(eval GCSFUSE_VERSION=999.$(shell git ls-remote https://github.com/GoogleCloudPlatform/gcsfuse.git HEAD | cut -c 1-40))

	docker buildx build \
		--load \
		--file ${BINDIR}/Dockerfile.gcsfuse \
		--tag gcsfuse-release:${GCSFUSE_VERSION}-amd \
		--build-arg GCSFUSE_VERSION=${GCSFUSE_VERSION} \
		--build-arg BRANCH_NAME=master \
		--build-arg ARCHITECTURE=amd64 \
		--platform=linux/amd64 .

	docker run \
		-v ${BINDIR}/linux/amd64:/release \
		gcsfuse-release:${GCSFUSE_VERSION}-amd \
		cp /gcsfuse_${GCSFUSE_VERSION}_amd64/usr/bin/gcsfuse /release

ifeq (${BUILD_ARM}, true)
	docker buildx build \
		--load \
		--file ${BINDIR}/Dockerfile.gcsfuse \
		--tag gcsfuse-release:${GCSFUSE_VERSION}-arm \
		--build-arg GCSFUSE_VERSION=${GCSFUSE_VERSION} \
		--build-arg BRANCH_NAME=master \
		--build-arg ARCHITECTURE=arm64 \
		--platform=linux/arm64 .

	docker run \
		-v ${BINDIR}/linux/arm64:/release \
		gcsfuse-release:${GCSFUSE_VERSION}-arm \
		cp /gcsfuse_${GCSFUSE_VERSION}_arm64/usr/bin/gcsfuse /release
endif

else
	gsutil cp ${GCSFUSE_PATH}/linux/amd64/gcsfuse ${BINDIR}/linux/amd64/gcsfuse
ifeq (${BUILD_ARM}, true)
	gsutil cp ${GCSFUSE_PATH}/linux/arm64/gcsfuse ${BINDIR}/linux/arm64/gcsfuse
endif
endif

	chmod +x ${BINDIR}/linux/amd64/gcsfuse
	chmod 0555 ${BINDIR}/linux/amd64/gcsfuse

ifeq (${BUILD_ARM}, true)
	chmod +x ${BINDIR}/linux/arm64/gcsfuse
	chmod 0555 ${BINDIR}/linux/arm64/gcsfuse
endif

	${BINDIR}/linux/$(shell dpkg --print-architecture)/gcsfuse --version

init-buildx:
	# Ensure we use a builder that can leverage it (the default on linux will not)
	-docker buildx rm multiarch-multiplatform-builder
	docker buildx create --use --name=multiarch-multiplatform-builder
	docker run --rm --privileged multiarch/qemu-user-static --reset --credential yes --persistent yes
	# Register gcloud as a Docker credential helper.
	# Required for "docker buildx build --push".
	gcloud auth configure-docker --quiet

build-image-and-push-multi-arch: init-buildx download-gcsfuse build-image-linux-amd64
ifeq (${BUILD_ARM}, true)
	$(MAKE) build-image-linux-arm64
	docker manifest create ${DRIVER_IMAGE}:${STAGINGVERSION} ${DRIVER_IMAGE}:${STAGINGVERSION}_linux_amd64 ${DRIVER_IMAGE}:${STAGINGVERSION}_linux_arm64
	docker manifest create ${SIDECAR_IMAGE}:${STAGINGVERSION} ${SIDECAR_IMAGE}:${STAGINGVERSION}_linux_amd64 ${SIDECAR_IMAGE}:${STAGINGVERSION}_linux_arm64
	docker manifest create ${PREFETCH_IMAGE}:${STAGINGVERSION} ${PREFETCH_IMAGE}:${STAGINGVERSION}_linux_amd64 ${PREFETCH_IMAGE}:${STAGINGVERSION}_linux_arm64
else
	docker manifest create ${DRIVER_IMAGE}:${STAGINGVERSION} ${DRIVER_IMAGE}:${STAGINGVERSION}_linux_amd64
	docker manifest create ${SIDECAR_IMAGE}:${STAGINGVERSION} ${SIDECAR_IMAGE}:${STAGINGVERSION}_linux_amd64
	docker manifest create ${PREFETCH_IMAGE}:${STAGINGVERSION} ${PREFETCH_IMAGE}:${STAGINGVERSION}_linux_amd64
endif

	docker manifest create ${WEBHOOK_IMAGE}:${STAGINGVERSION} ${WEBHOOK_IMAGE}:${STAGINGVERSION}_linux_amd64

	docker manifest push --purge ${DRIVER_IMAGE}:${STAGINGVERSION}
	docker manifest push --purge ${SIDECAR_IMAGE}:${STAGINGVERSION}
	docker manifest push --purge ${PREFETCH_IMAGE}:${STAGINGVERSION}
	docker manifest push --purge ${WEBHOOK_IMAGE}:${STAGINGVERSION}

build-image-linux-amd64:
	docker buildx build ${DOCKER_BUILDX_ARGS} \
		--file ./cmd/metadata_prefetch/Dockerfile \
		--tag ${PREFETCH_IMAGE}:${STAGINGVERSION}_linux_amd64 \
		--platform linux/amd64 \
		--build-arg TARGETPLATFORM=linux/amd64 .

	docker buildx build \
		--file ./cmd/csi_driver/Dockerfile \
		--tag validation_linux_amd64 \
		--platform=linux/amd64 \
		--target validation-image .

	docker buildx build ${DOCKER_BUILDX_ARGS} \
		--file ./cmd/csi_driver/Dockerfile \
		--tag ${DRIVER_IMAGE}:${STAGINGVERSION}_linux_amd64 \
		--platform linux/amd64 .

	docker buildx build ${DOCKER_BUILDX_ARGS} \
		--file ./cmd/sidecar_mounter/Dockerfile \
		--tag ${SIDECAR_IMAGE}:${STAGINGVERSION}_linux_amd64 \
		--platform linux/amd64 \
		--build-arg TARGETPLATFORM=linux/amd64 .

	docker buildx build ${DOCKER_BUILDX_ARGS} \
		--file ./cmd/webhook/Dockerfile \
		--tag ${WEBHOOK_IMAGE}:${STAGINGVERSION}_linux_amd64 \
		--platform linux/amd64 .

build-image-linux-arm64:
	docker buildx build ${DOCKER_BUILDX_ARGS} \
		--file ./cmd/metadata_prefetch/Dockerfile \
		--tag ${PREFETCH_IMAGE}:${STAGINGVERSION}_linux_arm64 \
		--platform linux/arm64 \
		--build-arg TARGETPLATFORM=linux/arm64 .
	\
	docker buildx build \
		--file ./cmd/csi_driver/Dockerfile \
		--tag validation_linux_arm64 \
		--platform=linux/arm64 \
		--target validation-image .
	\
	docker buildx build ${DOCKER_BUILDX_ARGS} \
		--file ./cmd/csi_driver/Dockerfile \
		--tag ${DRIVER_IMAGE}:${STAGINGVERSION}_linux_arm64 \
		--platform linux/arm64 .
	\
	docker buildx build ${DOCKER_BUILDX_ARGS} \
		--file ./cmd/sidecar_mounter/Dockerfile \
		--tag ${SIDECAR_IMAGE}:${STAGINGVERSION}_linux_arm64 \
		--platform linux/arm64 \
		--build-arg TARGETPLATFORM=linux/arm64 .

install:
	$(MAKE) generate-spec-yaml OVERLAY=${OVERLAY} REGISTRY=${REGISTRY} STAGINGVERSION=${STAGINGVERSION}
	kubectl apply -f ${BINDIR}/gcs-fuse-csi-driver-specs-generated.yaml
	./deploy/base/webhook/create-cert.sh --namespace gcs-fuse-csi-driver --service gcs-fuse-csi-driver-webhook --secret gcs-fuse-csi-driver-webhook-secret
	./deploy/base/webhook/manage-validating_admission_policy.sh --install

uninstall:
	$(MAKE) generate-spec-yaml OVERLAY=${OVERLAY} REGISTRY=${REGISTRY} STAGINGVERSION=${STAGINGVERSION}
	kubectl delete -f ${BINDIR}/gcs-fuse-csi-driver-specs-generated.yaml --wait
	./deploy/base/webhook/manage-validating_admission_policy.sh --uninstall

generate-spec-yaml:
	mkdir -p ${BINDIR}
	./deploy/install-kustomize.sh
	cd ./deploy/overlays/${OVERLAY}; ${BINDIR}/kustomize edit set image gke.gcr.io/gcs-fuse-csi-driver=${DRIVER_IMAGE}:${STAGINGVERSION};
	cd ./deploy/overlays/${OVERLAY}; ${BINDIR}/kustomize edit set image gke.gcr.io/gcs-fuse-csi-driver-webhook=${WEBHOOK_IMAGE}:${STAGINGVERSION};
	cd ./deploy/overlays/${OVERLAY}; ${BINDIR}/kustomize edit add configmap gcsfusecsi-image-config --behavior=merge --disableNameSuffixHash --from-literal=sidecar-image=${SIDECAR_IMAGE}:${STAGINGVERSION};
	cd ./deploy/overlays/${OVERLAY}; ${BINDIR}/kustomize edit add configmap gcsfusecsi-image-config --behavior=merge --disableNameSuffixHash --from-literal=metadata-sidecar-image=${PREFETCH_IMAGE}:${STAGINGVERSION};
	echo "[{\"op\": \"replace\",\"path\": \"/spec/tokenRequests/0/audience\",\"value\": \"${IDENTITY_POOL}\"}]" > ./deploy/overlays/${OVERLAY}/project_patch_csi_driver.json
	echo "[{\"op\": \"replace\",\"path\": \"/webhooks/0/clientConfig/caBundle\",\"value\": \"${CA_BUNDLE}\"}]" > ./deploy/overlays/${OVERLAY}/caBundle_patch_MutatingWebhookConfiguration.json
	echo "[{\"op\": \"replace\",\"path\": \"/spec/template/spec/containers/0/env/1/value\",\"value\": \"${IDENTITY_PROVIDER}\"}]" > ./deploy/overlays/${OVERLAY}/identity_provider_patch_csi_node.json
	echo "[{\"op\": \"replace\",\"path\": \"/spec/template/spec/containers/0/env/2/value\",\"value\": \"${IDENTITY_POOL}\"}]" > ./deploy/overlays/${OVERLAY}/identity_pool_patch_csi_node.json
ifneq (${SKIP_WI_NODE_LABEL_CHECK}, false)
	echo "[{\"op\": \"add\",\"path\": \"/spec/template/spec/containers/0/args/-\",\"value\": \"--skip-wi-node-label-check=${SKIP_WI_NODE_LABEL_CHECK}\"}]" > ./deploy/overlays/${OVERLAY}/skip_wi_node_label_check_patch.json
	cd ./deploy/overlays/${OVERLAY}; ${BINDIR}/kustomize edit add patch --path skip_wi_node_label_check_patch.json --kind DaemonSet --name gcsfusecsi-node --group apps --version v1;
endif
	kubectl kustomize deploy/overlays/${OVERLAY} | tee ${BINDIR}/gcs-fuse-csi-driver-specs-generated.yaml > /dev/null
	git restore ./deploy/overlays/${OVERLAY}/kustomization.yaml
	git restore ./deploy/overlays/${OVERLAY}/project_patch_csi_driver.json
	git restore ./deploy/overlays/${OVERLAY}/caBundle_patch_MutatingWebhookConfiguration.json
	git restore ./deploy/overlays/${OVERLAY}/identity_provider_patch_csi_node.json
	git restore ./deploy/overlays/${OVERLAY}/identity_pool_patch_csi_node.json
	git restore ./deploy/overlays/${OVERLAY}/skip_wi_node_label_check_patch.json
verify:
	hack/verify-all.sh

unit-test:
	go test -v -mod=vendor -timeout 30s "./pkg/..." -cover

sanity-test:
	cd test && go mod tidy && go test -mod=readonly -v -timeout 30s "./sanity/" -run TestSanity

build-e2e-test:
	cd test && go build -o ../bin/e2e-test-ci ./e2e

e2e-test:
	./test/e2e/run-e2e-local.sh

perf-test:
	$(MAKE) e2e-test E2E_TEST_USE_MANAGED_DRIVER=true E2E_TEST_GINKGO_TIMEOUT=3h E2E_TEST_SKIP= E2E_TEST_FOCUS=should.succeed.in.performance.test E2E_TEST_GINKGO_FLAKE_ATTEMPTS=1
