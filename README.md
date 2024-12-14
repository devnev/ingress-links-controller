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
<a href="https://monitoring.cluster1.example.org">monitoring.cluster1.example.org</a>
```

For a more complete output example, see [the end-to-end test
output](test/html/output.html).

## Install

For a quick setup, this can be installed on a kubernets cluster with kustomize:

* In its own namespace, using the latest build from the main branch

```sh
kubectl apply -k 'https://github.com/devnev/ingress-links-controller//kustomize/with-namespace?ref=main'
```

* In the current namespace, with resources prefixed to avoid conflicts, using
  the latest build from the main branch

```sh
kubectl apply -k 'https://github.com/devnev/ingress-links-controller//kustomize/with-name-prefix?ref=main'
```

Limitations:

* No ingress is installed for the ingress links controller itself. The page is
  assumed to be internal-only, but there is currently no standard way to specify
  that requests to this ingress should be authenticated.

## More details

This controller watches all `networking.k8s.io/v1.Ingress` objects, and renders
an HTML page from Go templates based on the hosts, paths & annotations of the
ingresses. The HTML page is served at `/`.

Templates can be customised using the CLI. The default templates allow custom
text for links to be specified per-ingress using annotations on the ingress.
Ingresses can opt out of appearing using an annotation.
