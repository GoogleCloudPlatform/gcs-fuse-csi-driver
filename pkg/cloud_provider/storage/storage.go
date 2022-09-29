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

package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog"
)

type ServiceBucket struct {
	Project   string
	Name      string
	Location  string
	SizeBytes int64
	Labels    map[string]string
}

type Service interface {
	CreateBucket(ctx context.Context, b *ServiceBucket) (*ServiceBucket, error)
	GetBucket(ctx context.Context, b *ServiceBucket) (*ServiceBucket, error)
	DeleteBucket(ctx context.Context, b *ServiceBucket) error
}

type ServiceManager interface {
	SetupService(ctx context.Context, ts oauth2.TokenSource) (Service, error)
}

type gcsService struct {
	storageClient *storage.Client
}

type gcsServiceManager struct {
}

func NewGCSServiceManager() (ServiceManager, error) {
	return &gcsServiceManager{}, nil
}

func (manager *gcsServiceManager) SetupService(ctx context.Context, ts oauth2.TokenSource) (Service, error) {
	if err := wait.PollImmediate(5*time.Second, 30*time.Second, func() (bool, error) {
		if _, err := ts.Token(); err != nil {
			klog.Errorf("error fetching initial token: %v", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return nil, err
	}

	client := oauth2.NewClient(ctx, ts)
	storageClient, err := storage.NewClient(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, err
	}
	return &gcsService{storageClient: storageClient}, nil
}

func (service *gcsService) CreateBucket(ctx context.Context, obj *ServiceBucket) (*ServiceBucket, error) {
	klog.V(4).Infof("Creating bucket %q: project %q, location %q", obj.Name, obj.Project, obj.Location)
	// Create the bucket
	bkt := service.storageClient.Bucket(obj.Name)
	if err := bkt.Create(ctx, obj.Project, &storage.BucketAttrs{Location: obj.Location, Labels: obj.Labels}); err != nil {
		return nil, fmt.Errorf("CreateBucket operation failed for bucket %q: %v", obj.Name, err)
	}

	// Check that the bucket exists
	bucket, err := service.GetBucket(ctx, obj)
	if err != nil {
		return nil, fmt.Errorf("failed to get bucket %q after creation: %v", obj.Name, err)
	}
	return bucket, nil
}

func (service *gcsService) DeleteBucket(ctx context.Context, obj *ServiceBucket) error {
	// Check that the bucket exists
	_, err := service.GetBucket(ctx, obj)
	if err != nil {
		if IsNotExistErr(err) {
			return nil
		}
		return fmt.Errorf("failed to get bucket %q before deletion: %v", obj.Name, err)
	}

	// Delete the bucket
	bkt := service.storageClient.Bucket(obj.Name)
	err = bkt.Delete(ctx)
	if err != nil {
		return fmt.Errorf("failed to delete bucket %q: %v", obj.Name, err)
	}
	return nil
}

func (service *gcsService) GetBucket(ctx context.Context, obj *ServiceBucket) (*ServiceBucket, error) {
	bkt := service.storageClient.Bucket(obj.Name)
	attrs, err := bkt.Attrs(ctx)
	if err != nil {
		klog.Errorf("Failed to get bucket %q: %v", obj.Name, err)
		// returns the original error because func IsNotExistErr relies on the error message
		return nil, err
	}

	if attrs != nil {
		return cloudBucketToServiceBucket(attrs)
	}
	return nil, fmt.Errorf("failed to get bucket %q: got empty attrs", obj.Name)
}

func cloudBucketToServiceBucket(attrs *storage.BucketAttrs) (*ServiceBucket, error) {
	return &ServiceBucket{
		Location: attrs.Location,
		Name:     attrs.Name,
		Labels:   attrs.Labels,
	}, nil
}

func CompareBuckets(a, b *ServiceBucket) error {
	mismatches := []string{}
	if a.Name != b.Name {
		mismatches = append(mismatches, "bucket name")
	}
	if a.Project != b.Project {
		mismatches = append(mismatches, "bucket project")
	}
	if a.Location != b.Location {
		mismatches = append(mismatches, "bucket location")
	}
	if a.SizeBytes != b.SizeBytes {
		mismatches = append(mismatches, "bucket size")
	}

	if len(mismatches) > 0 {
		return fmt.Errorf("bucket %q and bucket %q do not match: [%s]", a.Name, b.Name, strings.Join(mismatches, ", "))
	}
	return nil
}

func IsNotExistErr(err error) bool {
	return err == storage.ErrBucketNotExist
}
