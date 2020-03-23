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
	"testing"
	"time"

	"github.com/prometheus/common/model"
)

// example call: go test -v -args -googleAPIjsonkeypath=../../project-credential.json -googleAPIdatasetID=prometheus_test -googleAPItableID=test_stream

var googleAPIjsonkeypath string
var googleAPIdatasetID string
var googleAPItableID string

func init() {
	flag.StringVar(&googleAPIjsonkeypath, "googleAPIjsonkeypath", "foo", "Path to json keyfile for GCP service account. JSON keyfile also contains project_id")
	flag.StringVar(&googleAPIdatasetID, "googleAPIdatasetID", "bar", "Dataset name as shown in GCP.")
	flag.StringVar(&googleAPItableID, "googleAPItableID", "baz", "Table name as shown in GCP.")
}

func TestClient(t *testing.T) {

	samples := model.Samples{
		{
			Metric: model.Metric{
				model.MetricNameLabel: "testmetric",
				"test_label":          "test_label_value1",
			},
			//Timestamp: model.Time(123456789123),
			Timestamp: model.Time(time.Now().Unix()),
			Value:     1.23,
		},
		{
			Metric: model.Metric{
				model.MetricNameLabel: "testmetric",
				"test_label":          "test_label_value2",
			},
			Timestamp: model.Time(123456789123),
			Value:     5.1234,
		},
		{
			Metric: model.Metric{
				model.MetricNameLabel: "nan_value",
			},
			Timestamp: model.Time(123456789123),
			Value:     model.SampleValue(math.NaN()),
		},
		// {
		// 	Metric: model.Metric{
		// 		model.MetricNameLabel: "pos_inf_value",
		// 	},
		// 	Timestamp: model.Time(123456789123),
		// 	Value:     model.SampleValue(math.Inf(1)),
		// },
		// {
		// 	Metric: model.Metric{
		// 		model.MetricNameLabel: "neg_inf_value",
		// 	},
		// 	Timestamp: model.Time(123456789123),
		// 	Value:     model.SampleValue(math.Inf(-1)),
		// },
		// {
		// 	Metric: model.Metric{
		// 		model.MetricNameLabel: "partial",
		// 	},
		// },
	}

	//expectedBody := `testmetric,test_label=test_label_value1 value=1.23 123456789123
	//testmetric,test_label=test_label_value2 value=5.1234 123456789123
	//`

	thirtysecondtimeout, _ := time.ParseDuration("30s")

	c := NewClient(nil, googleAPIjsonkeypath, googleAPIdatasetID, googleAPItableID, thirtysecondtimeout)

	if err := c.Write(samples); err != nil {
		fmt.Println("Error sending samples: ", err)
	}
}
