# Copyright 2018 The Kubernetes Authors.
# Copyright 2024 Google LLC
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

# Build metadata-prefetch go binary
FROM --platform=$BUILDPLATFORM golang:1.22.7 AS metadata-prefetch-builder

ARG STAGINGVERSION

WORKDIR /gcs-fuse-csi-driver
ADD . .
RUN make metadata-prefetch BINDIR=/bin

# go/gke-releasing-policies#base-images
FROM gke.gcr.io/debian-base:bookworm-v1.0.4-gke.2 AS debian

# go/gke-releasing-policies#base-images
FROM  gcr.io/distroless/base-debian12 AS output-image

# Copy existing binaries.
COPY --from=debian /bin/ls /bin/ls

# Copy dependencies.
COPY --from=debian /lib/x86_64-linux-gnu/libselinux.so.1 /lib/x86_64-linux-gnu/libselinux.so.1
COPY --from=debian /lib/x86_64-linux-gnu/libc.so.6 /lib/x86_64-linux-gnu/libc.so.6
COPY --from=debian /lib/x86_64-linux-gnu/libpcre2-8.so.0 /lib/x86_64-linux-gnu/libpcre2-8.so.0
COPY --from=debian /lib64/ld-linux-x86-64.so.2 /lib64/ld-linux-x86-64.so.2 

# Validate dependencies
FROM output-image AS validator-image
COPY --from=debian /bin/bash /bin/bash
COPY --from=debian /usr/bin/ldd /usr/bin/ldd
COPY --from=debian /bin/grep /bin/grep
SHELL ["/bin/bash", "-c"]
RUN if ldd /bin/ls | grep "not found"; then echo "!!! Missing deps for ls command !!!" && exit 1; fi

# Final image
FROM output-image

# Copy the built binaries
COPY --from=metadata-prefetch-builder /bin/gcs-fuse-csi-driver-metadata-prefetch /gcs-fuse-csi-driver-metadata-prefetch

ENTRYPOINT ["/gcs-fuse-csi-driver-metadata-prefetch"]
