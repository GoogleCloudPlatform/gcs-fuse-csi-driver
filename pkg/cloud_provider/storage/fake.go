/*
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

	"cloud.google.com/go/storage"
	"golang.org/x/oauth2"
)

type fakeService struct {
	sm fakeServiceManager
}

type fakeServiceManager struct {
	createdBuckets map[string]*ServiceBucket
}

func (manager *fakeServiceManager) SetupService(ctx context.Context, ts oauth2.TokenSource) (Service, error) {
	return &fakeService{sm: *manager}, nil
}

func NewFakeServiceManager() ServiceManager {
	return &fakeServiceManager{createdBuckets: map[string]*ServiceBucket{}}
}

func (service *fakeService) CreateBucket(ctx context.Context, obj *ServiceBucket) (*ServiceBucket, error) {
	sb := &ServiceBucket{
		Project:   obj.Project,
		Location:  obj.Location,
		Name:      obj.Name,
		SizeBytes: obj.SizeBytes,
		Labels:    obj.Labels,
	}

	service.sm.createdBuckets[obj.Name] = sb
	return sb, nil
}

func (service *fakeService) DeleteBucket(ctx context.Context, obj *ServiceBucket) error {
	return nil
}

func (service *fakeService) GetBucket(ctx context.Context, obj *ServiceBucket) (*ServiceBucket, error) {
	if sb, ok := service.sm.createdBuckets[obj.Name]; ok {
		return sb, nil
	}
	return nil, storage.ErrBucketNotExist
}
