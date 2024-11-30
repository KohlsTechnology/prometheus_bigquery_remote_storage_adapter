FROM golang:1.23.3 AS builder

WORKDIR /go/src/github.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter
COPY . .
RUN make build

FROM gcr.io/distroless/static:nonroot

EXPOSE 9201

COPY --from=builder /go/src/github.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter/prometheus_bigquery_remote_storage_adapter /bigquery_remote_storage_adapter

ENTRYPOINT ["/bigquery_remote_storage_adapter"]
