# Ingress Links Controller

Serves a page linking to all ingress hosts.

## In detail

This controller watches all networking/v1.Ingress objects, and maintains a list
of hosts that appear in ingress' rules. Requests to the HTTP server at / produce
a barebones HTTP page with links to the hosts.

## Example

With an Ingress:

```yaml
version: networking/v1
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
