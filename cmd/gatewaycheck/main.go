// gatewaycheck exercises only loopback HTTP and local PostgreSQL. It does not
// contact Sarvam, start a tunnel or create dataset events. Optional preparation
// saves a single expiring session to an ignored private file for a manual test.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rohankamewar2/global-context-auralink-iil/internal/contextapi"
	"github.com/rohankamewar2/global-context-auralink-iil/internal/database"
	"github.com/rohankamewar2/global-context-auralink-iil/internal/memory"
)

type checker struct {
	base   string
	http   *http.Client
	config contextapi.Config
	store  contextapi.PGStore
}

func main() {
	base := flag.String("url", "http://127.0.0.1:8082", "HTTP loopback gateway URL")
	glid := flag.String("prepare-glid", "", "optional allowlisted GLID for a private manual-test session")
	role := flag.String("prepare-role", "", "buyer or seller; required with prepare-glid")
	thread := flag.String("thread", "", "explicit thread for the prepared session; blank leaves selection unresolved")
	flag.Parse()
	if err := run(*base, *glid, *role, *thread); err != nil {
		slog.Error("local gateway check failed", "reason", err.Error())
		os.Exit(1)
	}
}

func localURL(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()) &&
		u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.Path == ""
}

func run(base, glid, role, thread string) error {
	if !localURL(base) {
		return errors.New("gateway check requires an HTTP loopback URL without path, query or credentials")
	}
	if (glid == "") != (role == "") || (role != "" && role != "buyer" && role != "seller") || len(thread) > 256 || glid == "" && thread != "" {
		return errors.New("preparation requires an allowlisted GLID and buyer/seller role; thread is optional")
	}
	c := checker{base: base, http: &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	var health map[string]any
	if err := c.call("GET", "/healthz", nil, "", "", 200, &health); err != nil {
		return err
	}
	if health["mode"] != "sarvam_scoped_gateway" && health["mode"] != "sarvam_read_gateway" {
		return errors.New("target is not the restricted Sarvam gateway")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := database.Open(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	cfg, err := contextapi.LoadConfig()
	if err != nil {
		return err
	}
	if cfg == nil {
		return errors.New("backend access tokens are not configured")
	}
	c.config, c.store = *cfg, contextapi.PGStore{Pool: pool}
	var chosen memory.Scope
	if glid != "" {
		for _, s := range cfg.Subjects {
			if s.GLID == glid && s.Role == role {
				chosen = s
			}
		}
		if chosen.GLID == "" {
			return errors.New("prepared subject is not in the private demo allowlist")
		}
	}
	for _, path := range []string{"/v1/demo/subjects", "/v1/sessions", "/v1/users/1/memory", "/v1/sarvam/on-end", "/v1/sarvam/tools/record_callback_request"} {
		if err := c.call("POST", path, map[string]string{}, cfg.ToolToken, "", 404, nil); err != nil {
			return err
		}
	}
	for _, scope := range cfg.Subjects {
		snapshot, revision, err := c.store.Published(ctx, scope)
		if err != nil || revision > snapshot.IncludedRevision {
			return errors.New("sample memory is unavailable or has a pending refresh")
		}
		selected := ""
		if len(snapshot.Context.Requirements) != 0 {
			// Selection is only a local test fixture, not an agent/user choice.
			selected = snapshot.Context.Requirements[0].ThreadRef
		}
		v, cap, err := c.create(ctx, scope, selected)
		if err != nil {
			return err
		}
		_, otherCap, err := c.create(ctx, scope, selected)
		if err != nil {
			return err
		}
		body := map[string]string{"session_id": v.ID}
		for _, test := range []struct {
			token, capability string
			want              int
		}{{"", cap, 401}, {cfg.DemoToken, cap, 401}, {cfg.ToolToken, "", 404}, {cfg.ToolToken, otherCap, 404}} {
			if err := c.call("POST", "/v1/sarvam/tools/get_thread_context", body, test.token, test.capability, test.want, nil); err != nil {
				return err
			}
		}
		var got map[string]any
		for _, path := range []string{"/v1/sarvam/on-start", "/v1/sarvam/tools/get_thread_context"} {
			if err := c.call("POST", path, body, cfg.ToolToken, cap, 200, &got); err != nil {
				return err
			}
			if err := verify(got, v, snapshot); err != nil {
				return err
			}
		}
		var missing map[string]any
		if err := c.call("POST", "/v1/sarvam/tools/get_missing_fields", body, cfg.ToolToken, cap, 200, &missing); err != nil {
			return err
		}
		if _, found := missing["memory_md"]; found || missing["active_thread_ref"] != selected || missing["session_id"] != v.ID {
			return errors.New("missing-field response leaked context or changed session selection")
		}
		if err := c.call("POST", "/v1/sarvam/on-start", map[string]string{"session_id": v.ID, "glid": "1"}, cfg.ToolToken, cap, 400, nil); err != nil {
			return err
		}
	}
	if glid != "" {
		v, capability, err := c.create(ctx, chosen, thread)
		if err != nil {
			return err
		}
		var values map[string]any
		if err := c.call("POST", "/v1/sarvam/on-start", map[string]string{"session_id": v.ID}, cfg.ToolToken, capability, 200, &values); err != nil {
			return err
		}
		vars := map[string]string{}
		for _, key := range []string{"session_id", "active_role", "active_thread_ref", "memory_version", "memory_md", "missing_fields", "next_question", "current_constraints", "preferred_language", "opening_line"} {
			value, ok := values[key].(string)
			if !ok {
				return errors.New("prepared gateway variables are incomplete")
			}
			vars[key] = value
		}
		path, err := save("session", map[string]any{
			"mode": "manual_single_session_test", "expires_at": v.ExpiresAt,
			"request_body": map[string]string{"session_id": v.ID}, "agent_variables": vars,
			"session_secret": map[string]string{"header_name": "X-Auralink-Session-Token", "value": capability},
			"historical":     values["historical"], "synthetic": values["synthetic"],
		})
		if err != nil {
			return err
		}
		slog.Info("private manual-test session saved; do not share or commit this file", "path", path, "expires_at", v.ExpiresAt, "capability_in_agent_variables", false)
	}
	path, err := save("check", map[string]any{
		"checked_at": time.Now().UTC(), "status": "passed", "subject_roles": len(cfg.Subjects),
		"canonical_memory_verified": true, "cross_session_capabilities_rejected": true,
		"local_admin_routes_exposed": false, "tunnel_started": false,
		"provider_integration_tested": false, "writes_enabled": health["mode"] == "sarvam_scoped_gateway",
	})
	if err != nil {
		return err
	}
	slog.Info("local gateway verified", "subject_roles", len(cfg.Subjects), "report", path, "provider_integration", false)
	return nil
}

func (c checker) create(ctx context.Context, scope memory.Scope, thread string) (contextapi.Session, string, error) {
	s, revision, err := c.store.Published(ctx, scope)
	if err != nil || revision > s.IncludedRevision {
		return contextapi.Session{}, "", errors.New("selected memory is unavailable or pending refresh")
	}
	if _, err := memory.WithThread(s.Context, thread); err != nil {
		return contextapi.Session{}, "", errors.New("requested thread is not in the selected published memory")
	}
	return c.store.Create(ctx, contextapi.Session{Scope: scope, Principal: "local-demo", Channel: "voice", Thread: thread, InitialVersion: s.Version})
}

func verify(got map[string]any, v contextapi.Session, snapshot memory.Snapshot) error {
	md, ok := got["memory_md"].(string)
	hash := sha256.Sum256([]byte(md))
	if !ok || md != snapshot.MD || got["content_sha256"] != hex.EncodeToString(hash[:]) || got["content_sha256"] != snapshot.Hash ||
		got["session_id"] != v.ID || got["active_role"] != v.Scope.Role || got["active_thread_ref"] != v.Thread ||
		got["memory_version"] != strconv.FormatInt(snapshot.Version, 10) || got["processing_status"] != "published" {
		return errors.New("gateway changed canonical memory, hash or bound session variables")
	}
	return nil
}

func (c checker) call(method, path string, body any, token, capability string, want int, out any) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return errors.New("could not encode local check request")
		}
	}
	r, err := http.NewRequest(method, c.base+path, bytes.NewReader(data))
	if err != nil {
		return errors.New("could not prepare local check request")
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if capability != "" {
		r.Header.Set("X-Auralink-Session-Token", capability)
	}
	response, err := c.http.Do(r)
	if err != nil {
		return errors.New("could not reach loopback gateway; start go run ./cmd/gateway")
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		return fmt.Errorf("local gateway returned HTTP %d; expected %d", response.StatusCode, want)
	}
	if out != nil && json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(out) != nil {
		return errors.New("could not decode local gateway response")
	}
	return nil
}

func save(kind string, value any) (string, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", errors.New("could not encode private gateway artifact")
	}
	dir := filepath.Join("artifacts", "gateway")
	if os.MkdirAll(dir, 0700) != nil {
		return "", errors.New("could not create private artifact directory")
	}
	path := filepath.Join(dir, time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+kind+".json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", errors.New("could not create private gateway artifact")
	}
	_, writeErr := f.Write(append(data, '\n'))
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return "", errors.New("could not save private gateway artifact")
	}
	return strings.ReplaceAll(path, "\\", "/"), nil
}
