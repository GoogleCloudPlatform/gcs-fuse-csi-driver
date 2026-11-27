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
	"net"
	"strings"
	"sync"
	"testing"

	control "cloud.google.com/go/storage/control/apiv2"
	"cloud.google.com/go/storage/control/apiv2/controlpb"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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

	mu       sync.Mutex
	requests []*controlpb.GetStorageLayoutRequest
}

func (s *fakeStorageControlServer) GetStorageLayout(_ context.Context, req *controlpb.GetStorageLayoutRequest) (*controlpb.StorageLayout, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)

	return &controlpb.StorageLayout{Name: req.GetName()}, nil
}

func (s *fakeStorageControlServer) getRequests() []*controlpb.GetStorageLayoutRequest {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]*controlpb.GetStorageLayoutRequest(nil), s.requests...)
}

func newTestGCSService(t *testing.T, server *fakeStorageControlServer) *gcsService {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	srv := grpc.NewServer()
	controlpb.RegisterStorageControlServer(srv, server)
	go func() {
		_ = srv.Serve(lis)
	}()
	t.Cleanup(srv.Stop)

	controlClient, err := control.NewStorageControlClient(context.Background(),
		option.WithEndpoint(lis.Addr().String()),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())))
	if err != nil {
		t.Fatalf("failed to create the storage control client: %v", err)
	}
	t.Cleanup(func() {
		_ = controlClient.Close()
	})

	return &gcsService{controlClient: controlClient}
}

func TestCheckBucketExistsChecksAccessOnOnlyDir(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		expectedPrefix string
	}{
		{
			name:           "whole bucket",
			expectedPrefix: "",
		},
		{
			name:           "only-dir",
			expectedPrefix: "data/",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := &fakeStorageControlServer{}
			service := newTestGCSService(t, server)

			exist, err := service.CheckBucketExists(context.Background(), &ServiceBucket{Name: "test-bucket", Prefix: test.expectedPrefix})
			if !exist || err != nil {
				t.Fatalf("CheckBucketExists() = %v, %v, expected the check to pass", exist, err)
			}

			requests := server.getRequests()
			if len(requests) != 1 {
				t.Fatalf("got %d GetStorageLayout requests, expected 1", len(requests))
			}
			if got, expected := requests[0].GetName(), "projects/_/buckets/test-bucket/storageLayout"; got != expected {
				t.Errorf("GetStorageLayout name = %q, expected %q", got, expected)
			}
			if got := requests[0].GetPrefix(); got != test.expectedPrefix {
				t.Errorf("GetStorageLayout prefix = %q, expected %q", got, test.expectedPrefix)
			}
		})
	}
}
