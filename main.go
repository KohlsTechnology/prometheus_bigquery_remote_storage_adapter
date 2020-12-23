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

// The main package for the executable
package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter/bigquerydb"
	"github.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter/pkg/version"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/gogo/protobuf/proto"
	"github.com/golang/snappy"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/alecthomas/kingpin.v2"

	"github.com/prometheus/common/promlog"

	"github.com/prometheus/prometheus/prompb"
)

type config struct {
	googleProjectID      string
	googleAPIjsonkeypath string
	googleAPIdatasetID   string
	googleAPItableID     string
	remoteTimeout        time.Duration
	listenAddr           string
	telemetryPath        string
	promlogConfig        promlog.Config
	printVersion         bool
}

var (
	receivedSamples = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "storage_bigquery_received_samples_total",
			Help: "Total number of received samples.",
		},
	)
	sentSamples = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "storage_bigquery_sent_samples_total",
			Help: "Total number of processed samples sent to remote storage.",
		},
		[]string{"remote"},
	)
	failedSamples = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "storage_bigquery_failed_samples_total",
			Help: "Total number of processed samples which failed on send to remote storage.",
		},
		[]string{"remote"},
	)
	sentBatchDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "storage_bigquery_sent_batch_duration_seconds",
			Help:    "Duration of sample batch send calls to the remote storage.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"remote"},
	)
	writeErrors = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "storage_bigquery_write_errors_total",
			Help: "Total number of write errors to BigQuery.",
		},
	)
	readErrors = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "storage_bigquery_read_errors_total",
			Help: "Total number of read errors from BigQuery.",
		},
	)
	writeProcessingDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "storage_bigquery_write_api_seconds",
			Help:    "Duration of the write api processing.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"remote"},
	)
	readProcessingDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "storage_bigquery_read_api_seconds",
			Help:    "Duration of the read api processing.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"remote"},
	)
)

func init() {
	prometheus.MustRegister(receivedSamples)
	prometheus.MustRegister(sentSamples)
	prometheus.MustRegister(failedSamples)
	prometheus.MustRegister(sentBatchDuration)
	prometheus.MustRegister(writeErrors)
	prometheus.MustRegister(readErrors)
	prometheus.MustRegister(writeProcessingDuration)
	prometheus.MustRegister(readProcessingDuration)
}

func main() {
	cfg := parseFlags()

	http.Handle(cfg.telemetryPath, promhttp.Handler())

	logger := promlog.New(&cfg.promlogConfig)

	level.Info(logger).Log("msg", version.Get())

	level.Info(logger).Log("msg", "Configuration settings:",
		"googleAPIjsonkeypath", cfg.googleAPIjsonkeypath,
		"googleProjectID", cfg.googleProjectID,
		"googleAPIdatasetID", cfg.googleAPIdatasetID,
		"googleAPItableID", cfg.googleAPItableID,
		"telemetryPath", cfg.telemetryPath,
		"listenAddr", cfg.listenAddr,
		"remoteTimeout", cfg.remoteTimeout)

	writers, readers := buildClients(logger, cfg)
	if err := serve(logger, cfg.listenAddr, writers, readers); err != nil {
		level.Error(logger).Log("msg", "Failed to listen", "addr", cfg.listenAddr, "err", err)
		os.Exit(1)
	}
}

func parseFlags() *config {
	a := kingpin.New(filepath.Base(os.Args[0]), "Remote storage adapter")
	a.HelpFlag.Short('h')

	cfg := &config{
		promlogConfig: promlog.Config{},
	}

	a.Flag("version", "Print version and build information, then exit").
		Default("false").BoolVar(&cfg.printVersion)
	a.Flag("googleAPIjsonkeypath", "Path to json keyfile for GCP service account. JSON keyfile also contains project_id").
		Envar("PROMBQ_GCP_JSON").ExistingFileVar(&cfg.googleAPIjsonkeypath)
	a.Flag("googleAPIdatasetID", "Dataset name as shown in GCP.").
		Envar("PROMBQ_DATASET").Required().StringVar(&cfg.googleAPIdatasetID)
	a.Flag("googleAPItableID", "Table name as showon in GCP.").
		Envar("PROMBQ_TABLE").Required().StringVar(&cfg.googleAPItableID)
	a.Flag("send-timeout", "The timeout to use when sending samples to the remote storage.").
		Envar("PROMBQ_TIMEOUT").Default("30s").DurationVar(&cfg.remoteTimeout)
	a.Flag("web.listen-address", "Address to listen on for web endpoints.").
		Envar("PROMBQ_LISTEN").Default(":9201").StringVar(&cfg.listenAddr)
	a.Flag("web.telemetry-path", "Address to listen on for web endpoints.").
		Envar("PROMBQ_TELEMETRY").Default("/metrics").StringVar(&cfg.telemetryPath)
	cfg.promlogConfig.Level = &promlog.AllowedLevel{}
	a.Flag("log.level", "Only log messages with the given severity or above. One of: [debug, info, warn, error]").
		Envar("PROMBQ_LOG_LEVEL").Default("info").SetValue(cfg.promlogConfig.Level)
	cfg.promlogConfig.Format = &promlog.AllowedFormat{}
	a.Flag("log.format", "Output format of log messages. One of: [logfmt, json]").
		Envar("PROMBQ_LOG_FORMAT").Default("logfmt").SetValue(cfg.promlogConfig.Format)
	googleProjectIDFlagCause := a.Flag("googleProjectID", "The GCP Project ID is mandatory when googleAPIjsonkeypath is not provided").Envar("PROMBQ_GCP_PROJECT_ID")
	_, err := a.Parse(os.Args[1:])
	if cfg.googleAPIjsonkeypath == "" {
		googleProjectIDFlagCause.Required().StringVar(&cfg.googleProjectID)
		_, err = a.Parse(os.Args[1:])
	}

	if cfg.printVersion {
		version.Print()
		os.Exit(0)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, errors.Wrapf(err, "Error parsing commandline arguments"))
		a.Usage(os.Args[1:])
		os.Exit(2)
	}

	return cfg
}

type writer interface {
	Write(timeseries []*prompb.TimeSeries) error
	Name() string
}

type reader interface {
	Read(req *prompb.ReadRequest) (*prompb.ReadResponse, error)
	Name() string
}

func buildClients(logger log.Logger, cfg *config) ([]writer, []reader) {
	var writers []writer
	var readers []reader

	c := bigquerydb.NewClient(
		log.With(logger, "storage", "BigQuery"),
		cfg.googleAPIjsonkeypath,
		cfg.googleProjectID,
		cfg.googleAPIdatasetID,
		cfg.googleAPItableID,
		cfg.remoteTimeout)
	prometheus.MustRegister(c)
	writers = append(writers, c)
	readers = append(readers, c)
	level.Info(logger).Log("msg", "Starting up...")
	return writers, readers
}

func serve(logger log.Logger, addr string, writers []writer, readers []reader) error {
	http.HandleFunc("/write", func(w http.ResponseWriter, r *http.Request) {
		begin := time.Now()
		compressed, err := ioutil.ReadAll(r.Body)
		if err != nil {
			level.Error(logger).Log("msg", "Read error", "err", err.Error())
			http.Error(w, err.Error(), http.StatusInternalServerError)
			writeErrors.Inc()
			return
		}

		reqBuf, err := snappy.Decode(nil, compressed)
		if err != nil {
			level.Error(logger).Log("msg", "Decode error", "err", err.Error())
			http.Error(w, err.Error(), http.StatusBadRequest)
			writeErrors.Inc()
			return
		}

		var req prompb.WriteRequest
		if err := proto.Unmarshal(reqBuf, &req); err != nil {
			level.Error(logger).Log("msg", "Unmarshal error", "err", err.Error())
			http.Error(w, err.Error(), http.StatusBadRequest)
			writeErrors.Inc()
			return
		}

		var wg sync.WaitGroup
		for _, w := range writers {
			wg.Add(1)
			go func(rw writer) {
				sendSamples(logger, rw, req.Timeseries)
				wg.Done()
			}(w)
		}
		wg.Wait()
		duration := time.Since(begin).Seconds()
		writeProcessingDuration.WithLabelValues(writers[0].Name()).Observe(duration)

	})

	http.HandleFunc("/read", func(w http.ResponseWriter, r *http.Request) {
		begin := time.Now()
		compressed, err := ioutil.ReadAll(r.Body)
		if err != nil {
			level.Error(logger).Log("msg", "Read error", "err", err.Error())
			http.Error(w, err.Error(), http.StatusInternalServerError)
			readErrors.Inc()
			return
		}

		reqBuf, err := snappy.Decode(nil, compressed)
		if err != nil {
			level.Error(logger).Log("msg", "Decode error", "err", err.Error())
			http.Error(w, err.Error(), http.StatusBadRequest)
			readErrors.Inc()
			return
		}

		var req prompb.ReadRequest
		if err := proto.Unmarshal(reqBuf, &req); err != nil {
			level.Error(logger).Log("msg", "Unmarshal error", "err", err.Error())
			http.Error(w, err.Error(), http.StatusBadRequest)
			readErrors.Inc()
			return
		}

		// TODO: Support reading from more than one reader and merging the results.
		if len(readers) != 1 {
			http.Error(w, fmt.Sprintf("expected exactly one reader, found %d readers", len(readers)), http.StatusInternalServerError)
			readErrors.Inc()
			return
		}
		reader := readers[0]

		var resp *prompb.ReadResponse
		resp, err = reader.Read(&req)
		if err != nil {
			level.Warn(logger).Log("msg", "Error executing query", "query", req, "storage", reader.Name(), "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			readErrors.Inc()
			return
		}

		data, err := proto.Marshal(resp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			readErrors.Inc()
			return
		}

		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Header().Set("Content-Encoding", "snappy")

		compressed = snappy.Encode(nil, data)
		if _, err := w.Write(compressed); err != nil {
			level.Warn(logger).Log("msg", "Error writing response", "storage", reader.Name(), "err", err)
			readErrors.Inc()
		}
		duration := time.Since(begin).Seconds()
		readProcessingDuration.WithLabelValues(writers[0].Name()).Observe(duration)
		level.Debug(logger).Log("msg", "/read", "duration", duration)
	})

	return http.ListenAndServe(addr, nil)
}

func sendSamples(logger log.Logger, w writer, timeseries []*prompb.TimeSeries) {
	begin := time.Now()
	err := w.Write(timeseries)
	duration := time.Since(begin).Seconds()
	if err != nil {
		level.Warn(logger).Log("msg", "Error sending samples to remote storage", "err", err, "storage", w.Name(), "num_samples", len(timeseries))
		failedSamples.WithLabelValues(w.Name()).Add(float64(len(timeseries)))
		writeErrors.Inc()
	} else {
		sentSamples.WithLabelValues(w.Name()).Add(float64(len(timeseries)))
		sentBatchDuration.WithLabelValues(w.Name()).Observe(duration)
	}
}
