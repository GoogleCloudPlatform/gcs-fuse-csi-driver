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

# Build driver go binary
FROM golang:1.19.3 as driver-builder

ARG VERSION

WORKDIR /go/src/sigs.k8s.io/gcp-cloud-stroage-csi-driver
ADD . .
RUN make driver BINDIR=/bin

FROM launcher.gcr.io/google/debian11
ENV DEBIAN_FRONTEND noninteractive

# Copy the binaries
COPY --from=driver-builder /bin/csi-driver /csi-driver

ENTRYPOINT ["/csi-driver"]
