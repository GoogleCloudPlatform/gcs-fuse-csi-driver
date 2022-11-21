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
BINDIR ?= bin
REGISTRY ?= jiaxun
VERSION ?= v0.2.0
LDFLAGS ?= "-s -w -X main.version=${VERSION} -extldflags '-static'"
OVERLAY ?= stable

all: build-image-and-push

driver:
	mkdir -p ${BINDIR}
	CGO_ENABLED=0 GOOS=linux go build -mod vendor -ldflags ${LDFLAGS} -o ${BINDIR}/csi-driver cmd/csi_driver/main.go

sidecar-mounter:
	mkdir -p ${BINDIR}
	CGO_ENABLED=0 GOOS=linux go build -mod vendor -ldflags ${LDFLAGS} -o ${BINDIR}/sidecar-mounter cmd/sidecar_mounter/main.go

build-image-and-push:
	docker build --file ./cmd/sidecar_mounter/Dockerfile --tag ${REGISTRY}/gcp-cloud-storage-sidecar-mounter:${VERSION} --build-arg VERSION=${VERSION} .
	docker push ${REGISTRY}/gcp-cloud-storage-sidecar-mounter:${VERSION}
	
	docker build --file ./cmd/csi_driver/Dockerfile --tag ${REGISTRY}/gcp-cloud-storage-csi-driver:${VERSION} --build-arg VERSION=${VERSION} .
	docker push ${REGISTRY}/gcp-cloud-storage-csi-driver:${VERSION}

install:
	kubectl apply -k deploy/overlays/${OVERLAY}

uninstall:
	kubectl delete -k deploy/overlays/${OVERLAY}

dev-generate-yaml:
	kubectl kustomize deploy/overlays/dev | tee ./bin/gcp-cloud-storage-csi-driver-specs-generated.yaml

verify:
	hack/verify-all.sh

unit-test:
	go test -mod=vendor -timeout 30s -v -cover "./pkg/..."

sanity-test:
	go test -v -mod=vendor -timeout 30s "./test/sanity/" -run TestSanity
