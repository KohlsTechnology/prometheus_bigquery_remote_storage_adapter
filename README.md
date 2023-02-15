# BigQuery Remote Storage Adapter for Prometheus

[![Build Status](https://github.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter/actions/workflows/pipeline.yml/badge.svg?branch=master)]((https://github.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter/actions))
[![Go Report Card](https://goreportcard.com/badge/github.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter)](https://goreportcard.com/report/github.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter)
[![Join the chat at https://gitter.im/KohlsTechnology/prometheus_bigquery_remote_storage_adapter](https://badges.gitter.im/KohlsTechnology/prometheus_bigquery_remote_storage_adapter.svg)](https://gitter.im/KohlsTechnology/prometheus_bigquery_remote_storage_adapter?utm_source=badge&utm_medium=badge&utm_campaign=pr-badge&utm_content=badge)

This is a read/write adapter that receives samples via Prometheus's remote write protocol and stores them in Google BigQuery. This adapter is based off code found in the official prometheus repo:

https://github.com/prometheus/prometheus/tree/master/documentation/examples/remote_storage/remote_storage_adapter

Billing MUST be enabled on the GCP project with the destination BigQuery tables. This adapter uses the "streaming inserts" API. More information is available here: https://cloud.google.com/bigquery/streaming-data-into-bigquery#before_you_begin

The table schema for BigQuery can be found in file [bq-schema.json](https://raw.githubusercontent.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter/master/bq-schema.json). You can create a BigQuery dataset and table using the following commands.
```
BQ_DATASET_NAME=prometheus
BQ_TABLE_NAME=metrics
GCP_PROJECT_ID=my-gcp-project-id
bq --location=US mk --dataset $GCP_PROJECT_ID:$BQ_DATASET_NAME
bq mk --table \
  --schema ./bq-schema.json \
  --time_partitioning_field timestamp \
  --time_partitioning_type DAY $GCP_PROJECT_ID:$BQ_DATASET_NAME.$BQ_TABLE_NAME
```

The `tags` field is a JSON string and can be easily extracted. Here is an example query:

```
SELECT metricname, tags, JSON_EXTRACT(tags, '$.some_label')
  AS some_label, value, timestamp
  FROM `your_gcp_project.prometheus.metrics_stream`
  WHERE JSON_EXTRACT(tags, '$.some_label') = "\\"target_label_value\\""
```

Consider enabling partition expiration on the destination table based on your data retention and billing requirements (https://cloud.google.com/bigquery/docs/managing-partitioned-tables#partition-expiration).


## Running directly with googleAPIjsonkeypath

```
./bigquery_remote_storage_adapter \
  --googleAPIjsonkeypath=/secret/gcp_service_account.json \
  --googleAPIdatasetID=prometheus \
  --googleAPItableID=metrics_stream
```

## Running directly Google ADC

Reference: Google Application Default Credentials ([ADC](https://cloud.google.com/docs/authentication/production#automatically))

```
GOOGLE_APPLICATION_CREDENTIALS=../../private.key.json ./bigquery_remote_storage_adapter \
  --googleProjectID=<GCP Project ID> \
  --googleAPIdatasetID=prometheus \
  --googleAPItableID=metrics_stream
```

To show all flags:

```
./bigquery_remote_storage_adapter -h
```

## Deploying To Kubernetes

The recommended installation method is to use the [Prometheus operator](https://github.com/prometheus-operator/prometheus-operator).

Example of deploying the remote read/write adapter using the Prometheus operator:
```
---
apiVersion: monitoring.coreos.com/v1
kind: Prometheus
metadata:
  name: prometheus
  labels:
    prometheus: prometheus
spec:
  replicas: 2
  serviceAccountName: prometheus
  serviceMonitorSelector:
    matchLabels:
      team: frontend
  containers:
    - name: "prometheus-storage-bigquery"
      image: "quay.io/kohlstechnology/prometheus_bigquery_remote_storage_adapter:v0.5.1"
      env:
        - name: "PROMBQ_GCP_PROJECT_ID"
          value: "${PROJECT_ID}"
        - name: PROMBQ_DATASET
          value: "${BIGQUERY_DATASET}"
        - name: PROMBQ_TABLE
          value: "${BIGQUERY_TABLE}"
        - name: PROMBQ_TIMEOUT
          value: "2m"
      imagePullPolicy: IfNotPresent
      resources:
        limits:
          cpu: "5"
          memory: "500Mi"
        requests:
          cpu: "5"
          memory: "500Mi"
  remoteWrite:
    - url: http://localhost:9201/write
      remoteTimeout: 2m
      queueConfig:
        capacity: 500
        maxShards: 200
        minShards: 1
        maxSamplesPerSend: 100
        batchSendDeadline: 5s
        minBackoff: 30ms
        maxBackoff: 100ms
  remoteRead:
    - url: http://localhost:9201/read
      remoteTimeout: 1m
```

Here is an [external tutorial](https://cloud.google.com/community/tutorials/writing-prometheus-metrics-bigquery) that walks through setup, installation, and configuration using the Prometheus operator on GKE.

## Configuration

You can configure this storage adapter either through command line options or environment variables. The latter is required if you're using our docker image.

| Command Line Flag | Environment Variable | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `--googleAPIdatasetID` | `PROMBQ_DATASET` | Yes | | Dataset name as shown in GCP |
| `--googleAPItableID` | `PROMBQ_TABLE` | Yes | | Table name as shown in GCP |
| `--googleAPIjsonkeypath` | `PROMBQ_GCP_JSON` | No | | Path to json keyfile for GCP service account. JSON keyfile also contains project_id. |
| `--googleProjectID` | `PROMBQ_GCP_PROJECT_ID` | Yes (when -googleAPIjsonkeypath is missing)| | The GCP project_id |
| `--send-timeout` | `PROMBQ_TIMEOUT` | No | `30s` | The timeout to use when sending samples to the remote storage |
| `--web.listen-address` | `PROMBQ_LISTEN` | No | `:9201` | Address to listen on for web endpoints |
| `--web.telemetry-path` | `PROMBQ_TELEMETRY` | No | `/metrics` | Address to listen on for web endpoints |
| `--log.level` | `PROMBQ_LOG_LEVEL` | No | `info` | Only log messages with the given severity or above. One of: [debug, info, warn, error] |
| `--log.format` | `PROMBQ_LOG_FORMAT` | No | `logfmt` | Output format of log messages. One of: [logfmt, json] |

## Configuring Prometheus

To configure Prometheus to send samples to this binary, add the following to your `prometheus.yml`:

```yaml
# Remote write configuration (for Google BigQuery).
remote_write:
  - url: "http://localhost:9201/write"

# Remote read configuration (for Google BigQuery).
remote_read:
  - url: "http://localhost:9201/read"

```

## Performance Tuning

You will need to tune the storage adapter based on your needs. You have several levers available...

### Requests & Limits

When running on a container platform (like Kubernetes), it's important to configure the CPU / memory requests and limits properly. You should be able to get away with just a couple hundred megabytes of RAM (make sure request == limit), but the CPU needs will heavily depend on your environment. Set the CPU requests to the minimum you need to achieve the required performance. We recommend setting the limit higher (keep in mind that anything above the request is not guaranteed). Keep an eye on CPU throttling to help tweak your settings.

### Limit Metrics Stored Long-Term

The amount of data you send to BigQuery can be another big constraint. It is easy to overwhelm the BigQuery streaming engine by throwing millions of records at it. You might run into API quota issues or simply have data gaps. We highly recommend not to go crazy when it comes to scrape intervals (<30s) and be very selective on what gets stored long-term. Depending on your needs, it might make sense to calculate and store only aggregated metrics long-term.
Refer to the Prometheus documentation for [remote_write](https://prometheus.io/docs/prometheus/latest/configuration/configuration/#remote_write) and [relabel_config](https://prometheus.io/docs/prometheus/latest/configuration/configuration/#relabel_config) on how to implement this.

### Prometheus Remote Storage (remote_write & queue_config)

Prometheus allows you to tune the write behavior for remote storage. Please refer to their [documentation](https://prometheus.io/docs/practices/remote_write/) for details.
### Example `prometheus.yml`

```
remote_write:
- url: http://localhost:9201/write
  remote_timeout: 2m
  write_relabel_configs:
  - source_labels: [__name__]
    separator: ;
    regex: ALERTS|apiserver_request_.*|kube_namespace_labels
    replacement: $1
    action: keep
  queue_config:
    capacity: 500
    max_shards: 200
    min_shards: 1
    max_samples_per_send: 100
    batch_send_deadline: 5s
    min_backoff: 30ms
    max_backoff: 100ms
remote_read:
- url: http://localhost:9201/read
  remote_timeout: 1m
```

## Building

### Binary

If you just need a local version to test, then the simplest way is to execute:

```
make build
```

### Image

In order to build the docker image, simply execute

```
make image
```

## Releasing

This project is using [goreleaser](https://goreleaser.com). GitHub release creation is automated using Travis
CI. New releases are automatically created when new tags are pushed to the repo.
```
$ TAG=v0.0.2 make tag
```

How to manually create a release without relying on Travis CI.
```
$ TAG=v0.0.2 make tag
$ GITHUB_TOKEN=xxx make clean release
```

## Testing

### Running Unit Tests
```
make test-unit
```

### Running E2E Tests
Running the e2e tests requires a real GCP BigQuery instance to connect to.

```
make gcloud-auth
make bq-setup
make test-e2e
make bq-cleanup
make clean
```

To override the GCP project used for testing set the `GCP_PROJECT_ID` variable.
```
GCP_PROJECT_ID=my-awesome-project make bq-setup
GCP_PROJECT_ID=my-awesome-project make test-e2e
GCP_PROJECT_ID=my-awesome-project make bq-cleanup
```

## Prometheus Metrics Offered

| Metric Name | Metric Type | Short Description |
| --- | --- | --- |
| storage_bigquery_received_samples_total | Counter | Total number of received samples. |
| storage_bigquery_sent_samples_total | Counter | Total number of processed samples sent to remote storage that share the same description. |
| storage_bigquery_failed_samples_total | Counter | Total number of processed samples which failed on send to remote storage that share the same description. |
| storage_bigquery_sent_batch_duration_seconds | Histogram | Duration of sample batch send calls to the remote storage that share the same description. |
| storage_bigquery_write_errors_total | Counter | Total number of write errors to BigQuery. |
| storage_bigquery_read_errors_total | Counter | Total number of read errors from BigQuery |
| storage_bigquery_write_api_seconds | Histogram | Duration of the write api processing that share the same description. |
| storage_bigquery_read_api_seconds | Histogram | Duration of the read api processing that share the same description. |
