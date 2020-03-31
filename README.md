# BigQuery Remote Storage Adapter for Prometheus

[![Build Status](https://travis-ci.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter.svg?branch=master)](https://travis-ci.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter)
[![Go Report Card](https://goreportcard.com/badge/github.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter)](https://goreportcard.com/report/github.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter)
[![Docker Repository on Quay](https://quay.io/repository/kohlstechnology/prometheus_bigquery_remote_storage_adapter/status "Docker Repository on Quay")](https://quay.io/repository/kohlstechnology/prometheus_bigquery_remote_storage_adapter)

This is a write adapter that receives samples via Prometheus's remote write protocol and stores them in Google BigQuery. This adapter is based off code found in the official prometheus repo:

https://github.com/prometheus/prometheus/tree/master/documentation/examples/remote_storage/remote_storage_adapter

Remote read is not currently supported by this adapter.

Billing MUST be enabled on the GCP project with the destination BigQuery tables. This adapter uses the "streaming inserts" API. More information is available here: https://cloud.google.com/bigquery/streaming-data-into-bigquery#before_you_begin

The table schema in BigQuery should be the following format:

| Field name | Type | Mode |
| --- | --- | --- |
| metricname | STRING | NULLABLE |
| tags | STRING | NULLABLE |
| value | FLOAT | NULLABLE |
| timestamp | TIMESTAMP | NULLABLE |

It is recommended that the BigQuery table is partitioned on the timestamp column for performance.

The tags field is a json string and can be easily extracted. Here is an example query:

```
SELECT metricname, tags, JSON_EXTRACT(tags, '$.some_label') 
  AS some_label, value, timestamp 
  FROM `your_gcp_project.prometheus.metrics_stream`
  WHERE JSON_EXTRACT(tags, '$.some_label') = "\\"target_label_value\\""
```

## Running directly

```
./bigquery_remote_storage_adapter \
  --googleAPIjsonkeypath=/secret/gcp_service_account.json \
  --googleAPIdatasetID=prometheus \
  --googleAPItableID=metrics_stream
```

To show all flags:

```
./bigquery_remote_storage_adapter -h
```

## Configuration

You can configure this storage adapter either through command line options or environment variables. The later is required if you're using our docker image.

| Command Line Flag | Environment Variable | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `--googleAPIjsonkeypath` | `PROMBQ_GCP_JSON` | Yes | | Path to json keyfile for GCP service account. JSON keyfile also contains project_id. |
| `--googleAPIdatasetID` | `PROMBQ_DATASET` | Yes | | Dataset name as shown in GCP |
| `--googleAPItableID` | `PROMBQ_TABLE` | Yes | | Table name as showon in GCP |
| `--send-timeout` | `PROMBQ_TIMEOUT` | No | `30s` | The timeout to use when sending samples to the remote storage |
| `--web.listen-address` | `PROMBQ_LISTEN` | No | `:9201` | Address to listen on for web endpoints |
| `--web.telemetry-path` | `PROMBQ_TELEMETRY` | No | `/metrics` | Address to listen on for web endpoints |

## Configuring Prometheus

To configure Prometheus to send samples to this binary, add the following to your `prometheus.yml`:

```yaml
# Remote write configuration (for Google BigQuery).
remote_write:
  - url: "http://localhost:9201/write"

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

### Testing

You can execute goreleaser locally in order to test any changes.

```
make clean test-release
```
