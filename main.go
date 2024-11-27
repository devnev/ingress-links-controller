package main

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	netv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func main() {
	logf.SetLogger(logr.FromSlogHandler(slog.Default().Handler()))
	log := logf.Log.WithName("ingress-links-controller")
	m, err := manager.New(config.GetConfigOrDie(), manager.Options{})
	if err != nil {
		log.Error(err, "Failed to create manager")
		os.Exit(1)
	}

	var hostList atomic.Value

	if err = builder.ControllerManagedBy(m).For(&netv1.Ingress{}).Complete(reconcile.Func(func(ctx context.Context, r reconcile.Request) (reconcile.Result, error) {
		is := &netv1.IngressList{}
		if err := m.GetClient().List(ctx, is); err != nil {
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
	})); err != nil {
		log.Error(err, "Failed to create controller")
	}

	http.Handle("GET /{$}", http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.Header().Add("Content-Type", "text/html")
		rw.WriteHeader(http.StatusOK)
		hosts, _ := hostList.Load().([]string)
		err := tpl.Execute(rw, map[string]any{"Hosts": hosts})
		if err != nil {
			log.Error(err, "Failed to execute template for response")
			panic(http.ErrAbortHandler)
		}
	}))
	server := &http.Server{Handler: http.DefaultServeMux}

	srvCtx, cancelSrv := context.WithCancel(context.Background())

	grp, grpCtx := errgroup.WithContext(srvCtx)
	grp.Go(func() error {
		defer cancelSrv()
		defer log.Info("Manager exited")
		return m.Start(grpCtx)
	})
	grp.Go(func() error {
		defer cancelSrv()
		defer log.Info("HTTP server exited")
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	grp.Go(func() error {
		defer cancelSrv()
		defer server.Close()

		sigCtx, sigStop := signal.NotifyContext(srvCtx, os.Interrupt, syscall.SIGTERM)
		defer sigStop()

		select {
		case <-sigCtx.Done():
			shutdownCtx, cancelShutdown := context.WithTimeout(srvCtx, 10*time.Second)
			defer cancelShutdown()
			return server.Shutdown(shutdownCtx)
		case <-grpCtx.Done():
			return nil
		}
	})
	if err = grp.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		log.Error(err, "Serving failed")
		os.Exit(1)
	}
}

var tpl = template.Must(template.New("index").Parse(`{{range .Hosts}}<a href="https://{{.}}">{{.}}</a><br>{{printf "\n" }}{{end}}`))
