package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/pprof"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/modules/publicsettings"
	"juhe-ai/backend-go/internal/store/port"
)

type RouterOptions struct {
	Config                                  config.Config
	Logger                                  *slog.Logger
	PublicSettingsService                   *publicsettings.Service
	SystemAPIIPRateLimitReader              port.SystemAPIIPRateLimitReader
	SystemAPIIPReadRateLimiter              SystemAPIIPReadRateLimiter
	PublicAPIHandler                        http.Handler
	ManagementAPIAuthMiddleware             func(http.Handler) http.Handler
	ManagementProxyOptionsHandler           http.Handler
	ManagementProviderOptionsHandler        http.Handler
	ManagementRouteStrategyOptionsHandler   http.Handler
	ManagementMyRouteStrategyOptionsHandler http.Handler
}

func NewRouter(opts RouterOptions) http.Handler {
	r := chi.NewRouter()
	r.Use(requestIDMiddleware)
	r.Use(recoverMiddleware(opts.Logger))
	clientIPs := newClientIPResolver(opts.Config)

	health := NewHealthHandler(opts.Config, opts.Logger)
	r.Get("/__aisys__/health", health.ServeHTTP)
	r.Route("/__aisys__/api", func(system chi.Router) {
		system.Use(noStoreMiddleware)
		if opts.SystemAPIIPRateLimitReader != nil {
			system.Use(newSystemAPIIPReadRateLimitMiddleware(
				opts.SystemAPIIPRateLimitReader,
				opts.SystemAPIIPReadRateLimiter,
				clientIPs,
				opts.Logger,
			))
		}
		system.Get("/health", health.ServeHTTP)
		if opts.PublicSettingsService != nil {
			publicSettingsHandler := NewPublicSettingsHandler(*opts.PublicSettingsService, opts.Logger)
			system.Get("/settings/public", publicSettingsHandler.ServeHTTP)
		}
		if opts.Config.ManagementAPIEnabled {
			if opts.ManagementAPIAuthMiddleware == nil {
				panic("ManagementAPIAuthMiddleware is required when JUHE_AI_MANAGEMENT_API_ENABLED is true")
			}
			if opts.ManagementProxyOptionsHandler == nil &&
				opts.ManagementProviderOptionsHandler == nil &&
				opts.ManagementRouteStrategyOptionsHandler == nil &&
				opts.ManagementMyRouteStrategyOptionsHandler == nil {
				panic("at least one management API handler is required when JUHE_AI_MANAGEMENT_API_ENABLED is true")
			}
			if opts.ManagementProxyOptionsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/proxies/options", opts.ManagementProxyOptionsHandler.ServeHTTP)
			}
			if opts.ManagementProviderOptionsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/providers/options", opts.ManagementProviderOptionsHandler.ServeHTTP)
			}
			if opts.ManagementRouteStrategyOptionsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/route-strategies/options", opts.ManagementRouteStrategyOptionsHandler.ServeHTTP)
			}
			if opts.ManagementMyRouteStrategyOptionsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/my-route-strategies/options", opts.ManagementMyRouteStrategyOptionsHandler.ServeHTTP)
			}
		}
	})

	if opts.Config.MetricsEnabled {
		r.With(loopbackOnlyMiddleware(clientIPs)).Handle("/__aisys__/metrics", promhttp.Handler())
	}

	if opts.Config.PprofEnabled {
		r.Group(func(debug chi.Router) {
			debug.Use(loopbackOnlyMiddleware(clientIPs))
			debug.Get("/__debug/pprof", pprof.Index)
			debug.Get("/__debug/pprof/*", pprof.Index)
			debug.Get("/__debug/pprof/cmdline", pprof.Cmdline)
			debug.Get("/__debug/pprof/profile", pprof.Profile)
			debug.Get("/__debug/pprof/symbol", pprof.Symbol)
			debug.Get("/__debug/pprof/trace", pprof.Trace)
		})
	}

	if opts.Config.PublicAPIEnabled {
		if opts.PublicAPIHandler == nil {
			panic("PublicAPIHandler is required when JUHE_AI_PUBLIC_API_ENABLED is true")
		}
		r.Handle(publicapi.Prefix, opts.PublicAPIHandler)
		r.Handle(publicapi.Prefix+"/*", opts.PublicAPIHandler)
	}

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "接口不存在")
	})

	return r
}
