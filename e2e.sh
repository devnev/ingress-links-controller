#!/usr/bin/env bash
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
source "${script_dir}/e2e_scripts/prelude"
set -x # Use helper scripts (not functions) to keep set -x output meaningful

cluster=ingress-links-controller-test-cluster
image=devnev/ingress-links-controller:latest
context=kind-$cluster

## Cluster setup

# Port mappings and ingress setup from https://kind.sigs.k8s.io/docs/user/ingress/

if ! kind get clusters | grep --quiet $cluster; then
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

kind load docker-image $image --name $cluster

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
  --expected - \
  --attempts 10 \
  --sleep 2 \
  curl \
  --no-progress-meter \
  --max-time 2 \
  --header 'Host: links.localhost' \
  localhost:8123 <<EOF
<!DOCTYPE html>
<html>
<head>
	<style>
		html { height: 100%; }
		body { margin: 0; height: 100%; display: flex; font-family: sans-serif; color-scheme: light dark; background-color: Canvas; }
		#links { margin: auto; padding: 10px; border-radius: 10px; background-color: light-dark(#eee,#333); }
		a { display: block; margin: 2px; text-align: right; }
	</style>
</head>
<body>
	<div id="links">
		<a class="host" href="https://links.localhost">links.localhost</a>
			<a class="path" href="https://links.localhost/alive">/alive</a>
			<a class="path" href="https://links.localhost/ready">/ready</a>
	</div>
</body>
</html>
EOF

log_success Success!

## Cleanup

kind delete cluster --name $cluster
