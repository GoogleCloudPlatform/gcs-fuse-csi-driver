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

# Build gcsfuse
FROM golang:1.19.5 AS gcsfuse-builder

ARG global_ldflags

# Install gcsfuse using the specified version or commit hash
ADD https://api.github.com/repos/GoogleCloudPlatform/gcsfuse/git/refs/heads/master version.json
WORKDIR ${GOPATH}/src/github.com/GoogleCloudPlatform/gcsfuse
RUN git clone -b master https://github.com/GoogleCloudPlatform/gcsfuse.git . -q
RUN go install ./tools/build_gcsfuse
RUN mkdir /tmp/gcsfuse
RUN build_gcsfuse . /tmp/gcsfuse v2.0.0 -ldflags "all=${global_ldflags}" -ldflags "-X main.gcsfuseVersion=v2.0.0 ${global_ldflags}"

# Build sidecar-mounter go binary
FROM golang:1.19.5 as sidecar-mounter-builder

ARG VERSION

WORKDIR /go/src/sigs.k8s.io/gcp-cloud-stroage-csi-driver
ADD . .
RUN make sidecar-mounter BINDIR=/bin

# go/gke-releasing-policies#base-images
# We use `gcr.io/distroless/base` because it includes glibc.
FROM gcr.io/distroless/base-debian11

# Copy the binaries
COPY --from=gcsfuse-builder /tmp/gcsfuse/bin/gcsfuse /gcsfuse
COPY --from=sidecar-mounter-builder /bin/sidecar-mounter /sidecar-mounter

ENTRYPOINT ["/sidecar-mounter"]
