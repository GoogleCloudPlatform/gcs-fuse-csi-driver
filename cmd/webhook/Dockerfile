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

# Build webhook go binary
FROM golang:1.19.5 as webhook-builder

ARG VERSION

WORKDIR /go/src/sigs.k8s.io/gcp-cloud-stroage-csi-driver
ADD . .
RUN make webhook BINDIR=/bin

FROM gcr.io/distroless/static

# Copy the binaries
COPY --from=webhook-builder /bin/webhook /webhook

ENTRYPOINT ["/webhook"]
