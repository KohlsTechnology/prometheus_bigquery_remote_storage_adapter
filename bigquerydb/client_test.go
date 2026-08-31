//go:build e2e

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
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/prometheus/prometheus/prompb"
	"github.com/stretchr/testify/assert"
	"google.golang.org/api/iterator"
)

var bigQueryClientTimeout = time.Second * 60
var logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

var googleAPIdatasetID = os.Getenv("BQ_DATASET_NAME")
var googleAPItableID = os.Getenv("BQ_TABLE_NAME")
var googleProjectID = os.Getenv("GCP_PROJECT_ID")

func TestLabelMatchers(t *testing.T) {

	nowUnix := time.Now().Unix() * 1000

	timeseriesData := map[string][]*prompb.TimeSeries{
		"first": {&prompb.TimeSeries{
			Labels: []*prompb.Label{
				{
					Name:  "__name__",
					Value: "first_metric",
				},
				{
					Name:  "label",
					Value: "first",
				},
			},
			Samples: []prompb.Sample{
				{
					Timestamp: nowUnix,
					Value:     1,
				},
			},
		}},
		"second": {&prompb.TimeSeries{
			Labels: []*prompb.Label{
				{
					Name:  "__name__",
					Value: "second_metric",
				},
				{
					Name:  "label",
					Value: "second",
				},
			},
			Samples: []prompb.Sample{
				{
					Timestamp: nowUnix,
					Value:     1,
				},
			},
		}},
		"nan": {&prompb.TimeSeries{
			Labels: []*prompb.Label{
				{
					Name:  "__name__",
					Value: "nan_metric",
				},
				{
					Name:  "label",
					Value: "NaN",
				},
			},
			Samples: []prompb.Sample{
				{
					Timestamp: nowUnix,
					Value:     math.NaN(),
				},
			},
		}},
		"emptyResult": {},
	}

	bqclient := NewClient(logger, "", googleProjectID, googleAPIdatasetID, googleAPItableID, bigQueryClientTimeout)

	for _, timeseries := range timeseriesData {
		err := bqclient.Write(timeseries)
		if err != nil {
			t.Fatal("error sending samples", err)
		}
	}

	testCases := map[string]struct {
		matchName      string
		matchValue     string
		matchType      prompb.LabelMatcher_Type
		expectedResult string
	}{
		"metric_name_equals":          {matchName: "__name__", matchValue: "first_metric", matchType: prompb.LabelMatcher_EQ, expectedResult: "first"},
		"metric_name_not_equals":      {matchName: "__name__", matchValue: "first_metric", matchType: prompb.LabelMatcher_NEQ, expectedResult: "second"},
		"metric_name_regex_match":     {matchName: "__name__", matchValue: "fi.*", matchType: prompb.LabelMatcher_RE, expectedResult: "first"},
		"metric_name_regex_not_equal": {matchName: "__name__", matchValue: "fi.*", matchType: prompb.LabelMatcher_NRE, expectedResult: "second"},
		"label_equals":                {matchName: "label", matchValue: "first", matchType: prompb.LabelMatcher_EQ, expectedResult: "first"},
		"label_not_equals":            {matchName: "label", matchValue: "first", matchType: prompb.LabelMatcher_NEQ, expectedResult: "second"},
		"label_regex_match":           {matchName: "label", matchValue: "fi.*", matchType: prompb.LabelMatcher_RE, expectedResult: "first"},
		"label_regex_not_equal":       {matchName: "label", matchValue: "fi.*", matchType: prompb.LabelMatcher_NRE, expectedResult: "second"},
		"nan_timeseries_sample_value": {matchName: "label", matchValue: "NaN", matchType: prompb.LabelMatcher_EQ, expectedResult: "emptyResult"},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			request := prompb.ReadRequest{
				Queries: []*prompb.Query{
					{
						StartTimestampMs: nowUnix,
						EndTimestampMs:   nowUnix + 10000,
						Matchers: []*prompb.LabelMatcher{
							{
								Type:  testCase.matchType,
								Name:  testCase.matchName,
								Value: testCase.matchValue,
							},
						},
					},
				},
			}
			result, err := bqclient.Read(&request)

			assert.Nil(t, err, "failed to process query")
			assert.Len(t, result.Results, 1)
			assert.Equal(t, timeseriesData[testCase.expectedResult], result.Results[0].Timeseries)
		})
	}
}

// TestPromotedLabelColumns exercises promotion against a real table whose
// promoted column is REQUIRED, mirroring production. It provisions and drops
// its own table so bq-schema.json, the Makefile and the CI workflow need no
// changes. This is the direct regression test for a stock binary writing no
// hostname key and BigQuery rejecting every row with "Missing required field".
func TestPromotedLabelColumns(t *testing.T) {
	ctx := context.Background()
	nowUnix := time.Now().Unix() * 1000
	promotedTableID := googleAPItableID + "_promoted"

	client, err := bigquery.NewClient(ctx, googleProjectID)
	if err != nil {
		t.Fatal("failed to create bigquery client", err)
	}
	defer client.Close()

	table := client.Dataset(googleAPIdatasetID).Table(promotedTableID)
	err = table.Create(ctx, &bigquery.TableMetadata{
		Schema: bigquery.Schema{
			{Name: "metricname", Type: bigquery.StringFieldType},
			{Name: "tags", Type: bigquery.StringFieldType},
			{Name: "timestamp", Type: bigquery.TimestampFieldType},
			{Name: "value", Type: bigquery.FloatFieldType},
			// REQUIRED on purpose: an omitted key must be impossible.
			{Name: "hostname", Type: bigquery.StringFieldType, Required: true},
		},
		TimePartitioning: &bigquery.TimePartitioning{Field: "timestamp"},
	})
	if err != nil {
		t.Fatal("failed to create promoted test table", err)
	}
	defer func() {
		if err := table.Delete(ctx); err != nil {
			t.Logf("failed to drop promoted test table %s: %v", promotedTableID, err)
		}
	}()

	withInstance := []*prompb.TimeSeries{{
		Labels: []*prompb.Label{
			{Name: "__name__", Value: "promoted_with_instance"},
			{Name: "instance", Value: "web-01.example.net"},
		},
		Samples: []prompb.Sample{{Timestamp: nowUnix, Value: 1}},
	}}
	// No instance label at all: this is the row a REQUIRED column rejects
	// unless the adapter writes an empty string for it.
	withoutInstance := []*prompb.TimeSeries{{
		Labels: []*prompb.Label{
			{Name: "__name__", Value: "promoted_without_instance"},
		},
		Samples: []prompb.Sample{{Timestamp: nowUnix, Value: 2}},
	}}

	bqclient := NewClient(logger, "", googleProjectID, googleAPIdatasetID, promotedTableID, bigQueryClientTimeout,
		WithPromotedLabels([]PromotedColumn{{Column: "hostname", Label: "instance"}}))

	for _, timeseries := range [][]*prompb.TimeSeries{withInstance, withoutInstance} {
		// A REQUIRED violation surfaces here as a PutMultiError, not silently.
		assert.NoError(t, bqclient.Write(timeseries), "every row must be accepted by a REQUIRED promoted column")
	}

	query := client.Query(fmt.Sprintf(
		"SELECT metricname, hostname FROM %s.%s ORDER BY metricname", googleAPIdatasetID, promotedTableID))
	iter, err := query.Read(ctx)
	if err != nil {
		t.Fatal("failed to query promoted test table", err)
	}

	stored := map[string]string{}
	for {
		row := map[string]bigquery.Value{}
		err := iter.Next(&row)
		if err == iterator.Done {
			break
		}
		if err != nil {
			t.Fatal("failed to read promoted test table", err)
		}
		hostname, _ := row["hostname"].(string)
		metricname, _ := row["metricname"].(string)
		stored[metricname] = hostname
	}

	assert.Equal(t, map[string]string{
		"promoted_with_instance":    "web-01.example.net",
		"promoted_without_instance": "",
	}, stored)

	// The promoted label must still be in tags, so the read path round-trips.
	result, err := bqclient.Read(&prompb.ReadRequest{
		Queries: []*prompb.Query{{
			StartTimestampMs: nowUnix,
			EndTimestampMs:   nowUnix + 10000,
			Matchers: []*prompb.LabelMatcher{
				{Type: prompb.LabelMatcher_EQ, Name: "__name__", Value: "promoted_with_instance"},
			},
		}},
	})

	assert.NoError(t, err, "failed to process query")
	assert.Len(t, result.Results, 1)
	assert.Equal(t, withInstance, result.Results[0].Timeseries)
}
