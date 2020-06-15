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
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/prompb"
	"google.golang.org/api/iterator"
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
	value      float64   `bigquery:"value"`
	metricname string    `bigquery:"metricname"`
	timestamp  time.Time `bigquery:"timestamp"`
	tags       string    `bigquery:"tags"`
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

// Read queries the database and returns the results to Prometheus
func (c *BigqueryClient) Read(req *prompb.ReadRequest) (*prompb.ReadResponse, error) {
	readResp := &prompb.ReadResponse{Results: []*prompb.QueryResult{}}
	for _, q := range req.Queries {
		command, err := c.buildCommand(q)
		if err != nil {
			return nil, err
		}

		query := c.client.Query(command)
		ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
		iter, err := query.Read(ctx)
		level.Debug(c.logger).Log("msg", "BiQuery query", "rows received", iter.TotalRows)
		defer cancel()

		if err != nil {
			return nil, err
		}

		tsList, err := rowsToTimeseries(iter)

		// for _, ts := range tsList {
		// 	c.metrics.Samples["read"].Observe(float64(len(ts.Samples)))
		// 	// Prometheus wants its samples to be sorted with it receives them.
		// 	sort.Slice(ts.Samples, func(i, j int) bool { return ts.Samples[i].Timestamp < ts.Samples[j].Timestamp })
		// }

		readResp.Results = append(readResp.Results, &prompb.QueryResult{Timeseries: tsList})
	}

	return readResp, nil
}

// BuildCommand generates the proper SQL for the query
func (c *BigqueryClient) buildCommand(q *prompb.Query) (string, error) {
	matchers := make([]string, 0, len(q.Matchers))
	for _, m := range q.Matchers {
		// Metric Names
		if m.Name == model.MetricNameLabel {
			switch m.Type {
			case prompb.LabelMatcher_EQ:
				matchers = append(matchers, fmt.Sprintf("metricname = '%s'", escapeSingleQuotes(m.Value)))
			case prompb.LabelMatcher_NEQ:
				matchers = append(matchers, fmt.Sprintf("metricname != '%s'", escapeSingleQuotes(m.Value)))
			case prompb.LabelMatcher_RE:
				matchers = append(matchers, fmt.Sprintf("REGEXP_CONTAINS(metricname, r'%s')", escapeSlashes(m.Value)))
			case prompb.LabelMatcher_NRE:
				matchers = append(matchers, fmt.Sprintf("not REGEXP_CONTAINS(metricname, r'%s')", escapeSlashes(m.Value)))
			default:
				return "", errors.Errorf("unknown match type %v", m.Type)
			}
			continue
		}

		// Labels
		switch m.Type {
		case prompb.LabelMatcher_EQ:
			matchers = append(matchers, fmt.Sprintf("JSON_EXTRACT(tags, '$.%q') = '%s')", m.Name, escapeSingleQuotes(m.Value)))
		case prompb.LabelMatcher_NEQ:
			matchers = append(matchers, fmt.Sprintf("JSON_EXTRACT(tags, '$.%q') = '%s')", m.Name, escapeSingleQuotes(m.Value)))
		case prompb.LabelMatcher_RE:
			matchers = append(matchers, fmt.Sprintf("REGEXP_CONTAINS(JSON_EXTRACT(tags, '$.%q'), r'%s')", m.Name, escapeSlashes(m.Value)))
		case prompb.LabelMatcher_NRE:
			matchers = append(matchers, fmt.Sprintf("not REGEXP_CONTAINS(JSON_EXTRACT(tags, '$.%q'), r'%s')", m.Name, escapeSlashes(m.Value)))
		default:
			return "", errors.Errorf("unknown match type %v", m.Type)
		}
	}
	matchers = append(matchers, fmt.Sprintf("timestamp >= TIMESTAMP_MILLIS(%v)", q.StartTimestampMs))
	matchers = append(matchers, fmt.Sprintf("timestamp <= TIMESTAMP_MILLIS(%v)", q.EndTimestampMs))

	query := fmt.Sprintf("SELECT metricname, tags, timestamp, value FROM %s.%s WHERE %v", c.datasetID, c.tableID, strings.Join(matchers, " AND "))
	level.Debug(c.logger).Log("msg", "BiQuery read", "sql query", query)

	return query, nil
}

// rowsToTimeseries iterates over the BigQuery data and creates time series for Prometheus
func rowsToTimeseries(iter *bigquery.RowIterator) ([]*prompb.TimeSeries, error) {
	if iter == nil {
		return nil, nil
	}
	tsMap := make(map[model.Fingerprint]*prompb.TimeSeries)
	for {
		row := make(map[string]bigquery.Value)
		err := iter.Next(&row)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		sample, metric, labels, err := rowToSample(row)
		if err != nil {
			return nil, err
		}
		fp := metric.Fingerprint()
		if _, ok := tsMap[fp]; !ok {
			tsMap[fp] = &prompb.TimeSeries{Labels: labels}
		}
		tsMap[fp].Samples = append(tsMap[fp].Samples, *sample)
	}

	return orderTimeSeries(tsMap), nil
}

// rowToSample converts a BigQuery row to a sample and also processes the labels for later consumption
func rowToSample(row map[string]bigquery.Value) (*prompb.Sample, model.Metric, []*prompb.Label, error) {
	var v interface{}
	labelsJSON := row["tags"].(string)
	json.Unmarshal([]byte(labelsJSON), &v)
	labels := v.(map[string]interface{})
	labelPairs := make([]*prompb.Label, 0, len(labels))
	metric := model.Metric{}
	for name, value := range labels {
		labelPairs = append(labelPairs, &prompb.Label{
			Name:  name,
			Value: value.(string),
		})
		metric[model.LabelName(name)] = model.LabelValue(value.(string))
	}
	labelPairs = append(labelPairs, &prompb.Label{
		Name:  model.MetricNameLabel,
		Value: row["metricname"].(string),
	})
	metric[model.LabelName(model.MetricNameLabel)] = model.LabelValue(row["metricname"].(string))
	return &prompb.Sample{Timestamp: row["timestamp"].(time.Time).Unix(), Value: row["value"].(float64)}, metric, labelPairs, nil
}

// orderTimeSeries sole purpose is to make Prometheus happy
func orderTimeSeries(tsMap map[model.Fingerprint]*prompb.TimeSeries) []*prompb.TimeSeries {
	fps := make([]model.Fingerprint, 0, len(tsMap))
	for fp := range tsMap {
		fps = append(fps, fp)
	}
	sort.Slice(fps, func(i, j int) bool { return fps[i] < fps[j] })
	// Convert timeseries map to a list.
	tsList := make([]*prompb.TimeSeries, 0, len(tsMap))
	for _, fp := range fps {
		tsList = append(tsList, tsMap[fp])
	}
	return tsList
}

func escapeSingleQuotes(str string) string {
	return strings.Replace(str, `'`, `\'`, -1)
}

func escapeSlashes(str string) string {
	return strings.Replace(str, `/`, `\/`, -1)
}
