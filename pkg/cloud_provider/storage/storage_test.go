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
	"strings"
	"testing"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
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
				SizeBytes: 10 * util.Mb,
			},
			b: &ServiceBucket{
				Name:      "name",
				Project:   "project",
				Location:  "location",
				SizeBytes: 10 * util.Mb,
			},
		},
		{
			name: "nothing matches",
			a: &ServiceBucket{
				Name:      "name1",
				Project:   "project1",
				Location:  "location1",
				SizeBytes: 10 * util.Mb,
			},
			b: &ServiceBucket{
				Name:      "name2",
				Project:   "project2",
				Location:  "location2",
				SizeBytes: 20 * util.Mb,
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
