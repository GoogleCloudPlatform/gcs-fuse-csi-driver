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

# Build driver go binary
FROM golang:1.19.3 as driver-builder

WORKDIR /go/src/sigs.k8s.io/gcp-cloud-stroage-csi-driver
ADD . .
RUN make driver BINDIR=/bin

FROM launcher.gcr.io/google/debian11
ENV DEBIAN_FRONTEND noninteractive

# https://github.com/opencontainers/image-spec/blob/master/annotations.md
LABEL "org.opencontainers.image.authors"="Jiaxun Song <jiaxun.song@outlook.com>"
LABEL "org.opencontainers.image.description"="The Google Cloud Storage Container Storage Interface (CSI) Plugin"
LABEL "org.opencontainers.image.licenses"="Apache-2.0 OR MIT"
LABEL "org.opencontainers.image.source"="https://github.com/songjiaxun/gcp-cloud-storage-csi-driver"
LABEL "org.opencontainers.image.title"="gcp-cloud-storage-csi-driver"

# Copy the binaries
COPY --from=driver-builder /bin/csi-driver /csi-driver

ENTRYPOINT ["/csi-driver"]
