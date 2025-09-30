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
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter/bigquerydb"
	"github.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter/pkg/version"
	"github.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter/tracing"
	"github.com/gogo/protobuf/proto"
	"github.com/golang/snappy"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/prometheus/prompb"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"gopkg.in/alecthomas/kingpin.v2"
)

type config struct {
	googleProjectID      string
	googleAPIjsonkeypath string
	googleAPIdatasetID   string
	googleAPItableID     string
	remoteTimeout        time.Duration
	listenAddr           string
	telemetryPath        string
	promslogConfig       promslog.Config
	printVersion         bool
	enableTracing        bool
	tracingExporter      string
	tracingEndpoint      string
	tracingServiceName   string
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

	logger := promslog.New(&cfg.promslogConfig)

	if cfg.enableTracing {
		if err := tracing.InitTracing(cfg.tracingServiceName, cfg.tracingExporter, cfg.tracingEndpoint, logger); err != nil {
			logger.Error("failed to initialize tracing", slog.Any("error", err))
			os.Exit(1)
		}
		defer func() {
			if err := tracing.ShutdownTracing(context.Background()); err != nil {
				logger.Error("failed to shutdown tracing", slog.Any("error", err))
			}
		}()
	}

	http.Handle(cfg.telemetryPath, promhttp.Handler())

	logger.Info(version.Get())

	logger.Info("configuration settings",
		slog.Any("googleAPIjsonkeypath", cfg.googleAPIjsonkeypath),
		slog.Any("googleProjectID", cfg.googleProjectID),
		slog.Any("googleAPIdatasetID", cfg.googleAPIdatasetID),
		slog.Any("googleAPItableID", cfg.googleAPItableID),
		slog.Any("telemetryPath", cfg.telemetryPath),
		slog.Any("listenAddr", cfg.listenAddr),
		slog.Any("remoteTimeout", cfg.remoteTimeout),
		slog.Bool("tracing_enabled", cfg.enableTracing),
		slog.String("tracing_exporter", cfg.tracingExporter),
		slog.String("tracing_endpoint", cfg.tracingEndpoint),
		slog.String("tracing_service_name", cfg.tracingServiceName))

	writers, readers := buildClients(*logger, cfg)
	serve(*logger, cfg.listenAddr, writers, readers)
}

func parseFlags() *config {
	a := kingpin.New(filepath.Base(os.Args[0]), "Remote storage adapter")
	a.HelpFlag.Short('h')

	cfg := &config{
		promslogConfig: promslog.Config{},
	}

	a.Flag("version", "Print version and build information, then exit").
		Default("false").BoolVar(&cfg.printVersion)
	a.Flag("googleAPIjsonkeypath", "Path to json keyfile for GCP service account. JSON keyfile also contains project_id").
		Envar("PROMBQ_GCP_JSON").ExistingFileVar(&cfg.googleAPIjsonkeypath)
	googleProjectIDFlagCause := a.Flag("googleProjectID", "The GCP Project ID is mandatory when googleAPIjsonkeypath is not provided").
		Envar("PROMBQ_GCP_PROJECT_ID")
	googleProjectIDFlagCause.StringVar(&cfg.googleProjectID)
	a.Flag("googleAPIdatasetID", "Dataset name as shown in GCP.").
		Envar("PROMBQ_DATASET").Required().StringVar(&cfg.googleAPIdatasetID)
	a.Flag("googleAPItableID", "Table name as shown in GCP.").
		Envar("PROMBQ_TABLE").Required().StringVar(&cfg.googleAPItableID)
	a.Flag("send-timeout", "The timeout to use when sending samples to the remote storage.").
		Envar("PROMBQ_TIMEOUT").Default("30s").DurationVar(&cfg.remoteTimeout)
	a.Flag("web.listen-address", "Address to listen on for web endpoints.").
		Envar("PROMBQ_LISTEN").Default(":9201").StringVar(&cfg.listenAddr)
	a.Flag("web.telemetry-path", "Address to listen on for web endpoints.").
		Envar("PROMBQ_TELEMETRY").Default("/metrics").StringVar(&cfg.telemetryPath)
	cfg.promslogConfig.Level = &promslog.Level{}
	a.Flag("log.level", "Only log messages with the given severity or above. One of: [debug, info, warn, error]").
		Envar("PROMBQ_LOG_LEVEL").Default("info").SetValue(cfg.promslogConfig.Level)
	cfg.promslogConfig.Format = &promslog.Format{}
	a.Flag("log.format", "Output format of log messages. One of: [logfmt, json]").
		Envar("PROMBQ_LOG_FORMAT").Default("logfmt").SetValue(cfg.promslogConfig.Format)
	a.Flag("tracing.enable", "Enable OpenTelemetry tracing").
		Envar("PROMBQ_TRACING_ENABLE").Default("false").BoolVar(&cfg.enableTracing)
	a.Flag("tracing.exporter", "Tracing exporter (otlp, otlp-grpc, otlp-http, jaeger, zipkin, stdout)").
		Envar("PROMBQ_TRACING_EXPORTER").Default("otlp-grpc").StringVar(&cfg.tracingExporter)
	a.Flag("tracing.endpoint", "Tracing endpoint URL").
		Envar("PROMBQ_TRACING_ENDPOINT").StringVar(&cfg.tracingEndpoint)
	a.Flag("tracing.service-name", "Service name for tracing").
		Envar("PROMBQ_TRACING_SERVICE_NAME").Default("prometheus-bigquery-adapter").StringVar(&cfg.tracingServiceName)

	_, err := a.Parse(os.Args[1:])

	if cfg.printVersion {
		version.Print()
		os.Exit(0)
	}

	handle(err, a)
	if cfg.googleAPIjsonkeypath == "" {
		googleProjectIDFlagCause.Required().StringVar(&cfg.googleProjectID)
		_, err = a.Parse(os.Args[1:])
		handle(err, a)
	}

	return cfg
}

func handle(err error, application *kingpin.Application) {
	if err != nil {
		fmt.Fprintln(os.Stderr, errors.Wrapf(err, "Error parsing commandline arguments"))
		application.Usage(os.Args[1:])
		os.Exit(2)
	}
}

type writer interface {
	Write(timeseries []*prompb.TimeSeries) error
	Name() string
}

type reader interface {
	Read(req *prompb.ReadRequest) (*prompb.ReadResponse, error)
	Name() string
}

func buildClients(logger slog.Logger, cfg *config) ([]writer, []reader) {
	var writers []writer
	var readers []reader

	c := bigquerydb.NewClient(
		logger.With("storage", "bigquery"),
		cfg.googleAPIjsonkeypath,
		cfg.googleProjectID,
		cfg.googleAPIdatasetID,
		cfg.googleAPItableID,
		cfg.remoteTimeout)
	prometheus.MustRegister(c)
	writers = append(writers, c)
	readers = append(readers, c)
	logger.Info("starting up...")
	return writers, readers
}

func serve(logger slog.Logger, addr string, writers []writer, readers []reader) {
	srv := &http.Server{
		Addr: addr,
	}
	idleConnectionClosed := make(chan struct{})

	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGTERM, os.Interrupt)
		oscall := <-sigChan
		logger.Warn("system call received stopping http server...", slog.Any("systemcall", oscall))
		if err := srv.Shutdown(context.Background()); err != nil {
			logger.Error("error while shutting down http server", slog.Any("error", err))
			os.Exit(1)
		}
		close(idleConnectionClosed)
		logger.Warn("http server shutdown, and connections closed")
	}()

	writeHandler := func(w http.ResponseWriter, r *http.Request) {
		logger.Debug("write request received", slog.Any("method", r.Method), slog.Any("path", r.URL.Path))

		begin := time.Now()
		compressed, err := io.ReadAll(r.Body)
		if err != nil {
			logger.Error("read error", slog.Any("error", err.Error()))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			writeErrors.Inc()
			return
		}

		reqBuf, err := snappy.Decode(nil, compressed)
		if err != nil {
			logger.Error("decode error", slog.Any("error", err.Error()))
			http.Error(w, err.Error(), http.StatusBadRequest)
			writeErrors.Inc()
			return
		}

		var req prompb.WriteRequest
		if err := proto.Unmarshal(reqBuf, &req); err != nil {
			logger.Error("unmarshal error", slog.Any("error", err.Error()))
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

		logger.Debug("write request completed", slog.Any("duration", duration))
	}

	readHandler := func(w http.ResponseWriter, r *http.Request) {
		logger.Debug("read request receieved", slog.Any("method", r.Method), slog.Any("path", r.URL.Path))

		begin := time.Now()
		compressed, err := io.ReadAll(r.Body)
		if err != nil {
			logger.Error("read error", slog.Any("error", err.Error()))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			readErrors.Inc()
			return
		}

		reqBuf, err := snappy.Decode(nil, compressed)
		if err != nil {
			logger.Error("decode error", slog.Any("error", err.Error()))
			http.Error(w, err.Error(), http.StatusBadRequest)
			readErrors.Inc()
			return
		}

		var req prompb.ReadRequest
		if err := proto.Unmarshal(reqBuf, &req); err != nil {
			logger.Error("unmarshal error", slog.Any("error", err.Error()))
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
			logger.Warn("error executing query", slog.Any("query", req), slog.Any("storage", reader.Name()), slog.Any("error", err))
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
			logger.Warn("error writing response", slog.Any("storage", reader.Name()), slog.Any("error", err))
			readErrors.Inc()
		}
		duration := time.Since(begin).Seconds()
		readProcessingDuration.WithLabelValues(writers[0].Name()).Observe(duration)
		logger.Debug("read request completed", slog.Any("duration", duration))
	}

	http.HandleFunc("/write", otelhttp.NewHandler(http.HandlerFunc(writeHandler), "/write").ServeHTTP)
	http.HandleFunc("/read", otelhttp.NewHandler(http.HandlerFunc(readHandler), "/read").ServeHTTP)

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		logger.Error("failed to listen", slog.Any("addr", addr), slog.Any("error", err))
		os.Exit(1)
	}

	<-idleConnectionClosed
}

func sendSamples(logger slog.Logger, w writer, timeseries []*prompb.TimeSeries) {
	begin := time.Now()
	err := w.Write(timeseries)
	duration := time.Since(begin).Seconds()
	if err != nil {
		logger.Warn("error sending samples to remote storage", slog.Any("error", err), slog.Any("storage", w.Name()), slog.Any("num_samples", len(timeseries)))
		failedSamples.WithLabelValues(w.Name()).Add(float64(len(timeseries)))
		writeErrors.Inc()
	} else {
		logger.Debug("sent samples", slog.Any("num_samples", len(timeseries)))
		sentSamples.WithLabelValues(w.Name()).Add(float64(len(timeseries)))
		sentBatchDuration.WithLabelValues(w.Name()).Observe(duration)
	}
}
