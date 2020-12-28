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
	"flag"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/prometheus/prometheus/prompb"
	"github.com/stretchr/testify/assert"
)

// example call: go test -v -args -googleAPIjsonkeypath=../../project-credential.json -googleAPIdatasetID=prometheus_test -googleAPItableID=test_stream ./...

var logger = log.NewLogfmtLogger(log.NewSyncWriter(os.Stdout))

var googleAPIjsonkeypath string
var googleAPIdatasetID string
var googleAPItableID string
var googleProjectID string

func init() {
	flag.StringVar(&googleAPIjsonkeypath, "googleAPIjsonkeypath", "foo", "Path to json keyfile for GCP service account. JSON keyfile also contains project_id")
	flag.StringVar(&googleAPIdatasetID, "googleAPIdatasetID", "bar", "Dataset name as shown in GCP.")
	flag.StringVar(&googleAPItableID, "googleAPItableID", "baz", "Table name as shown in GCP.")
}

func TestNaN(t *testing.T) {

	nowUnix := time.Now().Unix() * 1000

	timeseriesGood := []*prompb.TimeSeries{
		{
			Labels: []*prompb.Label{
				{
					Name:  "__name__",
					Value: "test_metric",
				},
				{
					Name:  "label",
					Value: "goodvalue",
				},
				{
					Name:  "test",
					Value: "TestNaN",
				},
			},
			Samples: []prompb.Sample{
				{
					Timestamp: nowUnix,
					Value:     1,
				},
			},
		},
	}
	timeseriesNaN := []*prompb.TimeSeries{
		{
			Labels: []*prompb.Label{
				{
					Name:  "__name__",
					Value: "test_metric",
				},
				{
					Name:  "label",
					Value: "NaN",
				},
				{
					Name:  "test",
					Value: "TestNaN",
				},
			},
			Samples: []prompb.Sample{
				{
					Timestamp: nowUnix,
					Value:     math.NaN(),
				},
			},
		},
	}

	thirtysecondtimeout, _ := time.ParseDuration("30s")

	bqclient := NewClient(logger, googleAPIjsonkeypath, googleProjectID, googleAPIdatasetID, googleAPItableID, thirtysecondtimeout)

	if err := bqclient.Write(timeseriesGood); err != nil {
		fmt.Println("Error sending samples: ", err)
	}
	if err := bqclient.Write(timeseriesNaN); err != nil {
		fmt.Println("Error sending samples: ", err)
	}

	request := prompb.ReadRequest{
		Queries: []*prompb.Query{
			{
				StartTimestampMs: nowUnix,
				EndTimestampMs:   nowUnix + 10000,
				Matchers: []*prompb.LabelMatcher{
					{
						Type:  prompb.LabelMatcher_EQ,
						Name:  "test",
						Value: "TestNaN",
					},
				},
			},
		},
	}

	result, err := bqclient.Read(&request)

	assert.Nil(t, err, "failed to process query")
	assert.Len(t, result.Results, 1)
	assert.Len(t, result.Results[0].Timeseries, 1)
	assert.Len(t, result.Results[0].Timeseries[0].Samples, 1)
	assert.Equal(t, timeseriesGood, result.Results[0].Timeseries)

}

func TestWriteRead(t *testing.T) {
	nowUnix := time.Now().Unix() * 1000

	timeseries := []*prompb.TimeSeries{
		{
			Labels: []*prompb.Label{
				{
					Name:  "__name__",
					Value: "test_metric",
				},
				{
					Name:  "label_1",
					Value: "value_1",
				},
				{
					Name:  "label_2",
					Value: "value_2",
				},
				{
					Name:  "test",
					Value: "TestWriteRead",
				},
			},
			Samples: []prompb.Sample{
				{
					Timestamp: nowUnix,
					Value:     1,
				},
				{
					Timestamp: nowUnix + 2000,
					Value:     2,
				},
				{
					Timestamp: nowUnix + 3000,
					Value:     3,
				},
			},
		},
	}

	thirtysecondtimeout, _ := time.ParseDuration("30s")

	bqclient := NewClient(logger, googleAPIjsonkeypath, googleProjectID, googleAPIdatasetID, googleAPItableID, thirtysecondtimeout)

	if err := bqclient.Write(timeseries); err != nil {
		fmt.Println("Error sending samples: ", err)
	}

	request := prompb.ReadRequest{
		Queries: []*prompb.Query{
			{
				StartTimestampMs: nowUnix,
				EndTimestampMs:   nowUnix + 10000,
				Matchers: []*prompb.LabelMatcher{
					{
						Type:  prompb.LabelMatcher_EQ,
						Name:  "test",
						Value: "TestWriteRead",
					},
				},
			},
		},
	}

	result, err := bqclient.Read(&request)

	assert.Nil(t, err, "failed to process query")
	assert.Len(t, result.Results, 1)
	assert.Len(t, result.Results[0].Timeseries, 1)
	assert.Len(t, result.Results[0].Timeseries[0].Samples, 3)
	assert.Equal(t, timeseries, result.Results[0].Timeseries)

}
