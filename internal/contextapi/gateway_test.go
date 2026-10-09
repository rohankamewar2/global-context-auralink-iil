package contextapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func gatewayFixture(t *testing.T) (http.Handler, *fakeStore, Config, created) {
	t.Helper()
	local, store, config := fixtureAPI(t)
	voice := create(t, local, config, `{"glid":"900000001","role":"buyer","channel":"voice","active_thread_ref":"req:a"}`)
	gateway, err := GatewayHandler(config, store)
	if err != nil {
		t.Fatal(err)
	}
	return gateway, store, config, voice
}

func TestGatewayExactRouteAndAuthenticationBoundary(t *testing.T) {
	h, f, c, v := gatewayFixture(t)
	body := `{"session_id":"` + v.Session.ID + `"}`
	f.reads = 0
	for _, path := range []string{
		"/", "/readyz", "/v1/demo/subjects", "/v1/sessions",
		"/v1/users/900000001/memory", "/v1/sessions/" + v.Session.ID + "/sarvam-bootstrap",
		"/v1/sarvam/tools/record_callback_request",
		"/v1/sarvam/on-end", "/v1/sarvam/tools/get_thread_context/",
		"/v1//sarvam/tools/get_thread_context", "/v1/sarvam/../sarvam/tools/get_thread_context",
	} {
		for _, method := range []string{"GET", "POST", "OPTIONS"} {
			w := request(h, method, path, body, c.DemoToken, v.Token)
			if w.Code != 404 || w.Header().Get("Location") != "" {
				t.Fatalf("gateway exposed or redirected %s %s: %d", method, path, w.Code)
			}
		}
	}
	for _, tc := range []struct {
		path, body, token, capability string
		code                          int
	}{
		{"/v1/sarvam/tools/get_thread_context", body, "", v.Token, 401},
		{"/v1/sarvam/tools/get_thread_context", body, c.DemoToken, v.Token, 401},
		{"/v1/sarvam/tools/get_thread_context", body, c.ToolToken, "", 404},
		{"/v1/sarvam/tools/get_thread_context", body, c.ToolToken, strings.Repeat("d", 64), 404},
		{"/v1/sarvam/tools/get_thread_context", `{"session_id":"` + v.Session.ID + `","glid":"999"}`, c.ToolToken, v.Token, 400},
		{"/v1/sarvam/tools/get_thread_context", body + `{}`, c.ToolToken, v.Token, 400},
		{"/v1/sarvam/tools/get_thread_context", `{"session_id":"` + strings.Repeat("x", 17000) + `"}`, c.ToolToken, v.Token, 400},
		{"/v1/sarvam/tools/get_thread_context?glid=999", body, c.ToolToken, v.Token, 400},
		{"/v1/sarvam/tools/%67et_thread_context", body, c.ToolToken, v.Token, 400},
	} {
		w := request(h, "POST", tc.path, tc.body, tc.token, tc.capability)
		if w.Code != tc.code || strings.Contains(w.Body.String(), f.snapshot.MD) || strings.Contains(w.Body.String(), v.Token) {
			t.Fatalf("gateway rejected request incorrectly: HTTP %d want %d", w.Code, tc.code)
		}
	}
	if f.reads != 0 {
		t.Fatal("blocked gateway request read customer memory")
	}
}

func TestGatewayFlatVariablesAndMissingFieldMapping(t *testing.T) {
	h, f, c, v := gatewayFixture(t)
	// Unicode and exact canonical bytes survive the provider boundary.
	f.snapshot.MD = "# Buyer\nस्टील की आवश्यकता\n"
	f.snapshot.Hash = digest(f.snapshot.MD)
	f.snapshot.Context.MemoryMD = f.snapshot.MD
	body := `{"session_id":"` + v.Session.ID + `"}`
	for _, path := range []string{"/v1/sarvam/on-start", "/v1/sarvam/tools/get_thread_context", "/v1/sarvam/tools/get_missing_fields"} {
		w := request(h, "POST", path, body, c.ToolToken, v.Token)
		var got map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
			t.Fatalf("gateway mapping failed: %d", w.Code)
		}
		if got["session_id"] != v.Session.ID || got["active_thread_ref"] != "req:a" || got["active_role"] != "buyer" || got["memory_version"] != "1" || got["content_sha256"] != f.snapshot.Hash {
			t.Fatal("gateway changed session, role, thread, version or canonical hash")
		}
		if got["processing_status"] != "published" || got["synthetic"] != true {
			t.Fatal("gateway omitted processing or synthetic labels")
		}
		if path == "/v1/sarvam/tools/get_missing_fields" {
			for _, field := range []string{"memory_md", "current_constraints", "opening_line", "preferred_language"} {
				if _, ok := got[field]; ok {
					t.Fatal("missing-field tool returned unnecessary context")
				}
			}
		} else {
			for _, field := range []string{"session_id", "active_role", "active_thread_ref", "memory_version", "memory_md", "missing_fields", "next_question", "current_constraints", "preferred_language", "opening_line"} {
				if _, ok := got[field].(string); !ok {
					t.Fatalf("%s is not a flat string", field)
				}
			}
			if got["memory_md"] != f.snapshot.MD || strings.Contains(got["current_constraints"].(string), "copper") {
				t.Fatal("canonical Markdown changed or another thread's facts leaked")
			}
		}
		if strings.Contains(w.Body.String(), v.Token) || strings.Contains(w.Body.String(), c.ToolToken) || strings.Contains(w.Body.String(), "capability_sha256") {
			t.Fatal("gateway returned credentials")
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("gateway context could be cached")
		}
	}
}

func TestGatewaySessionIsolationAndFreshness(t *testing.T) {
	for _, change := range []string{"expired", "other-principal", "other-scope", "chat", "pending", "lost-thread", "other-capability"} {
		t.Run(change, func(t *testing.T) {
			h, f, c, v := gatewayFixture(t)
			s := f.sessions[v.Session.ID]
			cap := v.Token
			want := 404
			switch change {
			case "expired":
				s.ExpiresAt = time.Now().Add(-time.Minute)
			case "other-principal":
				s.Principal = "another-principal"
			case "other-scope":
				s.Scope.Role = "seller"
			case "chat":
				s.Channel = "chat"
				want = 409
			case "pending":
				f.accepted++
				want = 503
			case "lost-thread":
				s.Thread = "req:missing"
				want = 409
			case "other-capability":
				_, cap, _ = f.Create(t.Context(), Session{})
			}
			f.sessions[s.ID] = s
			w := request(h, "POST", "/v1/sarvam/on-start", `{"session_id":"`+s.ID+`"}`, c.ToolToken, cap)
			if w.Code != want || strings.Contains(w.Body.String(), "memory_md") {
				t.Fatalf("unsafe session accepted: %d want %d", w.Code, want)
			}
		})
	}
}
