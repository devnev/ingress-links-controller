package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

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
	Hosts []*hostValues
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
	Host string
	Path string
	Text template.HTML
}

type pathTemplateValue struct {
	Ingress *netv1.Ingress
	Rule    *netv1.IngressRule
	Path    *netv1.HTTPIngressPath
}

var srvTpl = template.Must(template.New("").Parse(`<!DOCTYPE html>
<html>
<head>
	{{- block "head" .}}
	<style>{{block "style" .}}
		html { height: 100%; }
		body { margin: 0; height: 100%; display: flex; font-family: sans-serif; color-scheme: light dark; background-color: Canvas; }
		#links { margin: auto; padding: 10px; border-radius: 10px; background-color: light-dark(#eee,#333); }
		a { display: block; margin: 2px; text-align: right; }
		{{- end}}
	</style>
	{{- end}}
</head>
<body>
	{{- block "body" .}}
	<div id="links">
	{{- range .Hosts }}
		{{block "hostlink" .}}<a class="host" href="https://{{.Host}}">{{or .Text .Host}}</a>{{end}}
		{{- range .Paths -}}
			{{- if ne .Path "/" }}
			{{block "pathlink" .}}<a class="path" href="https://{{.Host}}{{.Path}}">{{or .Text .Path}}</a>{{end}}
			{{- end -}}
		{{end -}}
	{{end}}
	</div>
	{{- end}}
</body>
</html>
`))

const (
	hostTemplateAnnotation = "ingress-links.nev.dev/host-template"
	pathTemplateAnnotation = "ingress-links.nev.dev/path-template"
	skipAnnotation         = "ingress-links.nev.dev/skip"
)

func main() {
	logf.SetLogger(logr.FromSlogHandler(slog.Default().Handler()))
	log := logf.Log.WithName("ingress-links-controller")

	flag.Usage = usage

	loadTemplates := flag.String("load-templates", "", "Glob pattern for additional templates files to load")
	kubeContext := flag.String("context", "", "Context from kubeconfig to use, if not the selected context")
	shutdownTimeout := flag.Duration("shutdown-timeout", 10*time.Second, "Timeout for graceful shutdown on INT or TERM signal")
	flag.Func("template", "Alternative templates - use name=tpl to create/replace a non-root template", func(s string) error {
		name, text, found := strings.Cut(s, "=")
		if !found || strings.ContainsAny(name, `<>{}'"&`) || strings.ContainsFunc(name, unicode.IsSpace) || strings.ContainsFunc(name, unicode.IsControl) {
			_, err := srvTpl.Parse(s)
			return err
		}
		_, err := srvTpl.New(name).Parse(text)
		return err
	})

	flag.Parse()

	if *loadTemplates != "" {
		if _, err := srvTpl.ParseGlob(*loadTemplates); err != nil {
			log.Error(err, "Failed to parse templates from %s", *loadTemplates)
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

	var pagePtr atomic.Pointer[string]

	_ = m.AddHealthzCheck("ping", healthz.Ping)
	_ = m.AddReadyzCheck("have-page", func(req *http.Request) error {
		if pagePtr.Load() == nil {
			return errors.New("page not rendered")
		}
		return nil
	})

	if err = builder.ControllerManagedBy(m).For(&netv1.Ingress{}).Complete(buildReconciler(log, m.GetClient(), &pagePtr, baseTpl)); err != nil {
		log.Error(err, "Failed to create controller")
	}

	_ = m.Add(&manager.Server{
		Name:            "main",
		Server:          buildServer(log, &pagePtr),
		ShutdownTimeout: shutdownTimeout,
	})

	if err := m.Start(signals.SetupSignalHandler()); !errors.Is(err, context.Canceled) {
		log.Error(err, "Manager failed")
		os.Exit(1)
	}
}

func buildReconciler(log logr.Logger, kubeClient client.Client, pagePtr *atomic.Pointer[string], tpl *template.Template) reconcile.TypedReconciler[reconcile.Request] {
	return reconcile.Func(func(ctx context.Context, r reconcile.Request) (reconcile.Result, error) {
		is := &netv1.IngressList{}
		if err := kubeClient.List(ctx, is); err != nil {
			return reconcile.Result{}, err
		}

		hosts := map[string]*hostValues{}
		var err error
		for _, item := range is.Items {
			if item.Annotations[skipAnnotation] == "true" {
				continue
			}

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
				host := rule.Host
				if host == "" {
					continue
				}

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
					pv := pathValues{
						Host: host,
					}
					switch {
					case path.PathType == nil:
					case *path.PathType == netv1.PathTypeExact:
						pv.Path = path.Path
					case *path.PathType == netv1.PathTypePrefix:
						pv.Path = path.Path
					}

					if pv.Path == "" || hv.Paths[pv.Path] != nil {
						continue
					}

					if pathTpl != nil {
						var sb strings.Builder
						if err := pathTpl.Execute(&sb, pathTemplateValue{
							Path:    &path,
							Ingress: &item,
							Rule:    &rule,
						}); err != nil {
							log.Error(err, "Failed to execute host template for ingress %s/%s")
						} else {
							pv.Text = template.HTML(sb.String())
						}
					}

					hosts[host].Paths[pv.Path] = &pv
				}
			}
		}

		// Sort by each segment of the domains starting from the TLD, i.e. the
		// last segment. Meaning: Subdomains of the same domain are grouped
		// together, and subdomains come after their parent domain if present.
		hostsList := slices.Collect(maps.Values(hosts))
		sort.Slice(hostsList, func(i, j int) bool {
			isegs, jsegs := strings.Split(hostsList[i].Host, "."), strings.Split(hostsList[j].Host, ".")
			for ridx := 0; ridx < len(isegs) && ridx < len(jsegs); ridx++ {
				iseg, jseg := isegs[len(isegs)-ridx-1], jsegs[len(jsegs)-ridx-1]
				if cmp := strings.Compare(iseg, jseg); cmp != 0 {
					return cmp < 0
				}
			}
			return len(isegs) < len(jsegs)
		})

		var sb strings.Builder
		if err := srvTpl.Execute(&sb, &templateValues{Hosts: hostsList}); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to execute page template: %w", err)
		}
		page := sb.String()
		oldPage := pagePtr.Swap(&page)
		if oldPage == nil {
			log.Info("First reconcile completed")
		}

		return reconcile.Result{}, nil
	})
}

func buildServer(log logr.Logger, pagePtr *atomic.Pointer[string]) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		page := pagePtr.Load()
		if page == nil {
			// not ready yet
			http.NotFound(rw, req)
			return
		}

		rw.Header().Add("Content-Type", "text/html")
		rw.WriteHeader(http.StatusOK)
		if _, err := io.Copy(rw, strings.NewReader(*page)); err != nil {
			panic(err.Error())
		}
	}))
	return &http.Server{Handler: mux}
}

func usage() {
	fmt.Fprintf(flag.CommandLine.Output(), "Flags for %s:\n", filepath.Base(os.Args[0]))
	flag.PrintDefaults()
	fmt.Fprintln(flag.CommandLine.Output(), "The current templates are:")
	var names []string
	for _, tpl := range srvTpl.Templates() {
		names = append(names, tpl.Name())
	}
	slices.Sort(names)
	for _, name := range names {
		tpl := srvTpl.Lookup(name)
		if name != "" {
			fmt.Fprintf(flag.CommandLine.Output(), "  %q:\n\t", name)
		} else {
			fmt.Fprintf(flag.CommandLine.Output(), "  root:\n\t")
		}
		fmt.Fprintln(flag.CommandLine.Output(), strings.ReplaceAll(strings.Trim(tpl.Tree.Root.String(), "\n"), "\n", "\n\t"))
	}
}
