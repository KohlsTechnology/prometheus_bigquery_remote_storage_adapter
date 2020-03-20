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
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"
	"google.golang.org/api/option"
)

// BigqueryClient allows sending batches of Prometheus samples to Bigquery.
type BigqueryClient struct {
	logger         log.Logger
	client         bigquery.Client
	datasetID      string
	tableID        string
	timeout        time.Duration
	ignoredSamples prometheus.Counter
}

// NewClient creates a new Client.
func NewClient(logger log.Logger, googleAPIjsonkeypath, googleAPIdatasetID, googleAPItableID string, remoteTimeout time.Duration) *BigqueryClient {
	ctx := context.Background()

	jsonFile, err := os.Open(googleAPIjsonkeypath)
	if err != nil {
		level.Error(logger).Log("err", err)
		os.Exit(1)
	}

	byteValue, _ := ioutil.ReadAll(jsonFile)

	var result map[string]interface{}
	json.Unmarshal([]byte(byteValue), &result)

	//fmt.Println(result["type"])
	jsonFile.Close()

	projectID := fmt.Sprintf("%v", result["project_id"])

	c, err := bigquery.NewClient(ctx, projectID, option.WithCredentialsFile(googleAPIjsonkeypath))
	if err != nil {
		level.Error(logger).Log("err", err)
		os.Exit(1)
	}

	if logger == nil {
		logger = log.NewNopLogger()
	}

	return &BigqueryClient{
		logger:    logger,
		client:    *c,
		datasetID: googleAPIdatasetID,
		tableID:   googleAPItableID,
		timeout:   remoteTimeout,
		ignoredSamples: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "prometheus_bigquery_ignored_samples_total",
				Help: "The total number of samples not sent to BigQuery due to unsupported float values (Inf, -Inf, NaN).",
			},
		),
	}
}

// Item represents a row item.
type Item struct {
	value      float64
	metricname string
	timestamp  time.Time
	tags       string
}

// Save implements the ValueSaver interface.
func (i *Item) Save() (map[string]bigquery.Value, string, error) {
	return map[string]bigquery.Value{
		"value":      i.value,
		"metricname": i.metricname,
		"timestamp":  i.timestamp,
		"tags":       i.tags,
	}, "", nil
}

// tagsFromMetric extracts tags from a Prometheus MetricNameLabel.
func tagsFromMetric(m model.Metric) string {
	tags := make(map[string]interface{}, len(m)-1)
	for l, v := range m {
		if l != model.MetricNameLabel {
			tags[string(l)] = string(v)
		}
	}
	tagsmarshaled, _ := json.Marshal(tags)
	return string(tagsmarshaled)
}

// Write sends a batch of samples to BigQuery via the client.
func (c *BigqueryClient) Write(samples model.Samples) error {
	inserter := c.client.Dataset(c.datasetID).Table(c.tableID).Inserter()
	inserter.SkipInvalidRows = true
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)

	batch := make([]*Item, 0, len(samples))

	for _, s := range samples {
		v := float64(s.Value)
		if math.IsNaN(v) || math.IsInf(v, 0) {
			level.Debug(c.logger).Log("msg", "cannot send to BigQuery, skipping sample", "value", v, "sample", s)
			c.ignoredSamples.Inc()
			continue
		}

		batch = append(batch, &Item{
			value:      v,
			metricname: string(s.Metric[model.MetricNameLabel]),
			timestamp:  s.Timestamp.Time(),
			tags:       tagsFromMetric(s.Metric),
		})

	}

	if err := inserter.Put(ctx, batch); err != nil {
		if multiError, ok := err.(bigquery.PutMultiError); ok {
			for _, err1 := range multiError {
				for _, err2 := range err1.Errors {
					fmt.Println(err2)
				}
			}
		}
		defer cancel()
		return err
	}
	defer cancel()
	return nil
}

func concatLabels(labels map[string]string) string {
	// 0xff cannot occur in valid UTF-8 sequences, so use it
	// as a separator here.
	separator := "\xff"
	pairs := make([]string, 0, len(labels))
	for k, v := range labels {
		pairs = append(pairs, k+separator+v)
	}
	return strings.Join(pairs, separator)
}

// Name identifies the client as a BigQuery client.
func (c BigqueryClient) Name() string {
	return "bigquerydb"
}

// Describe implements prometheus.Collector.
func (c *BigqueryClient) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.ignoredSamples.Desc()
}

// Collect implements prometheus.Collector.
func (c *BigqueryClient) Collect(ch chan<- prometheus.Metric) {
	ch <- c.ignoredSamples
}
