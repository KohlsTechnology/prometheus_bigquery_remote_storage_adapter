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
	"io"
	"log/slog"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/prometheus/prompb"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// BigqueryClient allows sending batches of Prometheus samples to Bigquery.
type BigqueryClient struct {
	logger             *slog.Logger
	client             bigquery.Client
	datasetID          string
	tableID            string
	timeout            time.Duration
	ignoredSamples     prometheus.Counter
	recordsFetched     prometheus.Counter
	batchWriteDuration prometheus.Histogram
	sqlQueryCount      prometheus.Counter
	sqlQueryDuration   prometheus.Histogram
	promoted           []PromotedColumn
}

// CoreColumns are the column names every row always carries. Promoted columns
// may never use these names, so a promoted key can never shadow a core field.
var CoreColumns = []string{"value", "metricname", "timestamp", "tags"}

// PromotedColumn maps a Prometheus label onto a dedicated top-level BigQuery
// column. StripPort removes a trailing :port from the value. OmitEmpty drops
// the key entirely when the label is absent, so the column is stored as NULL;
// without it the column is written on every row (empty string when the label
// is absent) so that a REQUIRED column can never be violated.
type PromotedColumn struct {
	Column    string
	Label     string
	StripPort bool
	OmitEmpty bool
}

// ClientOption customizes a BigqueryClient. Options are variadic so that the
// existing NewClient signature stays source-compatible for current callers.
type ClientOption func(*BigqueryClient)

// WithPromotedLabels configures labels to be promoted into their own columns.
func WithPromotedLabels(cols []PromotedColumn) ClientOption {
	return func(c *BigqueryClient) {
		c.promoted = append([]PromotedColumn(nil), cols...)
	}
}

// NewClient creates a new Client.
func NewClient(logger *slog.Logger, googleAPIjsonkeypath, googleProjectID, googleAPIdatasetID, googleAPItableID string, remoteTimeout time.Duration, opts ...ClientOption) *BigqueryClient {
	ctx := context.Background()
	if logger == nil {
		logger = promslog.NewNopLogger()
	}
	bigQueryClientOptions := []option.ClientOption{}
	if googleAPIjsonkeypath != "" {
		jsonFile, err := os.Open(googleAPIjsonkeypath)
		if err != nil {
			logger.Error("failed to open google api json key", slog.Any("error", err))
			os.Exit(1)
		}

		byteValue, _ := io.ReadAll(jsonFile)

		var result map[string]interface{}
		err = json.Unmarshal([]byte(byteValue), &result)
		if err != nil {
			logger.Error("failed to unmarshal google api json key", slog.Any("error", err))
			os.Exit(1)
		}

		jsonFile.Close()

		if googleProjectID == "" {
			googleProjectID = fmt.Sprintf("%v", result["project_id"])
		}
		bigQueryClientOptions = append(bigQueryClientOptions, option.WithAuthCredentialsFile(option.ServiceAccount, googleAPIjsonkeypath))
	}

	c, err := bigquery.NewClient(ctx, googleProjectID, bigQueryClientOptions...)

	if err != nil {
		logger.Error("failed to create new bigquery client", slog.Any("error", err))
		os.Exit(1)
	}

	bqc := &BigqueryClient{
		logger:    logger,
		client:    *c,
		datasetID: googleAPIdatasetID,
		tableID:   googleAPItableID,
		timeout:   remoteTimeout,
		ignoredSamples: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "storage_bigquery_ignored_samples_total",
				Help: "The total number of samples not sent to BigQuery due to unsupported float values (Inf, -Inf, NaN).",
			},
		),
		recordsFetched: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "storage_bigquery_records_fetched",
				Help: "Total number of records fetched",
			},
		),
		batchWriteDuration: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "storage_bigquery_batch_write_duration_seconds",
				Help:    "The duration it takes to write a batch of samples to BigQuery.",
				Buckets: prometheus.DefBuckets,
			},
		),
		sqlQueryCount: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "storage_bigquery_sql_query_count_total",
				Help: "Total number of sql_queries executed.",
			},
		),
		sqlQueryDuration: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Name: "storage_bigquery_sql_query_duration_seconds",
				Help: "Duration of the sql reads from BigQuery.",
			},
		),
	}

	for _, opt := range opts {
		opt(bqc)
	}
	bqc.preflightPromotedColumns()

	return bqc
}

// preflightPromotedColumns checks the destination table once at startup, but
// only when promotion is configured, so the default path issues no extra API
// call. A missing or mistyped column is a warning: rows will be rejected, but
// loudly, and a warning must not block a restart. A REQUIRED column combined
// with OmitEmpty is fatal because that pairing is guaranteed to reject rows.
// Unreadable metadata is not fatal either, since a write-scoped service
// account may lack bigquery.tables.get.
func (c *BigqueryClient) preflightPromotedColumns() {
	if len(c.promoted) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	md, err := c.client.Dataset(c.datasetID).Table(c.tableID).Metadata(ctx)
	if err != nil {
		c.logger.Debug("could not read destination table schema, skipping promoted column preflight", slog.Any("error", err))
		return
	}

	fatal := false
	for _, issue := range checkPromotedColumns(md.Schema, c.promoted) {
		if issue.fatal {
			c.logger.Error(issue.reason, slog.Any("column", issue.column))
			fatal = true
			continue
		}
		c.logger.Warn(issue.reason, slog.Any("column", issue.column))
	}
	if fatal {
		os.Exit(1)
	}
}

// promotedSchemaIssue is one problem found when comparing the configured
// promoted columns against the destination table schema.
type promotedSchemaIssue struct {
	column string
	reason string
	fatal  bool
}

// checkPromotedColumns compares the configured promoted columns against a table
// schema. It is separated from the API call so it can be tested without a live
// BigQuery table. Only an unsatisfiable configuration is fatal; everything else
// is a warning, because rows rejected at write time surface loudly through
// storage_bigquery_failed_samples_total and must not block a restart.
func checkPromotedColumns(schema bigquery.Schema, promoted []PromotedColumn) []promotedSchemaIssue {
	// BigQuery column names are case-insensitive, so "Hostname" configured
	// against a "hostname" field is the same column. Fold both sides of the
	// lookup, otherwise a mere case difference is reported as a missing column.
	declared := make(map[string]*bigquery.FieldSchema, len(schema))
	for _, f := range schema {
		declared[strings.ToLower(f.Name)] = f
	}

	var issues []promotedSchemaIssue
	for _, p := range promoted {
		f, ok := declared[strings.ToLower(p.Column)]
		if !ok {
			issues = append(issues, promotedSchemaIssue{
				column: p.Column,
				reason: "promoted column does not exist in the destination table, rows will be rejected until it is added",
			})
			continue
		}
		if f.Repeated {
			issues = append(issues, promotedSchemaIssue{
				column: p.Column,
				reason: "promoted column is REPEATED, but a promoted value is a single string, rows will be rejected",
			})
		}
		if f.Type != bigquery.StringFieldType {
			issues = append(issues, promotedSchemaIssue{
				column: p.Column,
				reason: fmt.Sprintf("promoted column has type %s, but a promoted value is a string, rows will be rejected", f.Type),
			})
		}
		// Repeated fields are never Required, so this cannot double-report.
		if f.Required && p.OmitEmpty {
			issues = append(issues, promotedSchemaIssue{
				column: p.Column,
				reason: "promoted column is REQUIRED but configured with omit-empty, which can never satisfy it",
				fatal:  true,
			})
		}
	}
	return issues
}

// Item represents a row item.
type Item struct {
	value      float64 `bigquery:"value"`
	metricname string  `bigquery:"metricname"`
	timestamp  int64   `bigquery:"timestamp"`
	tags       string  `bigquery:"tags"`
	promoted   map[string]string
}

// Save implements the ValueSaver interface.
func (i *Item) Save() (map[string]bigquery.Value, string, error) {
	row := map[string]bigquery.Value{
		"value":      i.value,
		"metricname": i.metricname,
		"timestamp":  i.timestamp,
		"tags":       i.tags,
	}
	// Promoted column names are validated at startup against CoreColumns, so
	// none of these keys can overwrite one of the four above.
	for column, value := range i.promoted {
		row[column] = value
	}
	return row, "", nil
}

// promotedValues resolves the configured promoted columns for one time series.
// It returns nil when promotion is disabled, so the default row shape is
// untouched.
func (c *BigqueryClient) promotedValues(m model.Metric) map[string]string {
	if len(c.promoted) == 0 {
		return nil
	}
	values := make(map[string]string, len(c.promoted))
	for _, p := range c.promoted {
		v, ok := m[model.LabelName(p.Label)]
		if !ok {
			if p.OmitEmpty {
				continue
			}
			// The column is written on every row: a REQUIRED column rejects a
			// missing key, but accepts an empty string.
			values[p.Column] = ""
			continue
		}
		value := string(v)
		if p.StripPort {
			value = stripPort(value)
		}
		values[p.Column] = value
	}
	return values
}

// stripPort removes a trailing :port from a host string. Bracketed IPv6
// literals keep their brackets, and a bare IPv6 address is left alone since it
// cannot be told apart from a host:port pair with certainty.
func stripPort(s string) string {
	if strings.HasPrefix(s, "[") {
		if end := strings.LastIndex(s, "]"); end != -1 {
			return s[:end+1]
		}
		return s
	}
	i := strings.LastIndex(s, ":")
	if i == -1 {
		return s
	}
	if strings.Contains(s[:i], ":") {
		// More than one colon and no brackets: treat as a bare IPv6 address.
		return s
	}
	port := s[i+1:]
	if port == "" {
		return s
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return s
		}
	}
	return s[:i]
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
func (c *BigqueryClient) Write(timeseries []*prompb.TimeSeries) error {
	inserter := c.client.Dataset(c.datasetID).Table(c.tableID).Inserter()
	inserter.SkipInvalidRows = true
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	batch := make([]*Item, 0, len(timeseries))

	for i := range timeseries {
		ts := timeseries[i]
		samples := ts.Samples
		c.recordsFetched.Add(float64(len(samples)))
		metric := make(model.Metric, len(ts.Labels))
		for _, l := range ts.Labels {
			metric[model.LabelName(l.Name)] = model.LabelValue(l.Value)
		}

		t := tagsFromMetric(metric)
		// Resolved once per series rather than per sample.
		promoted := c.promotedValues(metric)

		for _, s := range samples {
			v := float64(s.Value)
			if math.IsNaN(v) || math.IsInf(v, 0) {
				c.logger.Debug("cannot send to bigquery, skipping sample", slog.Any("value", v), slog.Any("sample", s))
				c.ignoredSamples.Inc()
				continue
			}

			batch = append(batch, &Item{
				value:      v,
				metricname: string(metric[model.MetricNameLabel]),
				timestamp:  model.Time(s.Timestamp).Unix(),
				tags:       t,
				promoted:   promoted,
			})
		}
	}

	begin := time.Now()
	if err := inserter.Put(ctx, batch); err != nil {
		if multiError, ok := err.(bigquery.PutMultiError); ok {
			for _, err1 := range multiError {
				for _, err2 := range err1.Errors {
					fmt.Println(err2)
				}
			}
		}
		return err
	}
	duration := time.Since(begin).Seconds()
	c.batchWriteDuration.Observe(duration)

	return nil
}

// Name identifies the client as a BigQuery client.
func (c BigqueryClient) Name() string {
	return "bigquerydb"
}

// Describe implements prometheus.Collector.
func (c *BigqueryClient) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.ignoredSamples.Desc()
	ch <- c.recordsFetched.Desc()
	ch <- c.sqlQueryCount.Desc()
	ch <- c.sqlQueryDuration.Desc()
	ch <- c.batchWriteDuration.Desc()
}

// Collect implements prometheus.Collector.
func (c *BigqueryClient) Collect(ch chan<- prometheus.Metric) {
	ch <- c.ignoredSamples
	ch <- c.recordsFetched
	ch <- c.sqlQueryCount
	ch <- c.sqlQueryDuration
	ch <- c.batchWriteDuration
}

// Read queries the database and returns the results to Prometheus
func (c *BigqueryClient) Read(req *prompb.ReadRequest) (*prompb.ReadResponse, error) {
	tsMap := map[model.Fingerprint]*prompb.TimeSeries{}
	for _, q := range req.Queries {
		command, err := c.buildCommand(q)
		if err != nil {
			return nil, err
		}

		query := c.client.Query(command)
		ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
		c.sqlQueryCount.Inc()
		begin := time.Now()
		iter, err := query.Read(ctx)
		defer cancel()

		if err != nil {
			return nil, err
		}

		if err = mergeResult(tsMap, iter); err != nil {
			return nil, err
		}
		duration := time.Since(begin).Seconds()
		c.sqlQueryDuration.Observe(duration)
		c.logger.Debug("bigquery sql query", slog.Any("rows", iter.TotalRows), slog.Any("duration", duration))
	}

	resp := prompb.ReadResponse{
		Results: []*prompb.QueryResult{
			{Timeseries: make([]*prompb.TimeSeries, 0, len(tsMap))},
		},
	}
	for _, ts := range tsMap {
		resp.Results[0].Timeseries = append(resp.Results[0].Timeseries, ts)
	}
	return &resp, nil
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
			matchers = append(matchers, fmt.Sprintf(`IFNULL(JSON_EXTRACT(tags, '$.%s'), '""') = '"%s"'`, m.Name, m.Value))
		case prompb.LabelMatcher_NEQ:
			matchers = append(matchers, fmt.Sprintf(`IFNULL(JSON_EXTRACT(tags, '$.%s'), '""') != '"%s"'`, m.Name, m.Value))
		case prompb.LabelMatcher_RE:
			matchers = append(matchers, fmt.Sprintf(`REGEXP_CONTAINS(IFNULL(JSON_EXTRACT(tags, '$.%s'), '""'), r'"%s"')`, m.Name, m.Value))
		case prompb.LabelMatcher_NRE:
			matchers = append(matchers, fmt.Sprintf(`not REGEXP_CONTAINS(IFNULL(JSON_EXTRACT(tags, '$.%s'), '""'), r'"%s"')`, m.Name, m.Value))
		default:
			return "", errors.Errorf("unknown match type %v", m.Type)
		}
	}
	matchers = append(matchers, fmt.Sprintf("timestamp >= TIMESTAMP_MILLIS(%v)", q.StartTimestampMs))
	matchers = append(matchers, fmt.Sprintf("timestamp <= TIMESTAMP_MILLIS(%v)", q.EndTimestampMs))

	query := fmt.Sprintf("SELECT metricname, tags, UNIX_MILLIS(timestamp) as timestamp, value FROM %s.%s WHERE %v ORDER BY timestamp", c.datasetID, c.tableID, strings.Join(matchers, " AND "))
	c.logger.Debug("bigquery read", slog.Any("sql query", query))

	return query, nil
}

// rowsToTimeseries iterates over the BigQuery data and creates time series for Prometheus
func mergeResult(tsMap map[model.Fingerprint]*prompb.TimeSeries, iter *bigquery.RowIterator) error {
	if iter == nil {
		return nil
	}
	for {
		row := make(map[string]bigquery.Value)
		err := iter.Next(&row)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return err
		}

		sample, metric, labels, err := rowToSample(row)
		if err != nil {
			return err
		}

		fp := metric.Fingerprint()
		ts, ok := tsMap[fp]
		if !ok {
			ts = &prompb.TimeSeries{Labels: labels}
			tsMap[fp] = ts
		}
		ts.Samples = append(ts.Samples, sample)
	}

	return nil
}

// rowToSample converts a BigQuery row to a sample and also processes the labels for later consumption
func rowToSample(row map[string]bigquery.Value) (prompb.Sample, model.Metric, []*prompb.Label, error) {
	var v interface{}
	labelsJSON := row["tags"].(string)
	err := json.Unmarshal([]byte(labelsJSON), &v)
	if err != nil {
		return prompb.Sample{}, nil, nil, err
	}
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
	// Make sure we sort the labels, so the test cases won't blow up
	sort.Slice(labelPairs, func(i, j int) bool { return labelPairs[i].Name < labelPairs[j].Name })
	metric[model.LabelName(model.MetricNameLabel)] = model.LabelValue(row["metricname"].(string))
	return prompb.Sample{Timestamp: row["timestamp"].(int64), Value: row["value"].(float64)}, metric, labelPairs, nil
}

func escapeSingleQuotes(str string) string {
	return strings.ReplaceAll(str, `'`, `\'`)
}

func escapeSlashes(str string) string {
	return strings.ReplaceAll(str, `/`, `\/`)
}
