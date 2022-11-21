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

package webhook

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func GetSidecarSpec() *v1.PodSpec {
	spec := &v1.PodSpec{
		Containers: []v1.Container{
			{
				Name:  "gke-gcsfuse-sidecar",
				Image: "jiaxun/gcs-fuse-csi-driver-sidecar-mounter",
				SecurityContext: &v1.SecurityContext{
					AllowPrivilegeEscalation: func(b bool) *bool { return &b }(false),
					RunAsUser:                func(i int64) *int64 { return &i }(0),
					RunAsGroup:               func(i int64) *int64 { return &i }(0),
				},
				Args: []string{"--v=5"},
				Resources: v1.ResourceRequirements{
					Limits: v1.ResourceList{
						v1.ResourceCPU:              resource.MustParse("100m"),
						v1.ResourceMemory:           resource.MustParse("30Mi"),
						v1.ResourceEphemeralStorage: resource.MustParse("5Gi"),
					},
					Requests: v1.ResourceList{
						v1.ResourceCPU:              resource.MustParse("100m"),
						v1.ResourceMemory:           resource.MustParse("30Mi"),
						v1.ResourceEphemeralStorage: resource.MustParse("5Gi"),
					},
				},
				VolumeMounts: []v1.VolumeMount{
					{
						Name:      "gke-gcsfuse",
						MountPath: "/tmp",
					},
				},
			},
		},
		Volumes: []v1.Volume{
			{
				Name: "gke-gcsfuse",
				VolumeSource: v1.VolumeSource{
					EmptyDir: &v1.EmptyDirVolumeSource{},
				},
			},
		},
	}
	return spec
}

func GetPodWithSidecarSpec() *v1.Pod {
	return &v1.Pod{Spec: *GetSidecarSpec()}
}
