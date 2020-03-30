FROM gcr.io/distroless/static

COPY ./prometheus_bigquery_remote_storage_adapter /bigquery_remote_storage_adapter

CMD /bigquery_remote_storage_adapter \
        --googleAPIjsonkeypath=/secret/ssh-privatekey \
        --googleAPIdatasetID=$bigquery_dataset \
        --googleAPItableID=$bigquery_table
