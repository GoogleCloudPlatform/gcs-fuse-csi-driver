/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"flag"
	"net"

	"k8s.io/klog"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/proxy/gcsfuse_proxy/auth"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/proxy/gcsfuse_proxy/mount"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/proxy/gcsfuse_proxy/server"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/util"
)

func init() {
	_ = flag.Set("logtostderr", "true")
}

var (
	gcsfuseProxyEndpoint = flag.String("gcsfuse-proxy-endpoint", "unix:/tmp/gcsfuse-proxy.sock", "gcsfuse-proxy endpoint")
	authServerEndpoint   = flag.String("auth-server-endpoint", "unix:/tmp/gcsfuse-auth-server.sock", "gcsfuse-auth-server endpoint")
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	tokenManager := auth.NewTokenManager()
	authServer, err := auth.NewServer(tokenManager)
	if err != nil {
		klog.Fatal(err)
	}

	authScheme, authAddr, err := util.ParseEndpoint(*authServerEndpoint, true)
	if err != nil {
		klog.Fatalf("failed to parse auth-server-endpoint %s: %v", *authServerEndpoint, err.Error())
	}

	authListener, err := net.Listen(authScheme, authAddr)
	if err != nil {
		klog.Fatal("cannot start auth server:", err)
	}
	go func() {
		klog.Fatal(authServer.Serve(authListener))
	}()

	proxyScheme, proxyAddr, err := util.ParseEndpoint(*gcsfuseProxyEndpoint, true)
	if err != nil {
		klog.Fatalf("failed to parse gcsfuse-proxy-endpoint %s: %v", *gcsfuseProxyEndpoint, err.Error())
	}

	listener, err := net.Listen(proxyScheme, proxyAddr)
	if err != nil {
		klog.Fatal("cannot start proxy server:", err)
	}

	mountServer := server.NewGCSFuseProxyServiceServer(tokenManager, *authServerEndpoint, mount.New(""))

	klog.V(2).Infof("Listening for connections on address: %v", listener.Addr())
	if err = server.RunGRPCServer(mountServer, false, listener); err != nil {
		klog.Fatalf("Error running grpc server. Error: %v", err)
	}
}
