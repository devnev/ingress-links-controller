#!/usr/bin/env bash
set -xeo pipefail

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
# Even with `wait --for=create`, we get `error: no matching resources found`
sleep 3


kubectl \
  --context $context \
  wait \
  --namespace ingress-nginx \
  pod \
  --selector=app.kubernetes.io/component=controller \
  --for=condition=ready \
  --timeout=90s

## Service (re)deployment

docker build -q -t $image .

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
  --selector=app.kubernetes.io/name=ingress-links-controller

# Option `wait --for=create` unavailable in CI
# Even with `wait --for=create`, we get `error: no matching resources found`
sleep 3


kubectl wait --context $context \
  --namespace default \
  pod \
  --selector=app.kubernetes.io/name=ingress-links-controller \
  --for=condition=ready

## Check

set +x
expected='<a href="https://links.localhost">links.localhost</a><br>'
for i in $(seq 1 10); do
  sleep 2
  response=$(curl --silent --max-time 2 --header 'Host: links.localhost' localhost:8123)
  if [[ "$response" == "$expected" ]]; then
    echo "Success!"
    kind delete cluster -n $cluster
    exit 0
  fi
done

echo "Response mismatch"
echo "Expected:"
echo "$expected"
ecoh "Last response:"
echo "$response"

exit 1
