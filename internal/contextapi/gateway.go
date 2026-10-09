package contextapi

import (
	"net/http"

	"github.com/rohankamewar2/global-context-auralink-iil/internal/memory"
	"github.com/rohankamewar2/global-context-auralink-iil/internal/sarvam"
)

// GatewayHandler is a separate, exact-path ingress for scoped provider requests.
// It deliberately does not register local administration, session creation,
// provider URL generation or unfinished lifecycle handlers. Both the service secret
// and the expiring session capability are required; session_id is not a secret.
func GatewayHandler(c Config, store Store) (http.Handler, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	a := &API{config: c, store: store}
	read := a.tool(a.gatewayContext)
	update := a.tool(a.voiceUpdate)
	// Bound concurrent work without trusting client-controlled proxy/IP headers.
	inflight := make(chan struct{}, 8)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// No ServeMux canonical redirects or alternate escaped path aliases.
		if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" {
			fail(w, 400, "unsupported_request_target")
			return
		}
		if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
			write(w, 200, map[string]string{"status": "ok", "mode": "sarvam_scoped_gateway"})
			return
		}
		if r.Method != http.MethodPost || (!gatewayReadPath(r.URL.Path) && r.URL.Path != "/v1/sarvam/tools/record_user_update") {
			fail(w, 404, "route_unavailable")
			return
		}
		select {
		case inflight <- struct{}{}:
			defer func() { <-inflight }()
		default:
			w.Header().Set("Retry-After", "1")
			fail(w, 429, "gateway_busy")
			return
		}
		if r.URL.Path == "/v1/sarvam/tools/record_user_update" {
			update(w, r)
		} else {
			read(w, r)
		}
	}), nil
}

func gatewayReadPath(path string) bool {
	return path == "/v1/sarvam/on-start" ||
		path == "/v1/sarvam/tools/get_thread_context" ||
		path == "/v1/sarvam/tools/get_missing_fields"
}

func (a *API) gatewayContext(w http.ResponseWriter, r *http.Request) {
	v, ok := a.agentSession(w, r)
	if !ok {
		return
	}
	if v.Channel != "voice" {
		fail(w, 409, "voice_session_required")
		return
	}
	s, accepted, err := a.store.Published(r.Context(), v.Scope)
	if err != nil {
		fail(w, 503, "published_memory_unavailable")
		return
	}
	if accepted > s.IncludedRevision {
		fail(w, 503, "memory_refresh_pending")
		return
	}
	c, err := memory.WithThread(s.Context, v.Thread)
	if err != nil {
		fail(w, 409, "selected_thread_requires_reselection")
		return
	}
	if !contextBounded(c) {
		fail(w, 503, "selected_thread_context_exceeds_limit")
		return
	}
	vars, err := sarvam.StartVariables(v.ID, c)
	if err != nil {
		fail(w, 503, "agent_variables_unavailable")
		return
	}
	// Flat strings match Sarvam's ten configured input variable names. They are
	// an application adapter contract, not a claim about provider-native schemas.
	out := map[string]any{
		"schema_version": 1, "processing_status": "published",
		"content_sha256": s.Hash, "included_revision": s.IncludedRevision,
		"latest_accepted_revision": accepted, "historical": c.Historical,
		"synthetic": c.Synthetic, "extraction_mode": c.ExtractionMode,
		"missing_fields_meaning": c.MissingFieldsMeaning,
	}
	for k, value := range vars {
		if r.URL.Path == "/v1/sarvam/tools/get_missing_fields" &&
			(k == "memory_md" || k == "opening_line" || k == "current_constraints" || k == "preferred_language") {
			continue
		}
		out[k] = value
	}
	write(w, 200, out)
}
