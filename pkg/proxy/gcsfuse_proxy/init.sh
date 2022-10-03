#!/bin/bash

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

set -xe

FORCE_INSTALL_GCSFUSE_PROXY=${FORCE_INSTALL_GCSFUSE_PROXY:-true}
INSTALL_GCSFUSE_PROXY=${INSTALL_GCSFUSE_PROXY:-true}
INSTALL_GCSFUSE=${INSTALL_GCSFUSE:-true}

HOST_CMD="nsenter --mount=/proc/1/ns/mnt"

# install gcsfuse
if [ "${INSTALL_GCSFUSE}" = "true" ]
then
  $HOST_CMD wget https://raw.githubusercontent.com/libfuse/libfuse/master/util/fuse.conf -O /etc/fuse.conf
  $HOST_CMD apt-get update
  $HOST_CMD apt-get install cgroup-tools fuse3 -y

  # replace gcsfuse with the fork build
  cp /gcsfuse/bin/* /host/usr/local/bin/
  cp /gcsfuse/sbin/* /host/sbin/

  echo "user_allow_other" >> /host/etc/fuse.conf
  $HOST_CMD mkdir /var/log/gcsfuse -p
fi

# enable GCS Fuse Proxy Server
if [ "${FORCE_INSTALL_GCSFUSE_PROXY}" = "true" ]
then
  echo "stop gcsfuse-proxy systemd service..."
  $HOST_CMD systemctl stop gcsfuse-proxy.service
  $HOST_CMD systemctl disable gcsfuse-proxy.service
  rm -f /host/usr/bin/gcsfuse-proxy
  rm -f /host/etc/systemd/system/gcsfuse-proxy.service
  $HOST_CMD systemctl daemon-reload
fi

if [ ! -f "/host/usr/bin/gcsfuse-proxy" ];then
  echo "copy gcsfuse-proxy...."
  cp /gcsfuse-proxy/gcsfuse-proxy /host/usr/bin/gcsfuse-proxy
  chmod 755 /host/usr/bin/gcsfuse-proxy
fi

if [ ! -f "/host/etc/systemd/system/gcsfuse-proxy.service" ];then
  echo "copy gcsfuse-proxy.service...."
  mkdir -p /host/etc/systemd/system
  cp /gcsfuse-proxy/gcsfuse-proxy.service /host/etc/systemd/system/gcsfuse-proxy.service
fi

# enable GCS Fuse Proxy Server
if [ "${INSTALL_GCSFUSE_PROXY}" = "true" ]
then
  echo "start gcsfuse-proxy systemd service..."
  $HOST_CMD systemctl daemon-reload
  $HOST_CMD systemctl enable gcsfuse-proxy.service
  $HOST_CMD systemctl start gcsfuse-proxy.service
fi
