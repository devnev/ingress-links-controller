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
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type TemplateData struct {
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

	m, err := manager.New(kubeConf, manager.Options{})
	if err != nil {
		log.Error(err, "Failed to create manager")
		os.Exit(1)
	}

	var hostList atomic.Value

	if err = builder.ControllerManagedBy(m).For(&netv1.Ingress{}).Complete(buildReconciler(m.GetClient(), &hostList)); err != nil {
		log.Error(err, "Failed to create controller")
	}

	m.Add(&manager.Server{
		Name:            "main",
		Server:          buildServer(log, &hostList),
		ShutdownTimeout: shutdownTimeout,
	})

	if err := m.Start(signals.SetupSignalHandler()); !errors.Is(err, context.Canceled) {
		log.Error(err, "Manager failed")
		os.Exit(1)
	}
}

func buildReconciler(kubeClient client.Client, hostList *atomic.Value) reconcile.TypedReconciler[reconcile.Request] {
	return reconcile.Func(func(ctx context.Context, r reconcile.Request) (reconcile.Result, error) {
		is := &netv1.IngressList{}
		if err := kubeClient.List(ctx, is); err != nil {
			return reconcile.Result{}, err
		}

		hostSet := map[string]struct{}{}
		for _, item := range is.Items {
			for _, rule := range item.Spec.Rules {
				if host := rule.Host; host != "" {
					hostSet[host] = struct{}{}
				}
			}
		}

		hostList.Store(slices.Sorted(maps.Keys(hostSet)))

		return reconcile.Result{}, nil
	})
}

func buildServer(log logr.Logger, hostList *atomic.Value) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.Header().Add("Content-Type", "text/html")
		rw.WriteHeader(http.StatusOK)
		hosts, _ := hostList.Load().([]string)
		err := tpl.Execute(rw, TemplateData{Hosts: hosts})
		if err != nil {
			log.Error(err, "Failed to execute template for response")
			panic(http.ErrAbortHandler)
		}
	}))
	return &http.Server{Handler: mux}
}
