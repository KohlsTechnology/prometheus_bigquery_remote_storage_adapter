//go:build unit

/*
Copyright 2022 Kohl's Department Stores, Inc.

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

package version

import (
	"fmt"
	"runtime"
	"testing"
)

// TestGetVersion calls version.Get checking for a valid version string.
func TestGetVersion(t *testing.T) {
	want := fmt.Sprintf("prometheus_bigquery_remote_storage_adapter, version v0.4.7 (branch: , revision: ), build date: , go version: %v", runtime.Version())
	msg := Get()
	if want != msg {
		t.Fatalf("wanted %q, but got %q", want, msg)
	}
}
