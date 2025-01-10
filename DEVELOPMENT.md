# Setup Test Environment

```shell
make gcloud-auth
make bq-setup
kind create cluster
make image
kind load docker-image quay.io/kohlstechnology/prometheus_bigquery_remote_storage_adapter:latest
git clone https://github.com/prometheus-operator/kube-prometheus.git
kubectl apply --server-side -f kube-prometheus/manifests/setup
kubectl wait \
        --for condition=Established \
        --all CustomResourceDefinition \
        --namespace=monitoring
kubectl apply -f kube-prometheus/manifests/
kns monitoring
kubectl create secret generic gcpcred --from-file=gcp.json=$HOME/.config/gcloud/application_default_credentials.json
kubectl patch prometheus k8s --patch-file=prometheus-patch.yaml
```

When you are done
```shell
make bq-cleanup
```
