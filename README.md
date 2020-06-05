# etcd-cert-secret-controller

Is used to sync rancher etcd certs residing on host volumes on master nodes to a secret in selected namespace
so that prometheus-operator can mount that secret and monitor etcd with client cert.

config:
```
$ etcd-cert-secret-controller --help
Usage of etcd-cert-secret-controller:
  -cert-path string
    	the cert path to look in for etcd client certs (default "/etc/kubernetes/ssl")
  -cert-regex string
    	regexp to use when look for etcd certs in cert-path. Will use the fist one found only. (default "kube-etcd.*.pem")
  -listen-address string
    	The address to listen on for HTTP metrics requests. (default ":8080")
  -log-level string
    	loglevel (default "info")
  -secret string
    	configure in which namespace and secret to copy the certs to (default "monitoring/etcd-cert")

```
