package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rohankamewar2/global-context-auralink-iil/internal/contextapi"
	"github.com/rohankamewar2/global-context-auralink-iil/internal/database"
	"github.com/rohankamewar2/global-context-auralink-iil/internal/demoui"
	"github.com/rohankamewar2/global-context-auralink-iil/internal/memory"
	"github.com/rohankamewar2/global-context-auralink-iil/internal/refresh"
	"github.com/rohankamewar2/global-context-auralink-iil/internal/sarvam"
	"github.com/rohankamewar2/global-context-auralink-iil/migrations"
)

func main() {
	role := flag.String("role", "buyer", "buyer or seller synthetic rehearsal")
	flag.Parse()
	if (*role != "buyer" && *role != "seller") || run(*role) != nil {
		fmt.Println("Synthetic UI preview failed")
		os.Exit(1)
	}
}
func run(role string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	admin, err := database.Open(ctx)
	if err != nil {
		return err
	}
	defer admin.Close()
	var random [8]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	schema := "auralink_voice_demo_" + hex.EncodeToString(random[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		return err
	}
	defer func() {
		clean, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		admin.Exec(clean, "DROP SCHEMA "+quoted+" CASCADE")
	}()
	cfg := admin.Config().Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err = migrations.Apply(ctx, pool); err != nil {
		return err
	}
	if _, err = database.SeedDemo(ctx, pool); err != nil {
		return err
	}
	scope := memory.Scope{Tenant: "local_demo", Namespace: "synthetic_demo", GLID: "900000001", Role: "buyer"}
	fields := map[string]string{"product": "Steel rods", "quantity": "200", "unit": "kg", "delivery_city": "Noida"}
	evidence := "Synthetic fixture: I need 200 kg of steel rods in Noida."
	if role == "seller" {
		scope.GLID, scope.Role = "900000002", "seller"
		// All writes are restricted to this newly created disposable schema.
		if _, err = pool.Exec(ctx, `INSERT INTO users(tenant_id,namespace,glid,is_synthetic,profile,provenance)
		 VALUES('local_demo','synthetic_demo','900000002',true,'{"display_name":"Synthetic Seller","fixture_id":"seller-mcb-001"}','{"source":"local_fixture","synthetic":true}');
		 INSERT INTO subject_scopes(user_id,role) SELECT id,'seller' FROM users WHERE glid='900000002';
		 INSERT INTO publication_state(scope_id) SELECT s.id FROM subject_scopes s JOIN users u ON u.id=s.user_id WHERE u.glid='900000002' AND s.role='seller'`); err != nil {
			return err
		}
		fields = map[string]string{"product": "MCB", "quantity": "100", "unit": "pieces", "delivery_city": "Noida"}
		evidence = "Synthetic seller enquiry: customer requests 100 pieces of MCB delivered to Noida. This is customer demand, not seller stock."
	}
	policy := memory.Policy{AsOfDate: time.Now().UTC().Format("2006-01-02"), ActivityDays: 90, ConversationDays: 30, MaxBytes: 6000}
	snapshot, err := memory.Generate(ctx, pool, scope, policy)
	if err != nil {
		return err
	}
	store := contextapi.PGStore{Pool: pool}
	v, cap, err := store.Create(ctx, contextapi.Session{Scope: scope, Principal: "local-demo", Channel: "voice", InitialVersion: snapshot.Version})
	if err != nil {
		return err
	}
	receipt, err := store.AcceptUpdate(ctx, v, cap, contextapi.UpdateRequest{IdempotencyKey: "voice-fixture", Kind: "new_requirement", Fields: fields, Evidence: evidence})
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("tmp", "ui-exports-")
	if err != nil {
		return err
	}
	worker := refresh.Worker{Pool: pool, ExportRoot: root}
	if _, _, err = worker.Step(ctx); err != nil {
		return err
	}
	snapshot, _, err = store.Published(ctx, scope)
	if err != nil {
		return err
	}
	v, cap, err = store.Create(ctx, contextapi.Session{Scope: scope, Principal: "local-demo", Channel: "voice", Thread: receipt.ThreadRef, InitialVersion: snapshot.Version})
	if err != nil {
		return err
	}
	apiConfig, err := contextapi.LoadConfig()
	if err != nil || apiConfig == nil {
		return fmt.Errorf("private API configuration unavailable")
	}
	apiConfig.Subjects = []memory.Scope{scope}
	selectedContext, err := memory.WithThread(snapshot.Context, receipt.ThreadRef)
	if err != nil {
		return err
	}
	vars, err := sarvam.StartVariables(v.ID, selectedContext)
	if err != nil {
		return err
	}
	vars["active_thread_ref"] = receipt.ThreadRef
	artifact := map[string]any{"request_body": map[string]string{"session_id": v.ID}, "session_secret": map[string]string{"value": cap}, "expires_at": v.ExpiresAt, "synthetic": true, "role": role, "export_root": root, "thread_ref": receipt.ThreadRef, "agent_variables": vars}
	if err = os.MkdirAll("artifacts/voice-demo", 0700); err != nil {
		return err
	}
	encoded, _ := json.MarshalIndent(artifact, "", "  ")
	if err = os.WriteFile("artifacts/voice-demo/session.json", encoded, 0600); err != nil {
		return err
	}
	mux := http.NewServeMux()
	demoui.Register(mux)
	if err = contextapi.Register(mux, *apiConfig, store); err != nil {
		return err
	}
	gateway, err := contextapi.GatewayHandler(*apiConfig, store)
	if err != nil {
		return err
	}
	gatewayServer := &http.Server{Addr: "127.0.0.1:8083", Handler: gateway, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	server := &http.Server{Addr: "127.0.0.1:8091", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); worker.Run(ctx) }()
	defer func() { stop(); <-workerDone }()
	go func() {
		<-ctx.Done()
		clean, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		server.Shutdown(clean)
		gatewayServer.Shutdown(clean)
	}()
	go func() {
		if err := gatewayServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Println("Synthetic gateway unavailable on port 8083")
			stop()
		}
	}()
	fmt.Println("Synthetic voice gateway: http://127.0.0.1:8083; UI: http://127.0.0.1:8091/demo; private session: artifacts/voice-demo/session.json")
	err = server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
