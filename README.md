# Ingress Links Controller

Serves a page linking to all ingress hosts.

## Example

With an Ingress:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
meta:
  namespace: monitoring
  name: monitoring
spec:
  rules:
    - host: monitoring.cluster1.example.org
```

It will serve a page containing:

```html
<a href="https://monitoring.cluster1.example.org">monitoring.cluster1.example.org</a><br>
```

## Install

This can be installed on a kubernets cluster with kustomize:

```sh
kubectl apply -k https://github.com/devnev/ingress-links-controller//kustomize/base?ref=main
```

Limitations:

- No ingress is installed for the ingress links controller itself. There is
  currently no standard way to specify that requests to this ingress should be
  authenticated.

## In detail

This controller watches all `networking.k8s.io/v1.Ingress` objects, and
maintains a list of hosts that appear in ingress' rules. Requests to the HTTP
server at / produce a barebones HTTP page with links to the hosts. The page can
be customized using Go templates.
