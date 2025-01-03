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
	"log/slog"
	"math"
	"os"
	"testing"
	"time"

	"github.com/prometheus/prometheus/prompb"
	"github.com/stretchr/testify/assert"
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
