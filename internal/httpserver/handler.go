package httpserver

import (
	"encoding/json"
	"net/http"
	"sync/atomic"

	"github.com/hookship/hook-service/internal/buildinfo"
)

// Readiness records whether hookshipd can accept traffic.
type Readiness struct {
	ready atomic.Bool
}

// Set changes the readiness state.
func (r *Readiness) Set(ready bool) {
	r.ready.Store(ready)
}

// Ready reports the current readiness state.
func (r *Readiness) Ready() bool {
	return r.ready.Load()
}

type statusResponse struct {
	Status     string `json:"status"`
	Service    string `json:"service"`
	Version    string `json:"version"`
	Durability string `json:"durability,omitempty"`
}

func newHandler(readiness *Readiness, info buildinfo.Info) http.Handler {
	return newHandlerWithRoutes(readiness, info, Options{})
}

// newHandlerWithRoutes builds the operational mux plus any profile routes.
func newHandlerWithRoutes(readiness *Readiness, info buildinfo.Info, options Options) http.Handler {
	mux := http.NewServeMux()
	ready := readiness.Ready
	if options.ReadinessProbe != nil {
		probe := options.ReadinessProbe
		ready = func() bool { return readiness.Ready() && probe() }
	}
	mux.Handle("/healthz", statusHandler("ok", http.StatusOK, info, options.Durability, func() bool { return true }))
	mux.Handle("/readyz", statusHandler("ready", http.StatusOK, info, options.Durability, ready))
	if options.StatusProvider != nil {
		mux.Handle("/statusz", statuszHandler(options.StatusProvider))
	}
	if options.Register != nil {
		options.Register(mux)
	}
	return mux
}

// statuszHandler serves payload-free operator status JSON. It accepts only GET
// and HEAD and never caches, matching the other operational probes.
func statuszHandler(provider func() any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		_ = json.NewEncoder(w).Encode(provider())
	})
}

func statusHandler(
	successStatus string,
	successCode int,
	info buildinfo.Info,
	durability string,
	available func() bool,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeStatus(w, r, http.StatusMethodNotAllowed, statusResponse{
				Status:     "method_not_allowed",
				Service:    "hookshipd",
				Version:    info.Version,
				Durability: durability,
			})
			return
		}

		if !available() {
			writeStatus(w, r, http.StatusServiceUnavailable, statusResponse{
				Status:     "not_ready",
				Service:    "hookshipd",
				Version:    info.Version,
				Durability: durability,
			})
			return
		}

		writeStatus(w, r, successCode, statusResponse{
			Status:     successStatus,
			Service:    "hookshipd",
			Version:    info.Version,
			Durability: durability,
		})
	})
}

func writeStatus(w http.ResponseWriter, r *http.Request, code int, response statusResponse) {
	w.WriteHeader(code)
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(response)
}
