BINARY = prometheus_bigquery_remote_storage_adapter
COMMIT := $(shell git rev-parse HEAD)
BRANCH := $(shell git symbolic-ref --short -q HEAD || echo HEAD)
DATE := $(shell date -u +%Y%m%d-%H:%M:%S)
VERSION_PKG = github.com/KohlsTechnology/prometheus_bigquery_remote_storage_adapter/pkg/version
LDFLAGS := "-X ${VERSION_PKG}.Branch=${BRANCH} -X ${VERSION_PKG}.BuildDate=${DATE} \
	-X ${VERSION_PKG}.GitSHA1=${COMMIT}"
TAG?=""
GCP_PROJECT_ID?="kohlsdev-prombq-adaptor"
BQ_DATASET_NAME?="prometheus"
BQ_TABLE_NAME?="metrics"

.PHONY: all
all: build

.PHONY: clean
clean:
	rm -rf $(BINARY) dist/
	go clean -testcache

.PHONY: build
build:
	CGO_ENABLED=0 go build -o $(BINARY) -ldflags $(LDFLAGS)

.PHONY: vendor
vendor:
	go mod vendor

.PHONY: image
image:
	docker build . -t quay.io/kohlstechnology/prometheus_bigquery_remote_storage_adapter:latest

.PHONY: test
test: lint-all test-unit test-e2e

.PHONY: test-unit
test-unit:
	go test -tags=unit -race -v -coverprofile=coverage.unit -covermode=atomic ./...

# Make sure go.mod and go.sum are not modified
.PHONY: test-dirty
test-dirty: vendor build
	go mod tidy
	git diff --exit-code
	# TODO: also check that there are no untracked files, e.g. extra .go

.PHONY: test-e2e
test-e2e:
	GCP_PROJECT_ID=$(GCP_PROJECT_ID) BQ_DATASET_NAME=$(BQ_DATASET_NAME) BQ_TABLE_NAME=$(BQ_TABLE_NAME) go test -tags=e2e -race -v -coverprofile=coverage.e2e -covermode=atomic ./...

.PHONY: gcloud-auth
gcloud-auth:
	gcloud auth application-default login

.PHONY: bq-setup
bq-setup:
	bq --location=US mk --dataset $(GCP_PROJECT_ID):$(BQ_DATASET_NAME)
	bq mk --table --schema ./bq-schema.json --time_partitioning_field timestamp --time_partitioning_type DAY $(GCP_PROJECT_ID):$(BQ_DATASET_NAME).$(BQ_TABLE_NAME)

.PHONY: bq-cleanup
bq-cleanup:
	bq rm -r -f --dataset $(GCP_PROJECT_ID):$(BQ_DATASET_NAME)

# Make sure goreleaser is working
.PHONY: test-release
test-release:
	BRANCH=$(BRANCH) COMMIT=$(COMMIT) DATE=$(DATE) VERSION_PKG=$(VERSION_PKG) goreleaser release --snapshot --skip-publish --clean

.PHONY: golangci-lint
golangci-lint:
	golangci-lint run

.PHONY: lint-all
lint-all: golangci-lint

.PHONY: tag
tag:
	git tag -a $(TAG) -m "Release $(TAG)"
	git push origin $(TAG)

# Requires GITHUB_TOKEN environment variable to be set
.PHONY: release
release:
	BRANCH=$(BRANCH) COMMIT=$(COMMIT) DATE=$(DATE) VERSION_PKG=$(VERSION_PKG) goreleaser release --clean
