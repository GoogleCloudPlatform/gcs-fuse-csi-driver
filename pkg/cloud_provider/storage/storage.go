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
	"errors"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/iam"
	"cloud.google.com/go/storage"
	"golang.org/x/oauth2"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

type ServiceBucket struct {
	Project                        string
	Name                           string
	Location                       string
	SizeBytes                      int64
	Labels                         map[string]string
	EnableUniformBucketLevelAccess bool
	EnableHierarchicalNamespace    bool
	EnableZB 					   bool // Enable Zonal Buckets
}

type Service interface {
	CreateBucket(ctx context.Context, b *ServiceBucket) (*ServiceBucket, error)
	GetBucket(ctx context.Context, b *ServiceBucket) (*ServiceBucket, error)
	DeleteBucket(ctx context.Context, b *ServiceBucket) error
	SetIAMPolicy(ctx context.Context, obj *ServiceBucket, member, roleName string) error
	RemoveIAMPolicy(ctx context.Context, obj *ServiceBucket, member, roleName string) error
	CheckBucketExists(ctx context.Context, obj *ServiceBucket) (bool, error)
	Close()
}

type ServiceManager interface {
	SetupService(ctx context.Context, ts oauth2.TokenSource) (Service, error)
	SetupServiceWithDefaultCredential(ctx context.Context) (Service, error)
}

type gcsService struct {
	storageClient *storage.Client
}

type gcsServiceManager struct{}

func NewGCSServiceManager() (ServiceManager, error) {
	return &gcsServiceManager{}, nil
}

func (manager *gcsServiceManager) SetupService(ctx context.Context, ts oauth2.TokenSource) (Service, error) {
	if err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 30*time.Second, true, func(context.Context) (bool, error) {
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

func (manager *gcsServiceManager) SetupServiceWithDefaultCredential(ctx context.Context) (Service, error) {
	storageClient, err := storage.NewClient(ctx)
	if err != nil {
		return nil, err
	}

	return &gcsService{storageClient: storageClient}, nil
}

func (service *gcsService) CreateBucket(ctx context.Context, obj *ServiceBucket) (*ServiceBucket, error) {
	klog.V(4).Infof("Creating bucket %q: project %q, location %q", obj.Name, obj.Project, obj.Location)
	// Create the bucket
	bkt := service.storageClient.Bucket(obj.Name)
	bktAttrs := &storage.BucketAttrs{
		Location:                 obj.Location,
		Labels:                   obj.Labels,
		UniformBucketLevelAccess: storage.UniformBucketLevelAccess{Enabled: obj.EnableUniformBucketLevelAccess},
		HierarchicalNamespace:    &storage.HierarchicalNamespace{Enabled: obj.EnableHierarchicalNamespace},
	}
	if obj.EnableZB {
		bktAttrs.CustomPlacementConfig = &storage.CustomPlacementConfig{
			DataLocations: []string{obj.Location + "-c"},
		}
		bktAttrs.StorageClass = "RAPID"
	}
	if err := bkt.Create(ctx, obj.Project, bktAttrs); err != nil {
		return nil, fmt.Errorf("CreateBucket operation failed for bucket %q: %w", obj.Name, err)
	}

	// Check that the bucket exists
	bucket, err := service.GetBucket(ctx, obj)
	if err != nil {
		return nil, fmt.Errorf("failed to get bucket %q after creation: %w", obj.Name, err)
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

		return fmt.Errorf("failed to get bucket %q before deletion: %w", obj.Name, err)
	}

	// Delete all objects in the bucket first
	bkt := service.storageClient.Bucket(obj.Name)
	it := bkt.Objects(ctx, nil)
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to iterate next object: %w", err)
		}
		if err := bkt.Object(attrs.Name).Delete(ctx); err != nil {
			return fmt.Errorf("failed to delete object %q: %w", attrs.Name, err)
		}
	}

	// Delete the bucket
	err = bkt.Delete(ctx)
	if err != nil {
		return fmt.Errorf("failed to delete bucket %q: %w", obj.Name, err)
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

func (service *gcsService) CheckBucketExists(ctx context.Context, obj *ServiceBucket) (bool, error) {
	bkt := service.storageClient.Bucket(obj.Name)
	_, err := bkt.Objects(ctx, &storage.Query{Prefix: ""}).Next()

	if err == nil || errors.Is(err, iterator.Done) {
		return true, nil
	}

	return false, err
}

func (service *gcsService) SetIAMPolicy(ctx context.Context, obj *ServiceBucket, member, roleName string) error {
	bkt := service.storageClient.Bucket(obj.Name)
	policy, err := bkt.IAM().Policy(ctx)
	if err != nil {
		return fmt.Errorf("failed to get bucket %q IAM policy: %w", obj.Name, err)
	}

	policy.Add(member, iam.RoleName(roleName))
	if err := bkt.IAM().SetPolicy(ctx, policy); err != nil {
		return fmt.Errorf("failed to set bucket %q IAM policy: %w", obj.Name, err)
	}

	return nil
}

func (service *gcsService) RemoveIAMPolicy(ctx context.Context, obj *ServiceBucket, member, roleName string) error {
	bkt := service.storageClient.Bucket(obj.Name)
	policy, err := bkt.IAM().Policy(ctx)
	if err != nil {
		return fmt.Errorf("failed to get bucket %q IAM policy: %w", obj.Name, err)
	}

	policy.Remove(member, iam.RoleName(roleName))
	if err := bkt.IAM().SetPolicy(ctx, policy); err != nil {
		return fmt.Errorf("failed to set bucket %q IAM policy: %w", obj.Name, err)
	}

	return nil
}

func (service *gcsService) Close() {
	service.storageClient.Close()
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
	return errors.Is(err, storage.ErrBucketNotExist)
}

func isPermissionDeniedErr(err error) bool {
	return strings.Contains(err.Error(), "googleapi: Error 403")
}

func isCanceledErr(err error) bool {
	return strings.Contains(err.Error(), "context canceled") || strings.Contains(err.Error(), "context deadline exceeded")
}

// ParseErrCode parses error and returns a gRPC code.
func ParseErrCode(err error) codes.Code {
	code := codes.Internal
	if IsNotExistErr(err) {
		code = codes.NotFound
	}

	if isPermissionDeniedErr(err) {
		code = codes.PermissionDenied
	}

	if isCanceledErr(err) {
		code = codes.Aborted
	}

	return code
}
