# Copyright 2022 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
BINDIR ?= bin
REGISTRY ?= jiaxun

all: build-image-and-push

driver:
	mkdir -p ${BINDIR}
	CGO_ENABLED=0 GOOS=linux go build -mod vendor -ldflags="-s -w" -o ${BINDIR}/csi-driver cmd/csi_driver/main.go

proxy:
	mkdir -p ${BINDIR}
	CGO_ENABLED=0 GOOS=linux go build -mod vendor -ldflags="-s -w" -o ${BINDIR}/gcsfuse-proxy cmd/gcsfuse_proxy/main.go

build-image-and-push:
	docker build --file Dockerfile --tag ${REGISTRY}/gcp-cloud-storage-csi-driver:v0.1.2 .
	docker push ${REGISTRY}/gcp-cloud-storage-csi-driver:v0.1.2

install:
	kubectl apply -k deploy/overlays/stable

uninstall:
	kubectl delete -k deploy/overlays/stable

dev-gcsfuse-proxy-proto:
	rm -f -r pkg/gcsfuse_proxy/pb
	mkdir pkg/gcsfuse_proxy/pb
	protoc --proto_path=pkg/gcsfuse_proxy/proto --go-grpc_out=pkg/gcsfuse_proxy/pb --go_out=pkg/gcsfuse_proxy/pb pkg/gcsfuse_proxy/proto/gcsfuse_proxy.proto

dev-build:
	mkdir -p ${BINDIR}
	go build -mod vendor -o ${BINDIR}/csi-driver cmd/csi_driver/main.go
	go build -mod vendor -o ${BINDIR}/gcsfuse-proxy cmd/gcsfuse_proxy/main.go

dev-build-image-and-push:
	docker build --file Dockerfile --tag ${REGISTRY}/gcp-cloud-storage-csi-driver:v2.0.0 .
	docker push ${REGISTRY}/gcp-cloud-storage-csi-driver:v2.0.0

dev-generate-yaml:
	kubectl kustomize deploy/overlays/dev | tee ./bin/gcp-cloud-storage-csi-driver-specs-generated.yaml

dev-install:
	kubectl apply -k deploy/overlays/dev

dev-uninstall:
	kubectl delete -k deploy/overlays/dev

verify:
	hack/verify-all.sh

unit-test:
	go test -mod=vendor -timeout 30s -v -cover "./pkg/..."

sanity-test:
	go test -v -mod=vendor -timeout 30s "./test/sanity/" -run TestSanity
