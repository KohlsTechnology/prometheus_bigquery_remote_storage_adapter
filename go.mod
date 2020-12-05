module github.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter

go 1.15

require (
	cloud.google.com/go/bigquery v1.13.0
	github.com/go-kit/kit v0.10.0
	github.com/gogo/protobuf v1.3.1
	github.com/golang/snappy v0.0.2
	github.com/grpc-ecosystem/grpc-gateway v1.15.2 // indirect
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.8.0
	github.com/prometheus/common v0.15.0
	github.com/prometheus/prometheus v2.5.0+incompatible
	github.com/stretchr/testify v1.6.0
	google.golang.org/api v0.35.0
	gopkg.in/alecthomas/kingpin.v2 v2.2.6
)
