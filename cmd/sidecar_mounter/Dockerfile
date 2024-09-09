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

# Build sidecar-mounter go binary
FROM golang:1.22.7 AS sidecar-mounter-builder

ARG STAGINGVERSION

WORKDIR /gcs-fuse-csi-driver
ADD . .
RUN make sidecar-mounter BINDIR=/bin

# go/gke-releasing-policies#base-images
# We use `gcr.io/distroless/base` because it includes glibc.
FROM gcr.io/distroless/base-debian12
ARG TARGETPLATFORM

# Copy the binaries
COPY ./bin/${TARGETPLATFORM}/gcsfuse /gcsfuse
COPY --from=sidecar-mounter-builder /bin/gcs-fuse-csi-driver-sidecar-mounter /gcs-fuse-csi-driver-sidecar-mounter

ENTRYPOINT ["/gcs-fuse-csi-driver-sidecar-mounter"]
