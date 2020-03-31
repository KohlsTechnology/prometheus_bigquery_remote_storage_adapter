FROM gcr.io/distroless/static:nonroot

COPY ./prometheus_bigquery_remote_storage_adapter /bigquery_remote_storage_adapter

EXPOSE 9201

ENTRYPOINT ["/bigquery_remote_storage_adapter"]
