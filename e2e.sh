#!/usr/bin/env bash
set -xeo pipefail

cluster=ingress-links-controller-test-cluster
image=docker.io/library/ingress-links-controller:latest
context=kind-$cluster

docker build -q -t $image .

# port mappings and ingress setup from https://kind.sigs.k8s.io/docs/user/ingress/

if ! kind get clusters | grep -q $cluster; then
  kind create cluster -n $cluster --config=- <<EOF
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

kubectl --context $context \
  apply \
  --filename https://kind.sigs.k8s.io/examples/ingress/deploy-ingress-nginx.yaml
kubectl --context $context \
  wait \
  --namespace ingress-nginx \
  pod \
  --selector=app.kubernetes.io/component=controller \
  --for=create
kubectl --context $context \
  wait \
  --namespace ingress-nginx \
  pod \
  --selector=app.kubernetes.io/component=controller \
  --for=condition=ready \
  --timeout=90s

kind load docker-image $image -n $cluster

kubectl --context $context \
  delete \
  --ignore-not-found=true \
  pod \
  controller

kubectl --context $context apply -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: controller
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ingress-links-controller
rules:
- apiGroups: ["networking.k8s.io"]
  resources: ["ingresses"]
  verbs: ["get", "watch", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ingress-links-controller
roleRef:
  kind: ClusterRole
  name: ingress-links-controller
  apiGroup: rbac.authorization.k8s.io
subjects:
- kind: ServiceAccount
  name: controller
  namespace: default
---
apiVersion: v1
kind: Pod
metadata:
  name: controller
  labels:
    app: controller
    image_id: "${image_id}"
spec:
  serviceAccountName: controller
  containers:
  - name: controller
    image: ingress-links-controller:latest
    imagePullPolicy: Never
---
kind: Service
apiVersion: v1
metadata:
  name: controller
spec:
  selector:
    app: controller
  ports:
  - port: 80
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: ingress
spec:
  rules:
  - host: links.localhost
    http:
      paths:
      - pathType: Prefix
        path: /
        backend:
          service:
            name: controller
            port:
              number: 80
EOF

kubectl wait --context $context \
  --namespace default \
  pod \
  --selector=app=controller \
  --for=create

kubectl wait --context $context \
  --namespace default \
  pod \
  --selector=app=controller \
  --for=condition=ready

set +x
for i in $(seq 1 10); do
  sleep 2
  response=$(curl --silent --max-time 2 --header 'Host: links.localhost' localhost:8123)
  if [[ "$response" == '<a href="https://links.localhost">links.localhost</a><br>' ]]; then
    echo "Success!"
    kind delete cluster -n $cluster
    exit 0
  fi
done

echo "last response:"
echo "$response"

exit 1
