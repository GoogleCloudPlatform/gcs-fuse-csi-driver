/*
Copyright 2018 The Kubernetes Authors.
Copyright 2022 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package storage

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	control "cloud.google.com/go/storage/control/apiv2"
	"cloud.google.com/go/storage/control/apiv2/controlpb"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestCompareBuckets(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name               string
		a                  *ServiceBucket
		b                  *ServiceBucket
		expectedMismatches []string
	}{
		{
			name: "matches equal",
			a: &ServiceBucket{
				Name:      "name",
				Project:   "project",
				Location:  "location",
				SizeBytes: 10 * util.MiB,
			},
			b: &ServiceBucket{
				Name:      "name",
				Project:   "project",
				Location:  "location",
				SizeBytes: 10 * util.MiB,
			},
		},
		{
			name: "nothing matches",
			a: &ServiceBucket{
				Name:      "name1",
				Project:   "project1",
				Location:  "location1",
				SizeBytes: 10 * util.MiB,
			},
			b: &ServiceBucket{
				Name:      "name2",
				Project:   "project2",
				Location:  "location2",
				SizeBytes: 20 * util.MiB,
			},
			expectedMismatches: []string{
				"bucket name",
				"bucket project",
				"bucket location",
				"bucket size",
			},
		},
	}

	for _, test := range cases {
		err := CompareBuckets(test.a, test.b)
		if len(test.expectedMismatches) == 0 {
			if err != nil {
				t.Errorf("test %v failed:\nexpected match,\ngot error %q", test.name, err)
			}
		} else {
			if err == nil {
				t.Errorf("test %v failed:\nexpected mismatches %q,\ngot success", test.expectedMismatches, test.name)
			} else {
				missedMismatches := []string{}
				for _, mismatch := range test.expectedMismatches {
					if !strings.Contains(err.Error(), mismatch) {
						missedMismatches = append(missedMismatches, mismatch)
					}
				}
				if len(missedMismatches) > 0 {
					t.Errorf("test %q failed:\nexpected mismatches %q,\nmissed mismatches %q", test.name, test.expectedMismatches, missedMismatches)
				}
			}
		}
	}
}

type fakeStorageControlServer struct {
	controlpb.UnimplementedStorageControlServer
	mu          sync.Mutex
	authByName  map[string][]string
	handlerFunc func(ctx context.Context, req *controlpb.GetStorageLayoutRequest) (*controlpb.StorageLayout, error)
}

func (s *fakeStorageControlServer) GetStorageLayout(ctx context.Context, req *controlpb.GetStorageLayoutRequest) (*controlpb.StorageLayout, error) {
	var authHeader string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("authorization"); len(vals) > 0 {
			authHeader = vals[0]
		}
	}
	s.mu.Lock()
	if s.authByName == nil {
		s.authByName = make(map[string][]string)
	}
	s.authByName[req.GetName()] = append(s.authByName[req.GetName()], authHeader)
	handler := s.handlerFunc
	s.mu.Unlock()

	if handler != nil {
		return handler(ctx, req)
	}
	return &controlpb.StorageLayout{Name: req.GetName()}, nil
}

func (s *fakeStorageControlServer) getAuthHeaders(name string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.authByName[name]...)
}

func startFakeTLSStorageControlServer(t *testing.T) (string, option.ClientOption, *fakeStorageControlServer) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
		IsCA:         true,
		BasicConstraintsValid: true,
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}
	leafCert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		t.Fatalf("failed to parse certificate: %v", err)
	}
	tlsCert := tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  priv,
		Leaf:        leafCert,
	}
	certPool := x509.NewCertPool()
	certPool.AddCert(leafCert)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewServerTLSFromCert(&tlsCert)))
	fakeSrv := &fakeStorageControlServer{}
	controlpb.RegisterStorageControlServer(srv, fakeSrv)
	go func() {
		_ = srv.Serve(lis)
	}()
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})

	clientOpt := option.WithGRPCDialOption(grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(certPool, "")))
	return lis.Addr().String(), clientOpt, fakeSrv
}

type countingTokenSource struct {
	token string
	calls atomic.Int32
}

func (c *countingTokenSource) Token() (*oauth2.Token, error) {
	c.calls.Add(1)
	return &oauth2.Token{
		AccessToken: c.token,
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(time.Hour),
	}, nil
}

func TestSetupServiceAndCheckBucketExists_NoStorageClientAndReusesControlClient(t *testing.T) {
	t.Parallel()
	endpoint1, clientOpt1, _ := startFakeTLSStorageControlServer(t)
	endpoint2, _, _ := startFakeTLSStorageControlServer(t)

	mgr := &gcsServiceManager{
		controlClients:           make(map[string]*control.StorageControlClient),
		defaultControlClientOpts: []option.ClientOption{clientOpt1},
	}

	ts1 := &countingTokenSource{token: "tok-1"}
	ts2 := &countingTokenSource{token: "tok-2"}

	if _, err := mgr.SetupService(context.Background(), nil, endpoint1); err == nil {
		t.Fatalf("expected SetupService with nil TokenSource to return an error")
	}

	svc1, err := mgr.SetupService(context.Background(), ts1, endpoint1)
	if err != nil {
		t.Fatalf("SetupService(svc1) failed: %v", err)
	}
	defer svc1.Close()

	svc2, err := mgr.SetupService(context.Background(), ts2, endpoint1)
	if err != nil {
		t.Fatalf("SetupService(svc2) failed: %v", err)
	}
	defer svc2.Close()

	svcDifferentEndpoint, err := mgr.SetupService(context.Background(), ts1, endpoint2)
	if err != nil {
		t.Fatalf("SetupService(svcDifferentEndpoint) failed: %v", err)
	}
	defer svcDifferentEndpoint.Close()

	g1 := svc1.(*gcsService)
	g2 := svc2.(*gcsService)
	gDiff := svcDifferentEndpoint.(*gcsService)

	if g1.storageClient != nil {
		t.Errorf("expected g1.storageClient to be nil after SetupService, got %v", g1.storageClient)
	}
	if g2.storageClient != nil {
		t.Errorf("expected g2.storageClient to be nil after SetupService, got %v", g2.storageClient)
	}
	if g1.controlClient == nil || g1.controlClient != g2.controlClient {
		t.Errorf("expected svc1 and svc2 with same endpoint to share controlClient, got %p vs %p", g1.controlClient, g2.controlClient)
	}
	if g1.controlClient == gDiff.controlClient {
		t.Errorf("expected different endpoints to have distinct controlClients")
	}

	exists, err := svc1.CheckBucketExists(context.Background(), &ServiceBucket{Name: "bkt-1"})
	if err != nil || !exists {
		t.Fatalf("svc1.CheckBucketExists failed: exists=%v, err=%v", exists, err)
	}
	if g1.storageClient != nil {
		t.Errorf("expected g1.storageClient to remain nil after CheckBucketExists, got %v", g1.storageClient)
	}

	sidecarSvc, err := mgr.SetupStorageServiceForSidecar(context.Background(), ts1, endpoint1)
	if err != nil {
		t.Fatalf("SetupStorageServiceForSidecar failed: %v", err)
	}
	defer sidecarSvc.Close()
	if sidecarSvc.(*gcsService).storageClient != nil {
		t.Errorf("expected sidecarSvc.storageClient to be nil after SetupStorageServiceForSidecar")
	}
}

func TestCheckBucketExists_PerCallTokenSourceIsolationAndReuse(t *testing.T) {
	t.Parallel()
	endpoint, clientOpt, fakeSrv := startFakeTLSStorageControlServer(t)

	mgr := &gcsServiceManager{
		controlClients:           make(map[string]*control.StorageControlClient),
		defaultControlClientOpts: []option.ClientOption{clientOpt},
	}

	tsPodA := &countingTokenSource{token: "token-pod-a"}
	tsPodB := &countingTokenSource{token: "token-pod-b"}

	svcA, err := mgr.SetupService(context.Background(), tsPodA, endpoint)
	if err != nil {
		t.Fatalf("SetupService(Pod A) failed: %v", err)
	}
	defer svcA.Close()

	svcB, err := mgr.SetupService(context.Background(), tsPodB, endpoint)
	if err != nil {
		t.Fatalf("SetupService(Pod B) failed: %v", err)
	}
	defer svcB.Close()

	if svcA.(*gcsService).controlClient != svcB.(*gcsService).controlClient {
		t.Fatalf("expected Pod A and Pod B to share the same controlClient")
	}

	if ok, err := svcA.CheckBucketExists(context.Background(), &ServiceBucket{Name: "bucket-a"}); err != nil || !ok {
		t.Fatalf("svcA.CheckBucketExists failed: ok=%v, err=%v", ok, err)
	}
	if ok, err := svcB.CheckBucketExists(context.Background(), &ServiceBucket{Name: "bucket-b"}); err != nil || !ok {
		t.Fatalf("svcB.CheckBucketExists failed: ok=%v, err=%v", ok, err)
	}

	authA := fakeSrv.getAuthHeaders("projects/_/buckets/bucket-a/storageLayout")
	if len(authA) != 1 || authA[0] != "Bearer token-pod-a" {
		t.Errorf("unexpected authorization header for Pod A: got %v, want [\"Bearer token-pod-a\"]", authA)
	}
	authB := fakeSrv.getAuthHeaders("projects/_/buckets/bucket-b/storageLayout")
	if len(authB) != 1 || authB[0] != "Bearer token-pod-b" {
		t.Errorf("unexpected authorization header for Pod B: got %v, want [\"Bearer token-pod-b\"]", authB)
	}

	if got := tsPodA.calls.Load(); got != 1 {
		t.Errorf("expected tsPodA.Token() to be called once across SetupService + CheckBucketExists, got %d", got)
	}
	if got := tsPodB.calls.Load(); got != 1 {
		t.Errorf("expected tsPodB.Token() to be called once across SetupService + CheckBucketExists, got %d", got)
	}
}

func TestClose_DoesNotBreakSharedControlClient(t *testing.T) {
	t.Parallel()
	endpoint, clientOpt, _ := startFakeTLSStorageControlServer(t)

	mgr := &gcsServiceManager{
		controlClients:           make(map[string]*control.StorageControlClient),
		defaultControlClientOpts: []option.ClientOption{clientOpt},
	}

	svc1, err := mgr.SetupService(context.Background(), &countingTokenSource{token: "tok-1"}, endpoint)
	if err != nil {
		t.Fatalf("SetupService(svc1) failed: %v", err)
	}
	svc2, err := mgr.SetupService(context.Background(), &countingTokenSource{token: "tok-2"}, endpoint)
	if err != nil {
		t.Fatalf("SetupService(svc2) failed: %v", err)
	}
	defer svc2.Close()

	if ok, err := svc1.CheckBucketExists(context.Background(), &ServiceBucket{Name: "bkt-1"}); err != nil || !ok {
		t.Fatalf("svc1.CheckBucketExists failed: ok=%v, err=%v", ok, err)
	}
	// Closing svc1 (even multiple times) must not close the shared StorageControlClient.
	svc1.Close()
	svc1.Close()

	if ok, err := svc2.CheckBucketExists(context.Background(), &ServiceBucket{Name: "bkt-2"}); err != nil || !ok {
		t.Fatalf("svc2.CheckBucketExists after svc1.Close() failed: ok=%v, err=%v", ok, err)
	}

	svc3, err := mgr.SetupService(context.Background(), &countingTokenSource{token: "tok-3"}, endpoint)
	if err != nil {
		t.Fatalf("SetupService(svc3) failed: %v", err)
	}
	defer svc3.Close()
	if ok, err := svc3.CheckBucketExists(context.Background(), &ServiceBucket{Name: "bkt-3"}); err != nil || !ok {
		t.Fatalf("svc3.CheckBucketExists after svc1.Close() failed: ok=%v, err=%v", ok, err)
	}

	// Non-shared sidecar service Close() is also idempotent and nils out its controlClient.
	sidecarSvc, err := mgr.SetupStorageServiceForSidecar(context.Background(), &countingTokenSource{token: "tok-sidecar"}, endpoint)
	if err != nil {
		t.Fatalf("SetupStorageServiceForSidecar failed: %v", err)
	}
	sidecarSvc.Close()
	sidecarSvc.Close()
	if _, err := sidecarSvc.CheckBucketExists(context.Background(), &ServiceBucket{Name: "bkt-sidecar"}); err == nil {
		t.Fatalf("expected CheckBucketExists on closed non-shared sidecar service to fail")
	}
}

func TestSetupServiceAndCheckBucketExists_ConcurrentRaceFree(t *testing.T) {
	t.Parallel()
	endpoint, clientOpt, fakeSrv := startFakeTLSStorageControlServer(t)
	fakeSrv.handlerFunc = func(ctx context.Context, req *controlpb.GetStorageLayoutRequest) (*controlpb.StorageLayout, error) {
		var authHeader string
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if vals := md.Get("authorization"); len(vals) > 0 {
				authHeader = vals[0]
			}
		}
		// Expect req.Name == "projects/_/buckets/bucket-<id>/storageLayout" and authHeader == "Bearer token-<id>"
		var id int
		if _, err := fmt.Sscanf(req.GetName(), "projects/_/buckets/bucket-%d/storageLayout", &id); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "unexpected name %q: %v", req.GetName(), err)
		}
		wantAuth := fmt.Sprintf("Bearer token-%d", id)
		if authHeader != wantAuth {
			return nil, status.Errorf(codes.Unauthenticated, "got auth %q, want %q", authHeader, wantAuth)
		}
		return &controlpb.StorageLayout{Name: req.GetName()}, nil
	}

	mgr := &gcsServiceManager{
		controlClients:           make(map[string]*control.StorageControlClient),
		defaultControlClientOpts: []option.ClientOption{clientOpt},
	}

	const numWorkers = 32
	var wg sync.WaitGroup
	errCh := make(chan error, numWorkers)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ts := &countingTokenSource{token: fmt.Sprintf("token-%d", id)}
			svc, err := mgr.SetupService(context.Background(), ts, endpoint)
			if err != nil {
				errCh <- fmt.Errorf("worker %d SetupService: %w", id, err)
				return
			}
			defer svc.Close()

			ok, err := svc.CheckBucketExists(context.Background(), &ServiceBucket{Name: fmt.Sprintf("bucket-%d", id)})
			if err != nil || !ok {
				errCh <- fmt.Errorf("worker %d CheckBucketExists: ok=%v, err=%w", id, ok, err)
				return
			}
		}(i)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}
