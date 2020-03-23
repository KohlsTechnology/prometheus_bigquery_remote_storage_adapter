# BigQuery Remote Storage Adapter for Prometheus

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

## Building

```
go build
```

## Running

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

## Configuring Prometheus

To configure Prometheus to send samples to this binary, add the following to your `prometheus.yml`:

```yaml
# Remote write configuration (for Google BigQuery).
remote_write:
  - url: "http://localhost:9201/write"

```
