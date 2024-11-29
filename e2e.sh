#!/usr/bin/env bash
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
source "${script_dir}/e2e_scripts/prelude"
set -x # Use helper scripts (not functions) to keep set -x output meaningful

cluster=ingress-links-controller-test-cluster
image=devnev/ingress-links-controller:latest
context=kind-$cluster

## Cluster setup

# Port mappings and ingress setup from https://kind.sigs.k8s.io/docs/user/ingress/

if ! kind get clusters | grep -q $cluster; then
  kind create cluster --name $cluster --config=- <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 80
    hostPort: 8123
    protocol: TCP
  - containerPort: 443
    hostPort: 443
    protocol: TCP
EOF
fi

kubectl \
  --context $context \
  apply \
  --filename https://kind.sigs.k8s.io/examples/ingress/deploy-ingress-nginx.yaml

# Option `wait --for=create` unavailable in CI
# Even with `wait --for=create`, we can get `error: no matching resources found`
sleep 5
run_if_ci sleep 10
run_if_not_ci \
  kubectl \
  --context $context \
  wait \
  --namespace ingress-nginx \
  pod \
  --selector=app.kubernetes.io/component=controller \
  --for=create

kubectl \
  --context $context \
  wait \
  --namespace ingress-nginx \
  pod \
  --selector=app.kubernetes.io/component=controller \
  --for=condition=ready \
  --timeout=90s

## Service (re)deployment

docker build --quiet --tag $image .

kind load docker-image $image -n $cluster

kubectl \
  --context $context \
  apply \
  --kustomize ./kustomize/e2e

# Make sure pod actually restarts
kubectl \
  --context $context \
  delete \
  --ignore-not-found=true \
  pod \
  --namespace ingress-links \
  --selector=app.kubernetes.io/name=ingress-links-controller

# Option `wait --for=create` unavailable in CI
# Even with `wait --for=create`, we can get `error: no matching resources found`
sleep 5
run_if_ci sleep 10
run_if_not_ci \
  kubectl \
  --context $context \
  wait \
  --namespace ingress-links \
  pod \
  --selector=app.kubernetes.io/name=ingress-links-controller \
  --for=create

kubectl \
  --context $context \
  wait \
  --namespace ingress-links \
  pod \
  --selector=app.kubernetes.io/name=ingress-links-controller \
  --for=condition=ready

## Check

expect_output \
  --expected '<a href="https://links.localhost">links.localhost</a><br>' \
  --attempts 10 \
  --sleep 2 \
  curl \
  --no-progress-meter \
  --max-time 2 \
  --header 'Host: links.localhost' \
  localhost:8123

log_success Success!

## Cleanup

kind delete cluster -n $cluster
