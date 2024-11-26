package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"sync/atomic"
	"syscall"

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
		for _, host := range hosts {
			link := html.EscapeString((&url.URL{Scheme: "https", Host: host}).String())
			rw.Write([]byte(fmt.Sprintf(`<a href="%s">%s</a><br>`+"\n", link, html.EscapeString(host))))
		}
	}))

	srvCtx, cancelSrv := context.WithCancel(context.Background())

	sigCtx, sigStop := signal.NotifyContext(srvCtx, os.Interrupt, syscall.SIGTERM)
	defer sigStop()

	grp, grpCtx := errgroup.WithContext(sigCtx)
	grp.Go(func() error {
		defer cancelSrv()
		return m.Start(grpCtx)
	})
	grp.Go(func() error {
		defer cancelSrv()
		if err := http.ListenAndServe("", http.DefaultServeMux); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	if err = grp.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		log.Error(err, "Serving failed")
		os.Exit(1)
	}
}
