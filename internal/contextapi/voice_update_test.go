package contextapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVoiceEditorStringArguments(t *testing.T) {
	update := `{"idempotency_key":"edit-1","kind":"correction","fields":{"quantity":"300"},"evidence":"yes","supersedes_event_id":2}`
	for _, target := range []string{`"3"`, `3`} {
		var got VoiceUpdateRequest
		body := `{"session_id":"x","confirmed":true,"update":` + strings.Replace(update, `"supersedes_event_id":2`, `"supersedes_event_id":`+target, 1) + `}`
		if json.Unmarshal([]byte(body), &got) != nil || got.Update.SupersedesEventID != 3 {
			t.Fatal("provider integer representation rejected")
		}
	}
	for _, target := range []string{`"3.0"`, `3.5`, `"-3"`, `null`, `"03"`, `"99999999999999999999999999"`} {
		var got VoiceUpdateRequest
		body := `{"session_id":"x","confirmed":true,"update":` + strings.Replace(update, `"supersedes_event_id":2`, `"supersedes_event_id":`+target, 1) + `}`
		if json.Unmarshal([]byte(body), &got) == nil {
			t.Fatal("invalid evidence ID accepted")
		}
	}
	for _, stringArgs := range []bool{false, true} {
		var u any = json.RawMessage(update)
		var confirmed any = true
		if stringArgs {
			u = update
			confirmed = "true"
		}
		body, _ := json.Marshal(map[string]any{"session_id": "fixture", "update": u, "confirmed": confirmed})
		var got VoiceUpdateRequest
		if json.Unmarshal(body, &got) != nil || !got.Confirmed || got.Update.Fields["quantity"] != "300" {
			t.Fatal("valid editor format rejected")
		}
	}
	for _, body := range []string{
		`{"session_id":"x","confirmed":"yes","update":{}}`,
		`{"session_id":"x","confirmed":1,"update":{}}`,
		`{"session_id":"x","confirmed":true,"update":null}`,
		`{"session_id":"x","confirmed":true,"update":{"glid":"other"}}`,
		`{"session_id":"x","confirmed":true,"update":{},"glid":"other"}`,
		`{"session_id":"x","confirmed":true,"update":"{} {}"}`,
	} {
		var got VoiceUpdateRequest
		if json.Unmarshal([]byte(body), &got) == nil {
			t.Fatal("malformed/override body accepted")
		}
	}
}

func TestVoiceUpdateBoundary(t *testing.T) {
	h, _, c, voice := gatewayFixture(t)
	valid := VoiceUpdateRequest{SessionID: voice.Session.ID, Confirmed: true, Update: UpdateRequest{IdempotencyKey: "voice:turn-1", Kind: "correction", Fields: map[string]string{"quantity": "300"}, Evidence: "Yes, change to 300", SupersedesEventID: 1}}
	encode := func(v VoiceUpdateRequest) string { b, _ := json.Marshal(v); return string(b) }
	path := "/v1/sarvam/tools/record_user_update"
	for _, tc := range []struct {
		token, cap string
		code       int
	}{
		{"", voice.Token, 401}, {c.DemoToken, voice.Token, 401}, {c.ToolToken, "", 404},
		{c.ToolToken, strings.Repeat("f", 64), 404}, {c.ToolToken, voice.Token, 503},
	} {
		if w := request(h, "POST", path, encode(valid), tc.token, tc.cap); w.Code != tc.code {
			t.Fatalf("got %d want %d", w.Code, tc.code)
		}
	}
	invalid := valid
	invalid.Confirmed = false
	if w := request(h, "POST", path, encode(invalid), c.ToolToken, voice.Token); w.Code != 400 {
		t.Fatal("unconfirmed update accepted")
	}
	if w := request(h, "POST", path, strings.TrimSuffix(encode(valid), "}")+`,"glid":"999"}`, c.ToolToken, voice.Token); w.Code != 400 {
		t.Fatal("identity override accepted")
	}
	local, store, cfg := fixtureAPI(t)
	chat := create(t, local, cfg, `{"glid":"900000001","role":"buyer","channel":"chat","active_thread_ref":"req:a"}`)
	other, _ := GatewayHandler(cfg, store)
	valid.SessionID = chat.Session.ID
	if w := request(other, "POST", path, encode(valid), cfg.ToolToken, chat.Token); w.Code != 409 {
		t.Fatal("chat session accepted for voice tool")
	}
}
