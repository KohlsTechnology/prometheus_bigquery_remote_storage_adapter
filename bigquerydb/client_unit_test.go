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

package bigquerydb

import (
	"encoding/json"
	"testing"

	"cloud.google.com/go/bigquery"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
)

// TestPromotedDisabledKeepsDefaultRowShape is the regression guard for existing
// users: with no promotion configured a row must contain exactly the four core
// keys, so a fifth key can never be introduced by default.
func TestPromotedDisabledKeepsDefaultRowShape(t *testing.T) {
	item := &Item{value: 1.5, metricname: "up", timestamp: 1234, tags: `{"job":"node"}`}

	row, insertID, err := item.Save()

	assert.NoError(t, err)
	assert.Empty(t, insertID)
	assert.Equal(t, map[string]bigquery.Value{
		"value":      1.5,
		"metricname": "up",
		"timestamp":  int64(1234),
		"tags":       `{"job":"node"}`,
	}, row)
	assert.Len(t, row, len(CoreColumns))
}

func TestPromotedSaveAddsColumnsWithoutTouchingCoreFields(t *testing.T) {
	base := &Item{value: 1.5, metricname: "up", timestamp: 1234, tags: `{"job":"node"}`}
	baseRow, _, err := base.Save()
	if err != nil {
		t.Fatal(err)
	}

	item := &Item{
		value: 1.5, metricname: "up", timestamp: 1234, tags: `{"job":"node"}`,
		promoted: map[string]string{"hostname": "web-01.example.net"},
	}
	row, _, err := item.Save()
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, "web-01.example.net", row["hostname"])
	assert.Len(t, row, len(CoreColumns)+1)
	for _, column := range CoreColumns {
		assert.Equal(t, baseRow[column], row[column], "core column %q must be unchanged by promotion", column)
	}
}

func TestPromotedValuesVerbatim(t *testing.T) {
	c := &BigqueryClient{promoted: []PromotedColumn{{Column: "hostname", Label: "instance"}}}

	values := c.promotedValues(model.Metric{
		model.MetricNameLabel: "up",
		"instance":            "au-syd-dc1-b1.cloud.openvpn.net",
	})

	assert.Equal(t, map[string]string{"hostname": "au-syd-dc1-b1.cloud.openvpn.net"}, values)
}

// A port is retained unless strip-port is configured.
func TestPromotedValuesRetainsPortByDefault(t *testing.T) {
	c := &BigqueryClient{promoted: []PromotedColumn{{Column: "hostname", Label: "instance"}}}

	values := c.promotedValues(model.Metric{"instance": "localhost:9090"})

	assert.Equal(t, map[string]string{"hostname": "localhost:9090"}, values)
}

// The REQUIRED-column guarantee: an absent label yields an empty string, never
// a missing key, because REQUIRED forbids NULL but accepts "".
func TestPromotedValuesAbsentLabelWritesEmptyString(t *testing.T) {
	c := &BigqueryClient{promoted: []PromotedColumn{{Column: "hostname", Label: "instance"}}}

	values := c.promotedValues(model.Metric{model.MetricNameLabel: "up"})

	assert.Contains(t, values, "hostname")
	assert.Equal(t, "", values["hostname"])
}

func TestPromotedValuesPreservesEmptyLabelValue(t *testing.T) {
	c := &BigqueryClient{promoted: []PromotedColumn{{Column: "hostname", Label: "instance"}}}

	values := c.promotedValues(model.Metric{"instance": ""})

	assert.Contains(t, values, "hostname")
	assert.Equal(t, "", values["hostname"])
}

func TestPromotedValuesOmitEmptyDropsKey(t *testing.T) {
	c := &BigqueryClient{promoted: []PromotedColumn{{Column: "cluster", Label: "cluster", OmitEmpty: true}}}

	assert.NotContains(t, c.promotedValues(model.Metric{model.MetricNameLabel: "up"}), "cluster")
	assert.Equal(t, map[string]string{"cluster": "prod"}, c.promotedValues(model.Metric{"cluster": "prod"}))
}

func TestPromotedValuesDisabledReturnsNil(t *testing.T) {
	c := &BigqueryClient{}

	assert.Nil(t, c.promotedValues(model.Metric{"instance": "localhost:9090"}))
}

func TestPromotedOneLabelIntoTwoColumns(t *testing.T) {
	c := &BigqueryClient{promoted: []PromotedColumn{
		{Column: "hostname", Label: "instance"},
		{Column: "node", Label: "instance", StripPort: true},
	}}

	values := c.promotedValues(model.Metric{"instance": "web-01.example.net:9100"})

	assert.Equal(t, map[string]string{
		"hostname": "web-01.example.net:9100",
		"node":     "web-01.example.net",
	}, values)
}

func TestPromotedStripPort(t *testing.T) {
	testCases := map[string]struct {
		in       string
		expected string
	}{
		"host and port":          {in: "localhost:9090", expected: "localhost"},
		"fqdn and port":          {in: "web-01.example.net:9100", expected: "web-01.example.net"},
		"already port free":      {in: "au-syd-dc1-b1.cloud.openvpn.net", expected: "au-syd-dc1-b1.cloud.openvpn.net"},
		"ipv4 and port":          {in: "10.0.0.5:9100", expected: "10.0.0.5"},
		"bracketed ipv6 w/ port": {in: "[2001:db8::1]:9100", expected: "[2001:db8::1]"},
		"bracketed ipv6 no port": {in: "[2001:db8::1]", expected: "[2001:db8::1]"},
		"bare ipv6 left alone":   {in: "2001:db8::1", expected: "2001:db8::1"},
		"non numeric suffix":     {in: "host:name", expected: "host:name"},
		"trailing colon":         {in: "host:", expected: "host:"},
		"empty":                  {in: "", expected: ""},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.expected, stripPort(tc.in))
		})
	}
}

// Promotion must not remove the label from the tags JSON, so existing
// JSON_EXTRACT queries and the read path keep working.
func TestPromotedLabelIsRetainedInTags(t *testing.T) {
	metric := model.Metric{
		model.MetricNameLabel: "up",
		"instance":            "localhost:9090",
		"job":                 "node",
	}
	c := &BigqueryClient{promoted: []PromotedColumn{{Column: "hostname", Label: "instance", StripPort: true}}}

	values := c.promotedValues(metric)
	var tags map[string]string
	if err := json.Unmarshal([]byte(tagsFromMetric(metric)), &tags); err != nil {
		t.Fatal(err)
	}

	// The column is stripped, the tags JSON keeps the original value verbatim.
	assert.Equal(t, "localhost", values["hostname"])
	assert.Equal(t, "localhost:9090", tags["instance"])
	assert.Equal(t, "node", tags["job"])
	assert.NotContains(t, tags, model.MetricNameLabel)
}

func TestPromotedMixedBatchPerRowKeys(t *testing.T) {
	c := &BigqueryClient{promoted: []PromotedColumn{{Column: "hostname", Label: "instance"}}}

	withLabel := c.promotedValues(model.Metric{"instance": "web-01.example.net"})
	withoutLabel := c.promotedValues(model.Metric{model.MetricNameLabel: "aggregated"})

	assert.Equal(t, "web-01.example.net", withLabel["hostname"])
	assert.Equal(t, "", withoutLabel["hostname"])
	assert.Contains(t, withoutLabel, "hostname")
}

// The preflight logic is tested against a plain schema value, with no live
// BigQuery table, so a mistyped destination column is covered without GCP.
func TestPromotedSchemaPreflight(t *testing.T) {
	mapping := []PromotedColumn{{Column: "hostname", Label: "instance"}}
	omitEmptyMapping := []PromotedColumn{{Column: "hostname", Label: "instance", OmitEmpty: true}}

	testCases := map[string]struct {
		schema        bigquery.Schema
		promoted      []PromotedColumn
		expectIssues  int
		expectFatal   bool
		reasonKeyword string
	}{
		"correct STRING NULLABLE column": {
			schema:   bigquery.Schema{{Name: "hostname", Type: bigquery.StringFieldType}},
			promoted: mapping,
		},
		"correct STRING REQUIRED column, always written": {
			schema:   bigquery.Schema{{Name: "hostname", Type: bigquery.StringFieldType, Required: true}},
			promoted: mapping,
		},
		"column missing entirely": {
			schema:        bigquery.Schema{{Name: "tags", Type: bigquery.StringFieldType}},
			promoted:      mapping,
			expectIssues:  1,
			reasonKeyword: "does not exist",
		},
		"wrong type INTEGER": {
			schema:        bigquery.Schema{{Name: "hostname", Type: bigquery.IntegerFieldType}},
			promoted:      mapping,
			expectIssues:  1,
			reasonKeyword: "type INTEGER",
		},
		"wrong type TIMESTAMP": {
			schema:        bigquery.Schema{{Name: "hostname", Type: bigquery.TimestampFieldType}},
			promoted:      mapping,
			expectIssues:  1,
			reasonKeyword: "type TIMESTAMP",
		},
		"wrong type RECORD": {
			schema:        bigquery.Schema{{Name: "hostname", Type: bigquery.RecordFieldType}},
			promoted:      mapping,
			expectIssues:  1,
			reasonKeyword: "type RECORD",
		},
		"REPEATED string column": {
			schema:        bigquery.Schema{{Name: "hostname", Type: bigquery.StringFieldType, Repeated: true}},
			promoted:      mapping,
			expectIssues:  1,
			reasonKeyword: "REPEATED",
		},
		"REPEATED and wrong type reports both": {
			schema:       bigquery.Schema{{Name: "hostname", Type: bigquery.IntegerFieldType, Repeated: true}},
			promoted:     mapping,
			expectIssues: 2,
		},
		"REQUIRED with omit-empty is fatal": {
			schema:        bigquery.Schema{{Name: "hostname", Type: bigquery.StringFieldType, Required: true}},
			promoted:      omitEmptyMapping,
			expectIssues:  1,
			expectFatal:   true,
			reasonKeyword: "omit-empty",
		},
		"NULLABLE with omit-empty is fine": {
			schema:   bigquery.Schema{{Name: "hostname", Type: bigquery.StringFieldType}},
			promoted: omitEmptyMapping,
		},
		"no promotion configured yields nothing": {
			schema:   bigquery.Schema{{Name: "hostname", Type: bigquery.IntegerFieldType}},
			promoted: nil,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			issues := checkPromotedColumns(tc.schema, tc.promoted)

			assert.Len(t, issues, tc.expectIssues)

			fatal := false
			for _, issue := range issues {
				if issue.fatal {
					fatal = true
				}
				assert.Equal(t, "hostname", issue.column)
			}
			assert.Equal(t, tc.expectFatal, fatal)

			if tc.reasonKeyword != "" && len(issues) > 0 {
				joined := ""
				for _, issue := range issues {
					joined += issue.reason + "\n"
				}
				assert.Contains(t, joined, tc.reasonKeyword)
			}
		})
	}
}

// BigQuery column names are case-insensitive, so a column configured as
// "Hostname" against a schema field "hostname" is the same column and must not
// be reported as missing. The mismatched case still has to be checked for type
// and mode, since that is the column the rows will actually land in.
func TestCheckPromotedColumnsIsCaseInsensitive(t *testing.T) {
	testCases := map[string]struct {
		schema       bigquery.Schema
		promoted     []PromotedColumn
		expectIssues int
	}{
		"configured upper, declared lower": {
			schema:   bigquery.Schema{{Name: "hostname", Type: bigquery.StringFieldType}},
			promoted: []PromotedColumn{{Column: "Hostname", Label: "instance"}},
		},
		"configured lower, declared upper": {
			schema:   bigquery.Schema{{Name: "Hostname", Type: bigquery.StringFieldType}},
			promoted: []PromotedColumn{{Column: "hostname", Label: "instance"}},
		},
		"case mismatch still validates the type": {
			schema:       bigquery.Schema{{Name: "hostname", Type: bigquery.IntegerFieldType}},
			promoted:     []PromotedColumn{{Column: "Hostname", Label: "instance"}},
			expectIssues: 1,
		},
		"genuinely missing column is still reported": {
			schema:       bigquery.Schema{{Name: "tags", Type: bigquery.StringFieldType}},
			promoted:     []PromotedColumn{{Column: "Hostname", Label: "instance"}},
			expectIssues: 1,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			issues := checkPromotedColumns(tc.schema, tc.promoted)

			assert.Len(t, issues, tc.expectIssues)
			for _, issue := range issues {
				// The issue names the column as the operator configured it.
				assert.Equal(t, tc.promoted[0].Column, issue.column)
			}
		})
	}
}
