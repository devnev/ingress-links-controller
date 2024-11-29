package main

import (
	"context"
	"errors"
	"flag"
	"html/template"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
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
	Hosts []string
}

var tpl = template.Must(template.New(".").Parse(`{{range .Hosts}}<a href="https://{{.}}">{{.}}</a><br>{{printf "\n" }}{{end}}`))

func main() {
	logf.SetLogger(logr.FromSlogHandler(slog.Default().Handler()))
	log := logf.Log.WithName("ingress-links-controller")

	loadTemplates := flag.String("load-templates", "", "Glob pattern for additional templates files to load")
	template := flag.String("template", "", "Alternative root template to render")
	kubeContext := flag.String("context", "", "Context from kubeconfig to use, if not the selected context")
	shutdownTimeout := flag.Duration("shutdown-timeout", 10*time.Second, "Timeout for graceful shutdown on INT or TERM signal")

	flag.Parse()

	if *loadTemplates != "" {
		if _, err := tpl.ParseGlob(*loadTemplates); err != nil {
			log.Error(err, "Failed to parse templates from %s", *loadTemplates)
			os.Exit(1)
		}
	}
	if *template != "" {
		if _, err := tpl.Parse(*template); err != nil {
			log.Error(err, "Failed to parse template from --template flag")
			os.Exit(1)
		}
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

	if err = builder.ControllerManagedBy(m).For(&netv1.Ingress{}).Complete(buildReconciler(m.GetClient(), &data)); err != nil {
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

func buildReconciler(kubeClient client.Client, data *atomic.Pointer[templateValues]) reconcile.TypedReconciler[reconcile.Request] {
	return reconcile.Func(func(ctx context.Context, r reconcile.Request) (reconcile.Result, error) {
		is := &netv1.IngressList{}
		if err := kubeClient.List(ctx, is); err != nil {
			return reconcile.Result{}, err
		}

		hosts := map[string]struct{}{}
		for _, item := range is.Items {
			for _, rule := range item.Spec.Rules {
				if host := rule.Host; host != "" {
					hosts[host] = struct{}{}
				}
			}
		}

		data.Store(&templateValues{Hosts: slices.Sorted(maps.Keys(hosts))})

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

		err := tpl.Execute(rw, data)
		if err != nil {
			log.Error(err, "Failed to execute template for response")
			panic(http.ErrAbortHandler)
		}
	}))
	return &http.Server{Handler: mux}
}
