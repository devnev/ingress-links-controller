package main

import (
	"context"
	"errors"
	"flag"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	netv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type templateValues struct {
	Hosts map[string]*hostValues
}

type hostValues struct {
	Host  string
	Text  template.HTML
	Paths map[string]*pathValues
}

type hostTemplateValue struct {
	Host    string
	Ingress *netv1.Ingress
	Rule    *netv1.IngressRule
}

type pathValues struct {
	Path string
	Text template.HTML
}

type pathTemplateValue struct {
	Ingress *netv1.Ingress
	Rule    *netv1.IngressRule
	Path    *netv1.HTTPIngressPath
}

var srvTpl = template.Must(template.New(".").Parse(`
{{- range .Hosts}}<a href="https://{{.Host}}">{{or .Text .Host}}</a><br>
{{$host := .Host}}{{range .Paths}}{{if ne .Path "/"}}<a href="https://{{$host}}/{{.Path}}"><br>{{or .Text .Path}}
{{end}}{{end}}{{end}}`))

const (
	hostTemplateAnnotation = "ingress-links.nev.dev/host-template"
	pathTemplateAnnotation = "ingress-links.nev.dev/path-template"
)

func main() {
	logf.SetLogger(logr.FromSlogHandler(slog.Default().Handler()))
	log := logf.Log.WithName("ingress-links-controller")

	loadTemplates := flag.String("load-templates", "", "Glob pattern for additional templates files to load")
	template := flag.String("template", "", "Alternative root template to render")
	kubeContext := flag.String("context", "", "Context from kubeconfig to use, if not the selected context")
	shutdownTimeout := flag.Duration("shutdown-timeout", 10*time.Second, "Timeout for graceful shutdown on INT or TERM signal")

	flag.Parse()

	if *loadTemplates != "" {
		if _, err := srvTpl.ParseGlob(*loadTemplates); err != nil {
			log.Error(err, "Failed to parse templates from %s", *loadTemplates)
			os.Exit(1)
		}
	}
	if *template != "" {
		if _, err := srvTpl.Parse(*template); err != nil {
			log.Error(err, "Failed to parse template from --template flag")
			os.Exit(1)
		}
	}

	baseTpl, err := srvTpl.Clone()
	if err != nil {
		log.Error(err, "Failed to clone templates")
		os.Exit(1)
	}

	kubeConf, err := config.GetConfigWithContext(*kubeContext)
	if err != nil {
		log.Error(err, "Failed to get kubeconfig")
		os.Exit(1)
	}

	m, err := manager.New(kubeConf, manager.Options{
		Metrics:                server.Options{BindAddress: ":8080"},
		HealthProbeBindAddress: ":8081",
		LivenessEndpointName:   "/alive",
		ReadinessEndpointName:  "/ready",
	})
	if err != nil {
		log.Error(err, "Failed to create manager")
		os.Exit(1)
	}

	var data atomic.Pointer[templateValues]

	_ = m.AddHealthzCheck("ping", healthz.Ping)
	_ = m.AddReadyzCheck("have-data", func(req *http.Request) error {
		if data.Load() == nil {
			return errors.New("data not loaded")
		}
		return nil
	})

	if err = builder.ControllerManagedBy(m).For(&netv1.Ingress{}).Complete(buildReconciler(log, m.GetClient(), &data, baseTpl)); err != nil {
		log.Error(err, "Failed to create controller")
	}

	_ = m.Add(&manager.Server{
		Name:            "main",
		Server:          buildServer(log, &data),
		ShutdownTimeout: shutdownTimeout,
	})

	if err := m.Start(signals.SetupSignalHandler()); !errors.Is(err, context.Canceled) {
		log.Error(err, "Manager failed")
		os.Exit(1)
	}
}

func buildReconciler(log logr.Logger, kubeClient client.Client, data *atomic.Pointer[templateValues], tpl *template.Template) reconcile.TypedReconciler[reconcile.Request] {
	return reconcile.Func(func(ctx context.Context, r reconcile.Request) (reconcile.Result, error) {
		is := &netv1.IngressList{}
		if err := kubeClient.List(ctx, is); err != nil {
			return reconcile.Result{}, err
		}

		hosts := map[string]*hostValues{}
		var err error
		for _, item := range is.Items {
			var hostTpl *template.Template
			if template := item.Annotations[hostTemplateAnnotation]; template != "" {
				hostTpl, err = tpl.Clone()
				if err != nil {
					return reconcile.Result{}, err
				}
				if _, err = hostTpl.Parse(template); err != nil {
					log.Error(err, "Failed to parse host template from %s annotation for ingress %s/%s", hostTemplateAnnotation, item.Namespace, item.Name)
					hostTpl = nil
				}
			}

			var pathTpl *template.Template
			if template := item.Annotations[pathTemplateAnnotation]; template != "" {
				pathTpl, err = tpl.Clone()
				if err != nil {
					return reconcile.Result{}, err
				}
				if _, err = pathTpl.Parse(template); err != nil {
					log.Error(err, "Failed to parse path template from %s annotation for ingress %s/%s", pathTemplateAnnotation, item.Namespace, item.Name)
					pathTpl = nil
				}
			}

			for _, rule := range item.Spec.Rules {
				if host := rule.Host; host != "" {
					if hosts[host] == nil {
						hosts[host] = &hostValues{
							Host:  host,
							Paths: map[string]*pathValues{},
						}
					}
					hv := hosts[host]

					if hostTpl != nil {
						var sb strings.Builder
						if err := hostTpl.Execute(&sb, hostTemplateValue{
							Host:    host,
							Ingress: &item,
							Rule:    &rule,
						}); err != nil {
							log.Error(err, "Failed to execute host template for ingress %s/%s")
						} else {
							hv.Text = template.HTML(sb.String())
						}
					}

					for _, path := range rule.HTTP.Paths {
						pv := pathValues{}
						switch {
						case path.PathType == nil:
						case *path.PathType == netv1.PathTypeExact:
							pv.Path = path.Path
						case *path.PathType == netv1.PathTypePrefix:
							pv.Path = path.Path
						}

						if hv.Paths[pv.Path] != nil {
							continue
						}

						if pathTpl != nil {
							var sb strings.Builder
							if err := pathTpl.Execute(&sb, hostTemplateValue{
								Host:    host,
								Ingress: &item,
								Rule:    &rule,
							}); err != nil {
								log.Error(err, "Failed to execute host template for ingress %s/%s")
							} else {
								pv.Text = template.HTML(sb.String())
							}
						}

						if pv.Path != "" {
							hosts[host].Paths[pv.Path] = &pv
						}
					}
				}
			}
		}

		old := data.Swap(&templateValues{Hosts: hosts})
		if old == nil {
			log.Info("First reconcile completed")
		}

		return reconcile.Result{}, nil
	})
}

func buildServer(log logr.Logger, data *atomic.Pointer[templateValues]) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		data := data.Load()
		if data == nil {
			// not ready yet
			http.NotFound(rw, req)
			return
		}

		rw.Header().Add("Content-Type", "text/html")
		rw.WriteHeader(http.StatusOK)

		err := srvTpl.Execute(rw, data)
		if err != nil {
			log.Error(err, "Failed to execute template for response")
			panic(http.ErrAbortHandler)
		}
	}))
	return &http.Server{Handler: mux}
}
