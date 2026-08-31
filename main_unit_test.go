//go:build unit

/*
Copyright 2020 Kohl's Department Stores, Inc.
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
	"testing"

	"github.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter/bigquerydb"
	"github.com/stretchr/testify/assert"
)

func TestParsePromotedLabelsValid(t *testing.T) {
	testCases := map[string]struct {
		raw      string
		expected []bigquerydb.PromotedColumn
	}{
		"empty": {
			raw: "", expected: nil,
		},
		"whitespace only": {
			raw: "   ", expected: nil,
		},
		"single pair": {
			raw:      "hostname:instance",
			expected: []bigquerydb.PromotedColumn{{Column: "hostname", Label: "instance"}},
		},
		"multiple pairs": {
			raw: "hostname:instance,cluster:cluster",
			expected: []bigquerydb.PromotedColumn{
				{Column: "hostname", Label: "instance"},
				{Column: "cluster", Label: "cluster"},
			},
		},
		"surrounding whitespace tolerated": {
			raw: "hostname:instance, cluster:cluster ",
			expected: []bigquerydb.PromotedColumn{
				{Column: "hostname", Label: "instance"},
				{Column: "cluster", Label: "cluster"},
			},
		},
		"strip-port modifier": {
			raw:      "hostname:instance|strip-port",
			expected: []bigquerydb.PromotedColumn{{Column: "hostname", Label: "instance", StripPort: true}},
		},
		"omit-empty modifier": {
			raw:      "cluster:cluster|omit-empty",
			expected: []bigquerydb.PromotedColumn{{Column: "cluster", Label: "cluster", OmitEmpty: true}},
		},
		"both modifiers": {
			raw:      "hostname:instance|strip-port|omit-empty",
			expected: []bigquerydb.PromotedColumn{{Column: "hostname", Label: "instance", StripPort: true, OmitEmpty: true}},
		},
		"one label into two columns is allowed": {
			raw: "hostname:instance,node:instance",
			expected: []bigquerydb.PromotedColumn{
				{Column: "hostname", Label: "instance"},
				{Column: "node", Label: "instance"},
			},
		},
		"underscore identifiers": {
			raw:      "host_name:some_label",
			expected: []bigquerydb.PromotedColumn{{Column: "host_name", Label: "some_label"}},
		},
		"empty entries skipped": {
			raw:      "hostname:instance,,",
			expected: []bigquerydb.PromotedColumn{{Column: "hostname", Label: "instance"}},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got, err := parsePromotedLabels(tc.raw)

			assert.NoError(t, err)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestParsePromotedLabelsInvalid(t *testing.T) {
	testCases := map[string]struct {
		raw         string
		errContains string
	}{
		"missing colon":            {raw: "hostname", errContains: "column:label"},
		"too many colons":          {raw: "hostname:instance:extra", errContains: "column:label"},
		"empty label":              {raw: "hostname:", errContains: "non-empty"},
		"empty column":             {raw: ":instance", errContains: "non-empty"},
		"invalid column dash":      {raw: "host-name:instance", errContains: "valid BigQuery column name"},
		"invalid column leading 9": {raw: "9host:instance", errContains: "valid BigQuery column name"},
		"invalid label space":      {raw: "hostname:my label", errContains: "valid Prometheus label name"},
		"invalid label dash":       {raw: "hostname:my-label", errContains: "valid Prometheus label name"},
		"unknown modifier":         {raw: "hostname:instance|lowercase", errContains: `unknown modifier "lowercase"`},
		"duplicate column":         {raw: "hostname:instance,hostname:node", errContains: "mapped more than once"},
		"reserved value":           {raw: "value:instance", errContains: "reserved column name"},
		"reserved metricname":      {raw: "metricname:instance", errContains: "reserved column name"},
		"reserved timestamp":       {raw: "timestamp:instance", errContains: "reserved column name"},
		"reserved tags":            {raw: "tags:instance", errContains: "reserved column name"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got, err := parsePromotedLabels(tc.raw)

			assert.Nil(t, got)
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), tc.errContains)
			}
		})
	}
}

// Every reserved name must be exactly the set of columns a row always carries,
// which is what makes it impossible for a promoted key to shadow a core field.
func TestParsePromotedLabelsRejectsEveryCoreColumn(t *testing.T) {
	for _, column := range bigquerydb.CoreColumns {
		t.Run(column, func(t *testing.T) {
			_, err := parsePromotedLabels(column + ":instance")

			assert.Error(t, err)
		})
	}
}
