package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
)

func TestEventTimes(t *testing.T) {
	tests := []struct {
		name, start, end, tz string
		wantStart, wantEnd   string
		wantErr              string
	}{
		{name: "timed with end", start: "2026-06-24T13:00:00Z", end: "2026-06-24T14:30:00Z", wantStart: "2026-06-24T13:00:00Z", wantEnd: "2026-06-24T14:30:00Z"},
		{name: "timed default 1h", start: "2026-06-24T13:00:00+02:00", wantStart: "2026-06-24T13:00:00+02:00", wantEnd: "2026-06-24T14:00:00+02:00"},
		{name: "local time with zone", start: "2026-06-24T13:00:00", tz: "Europe/Berlin", wantStart: "2026-06-24T13:00:00+02:00", wantEnd: "2026-06-24T14:00:00+02:00"},
		{name: "all-day default 1 day", start: "2026-06-24", wantStart: "2026-06-24", wantEnd: "2026-06-25"},
		{name: "all-day multi day", start: "2026-06-24", end: "2026-06-27", wantStart: "2026-06-24", wantEnd: "2026-06-27"},
		{name: "missing start", wantErr: "start is required"},
		{name: "local time without zone", start: "2026-06-24T13:00:00", wantErr: "invalid time"},
		{name: "bad zone", start: "2026-06-24T13:00:00", tz: "Mars/Olympus", wantErr: "unknown time_zone"},
		{name: "end before start", start: "2026-06-24T13:00:00Z", end: "2026-06-24T12:00:00Z", wantErr: "end must be after start"},
		{name: "all-day mixed end", start: "2026-06-24", end: "2026-06-24T12:00:00Z", wantErr: "must be YYYY-MM-DD too"},
		{name: "all-day same day end", start: "2026-06-24", end: "2026-06-24", wantErr: "exclusive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, e, err := eventTimes(tt.start, tt.end, tt.tz)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := eventStart(s); got != tt.wantStart {
				t.Errorf("start = %q, want %q", got, tt.wantStart)
			}
			if got := eventStart(e); got != tt.wantEnd {
				t.Errorf("end = %q, want %q", got, tt.wantEnd)
			}
		})
	}
}

// fakeCalendar is a minimal Calendar API: inserts and deletes, with summaries
// and IDs that trigger failures.
type fakeCalendar struct {
	conns     atomic.Int32
	limited   atomic.Int32 // remaining 429s for summary "ratelimited"
	mu        sync.Mutex
	nextID    int
	sendFlags []string
}

func (f *fakeCalendar) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	writeErr := func(code int, reason string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"code": code, "message": reason, "errors": []map[string]string{{"reason": reason}},
		}})
	}
	f.mu.Lock()
	f.sendFlags = append(f.sendFlags, r.URL.Query().Get("sendUpdates"))
	f.mu.Unlock()

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/calendars/primary/events":
		var ev calendar.Event
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			writeErr(400, "badRequest")
			return
		}
		switch ev.Summary {
		case "fail":
			writeErr(400, "invalid")
			return
		case "ratelimited":
			if f.limited.Add(-1) >= 0 {
				writeErr(403, "rateLimitExceeded")
				return
			}
		}
		f.mu.Lock()
		f.nextID++
		ev.Id = "ev" + string(rune('0'+f.nextID))
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(ev)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/calendars/primary/events/"):
		switch strings.TrimPrefix(r.URL.Path, "/calendars/primary/events/") {
		case "gone":
			writeErr(410, "deleted")
		case "missing":
			writeErr(404, "notFound")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		writeErr(404, "notFound")
	}
}

// newTestSession wires an MCP client to the server, backed by fakeCalendar
// over TLS so connection reuse is observable.
func newTestSession(t *testing.T) (*mcp.ClientSession, *fakeCalendar) {
	t.Helper()
	retryBase = time.Millisecond
	fake := &fakeCalendar{}
	srv := httptest.NewUnstartedServer(fake)
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			fake.conns.Add(1)
		}
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	tlsConf := srv.Client().Transport.(*http.Transport).TLSClientConfig
	// A fresh transport per construction, like calendar.NewService does in
	// production, so TestClientReusesConnection catches per-call clients.
	cal := sync.OnceValues(func() (*calendarAPI, error) {
		hc := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConf.Clone()}}
		return newCalendarAPI(context.Background(), option.WithEndpoint(srv.URL+"/"), option.WithHTTPClient(hc))
	})
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	if _, err := newServer(&handler{cal: cal}).Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, fake
}

func callText(t *testing.T, cs *mcp.ClientSession, name string, args any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func TestCreateEventsPartialFailure(t *testing.T) {
	cs, fake := newTestSession(t)
	fake.limited.Store(2)

	text, isErr := callText(t, cs, "calendar_create_events", map[string]any{
		"send_updates": "all",
		"events": []map[string]any{
			{"summary": "Standup", "start": "2026-06-24T09:00:00Z"},
			{"summary": "fail", "start": "2026-06-24T10:00:00Z"},
			{"summary": "ratelimited", "start": "2026-06-24"},
			{"summary": "Bad time", "start": "tomorrow"},
		},
	})
	if isErr {
		t.Errorf("IsError = true on partial failure; want false so the model retries only failed items")
	}
	for _, want := range []string{"Created 2/4.", "2 failed", "OK   #1", "FAIL #2", "OK   #3", "FAIL #4: invalid time"} {
		if !strings.Contains(text, want) {
			t.Errorf("result missing %q:\n%s", want, text)
		}
	}
	if !slices.Contains(fake.sendFlags, "all") {
		t.Errorf("sendUpdates not forwarded, got %v", fake.sendFlags)
	}
}

func TestCreateEventsAllFailedIsError(t *testing.T) {
	cs, _ := newTestSession(t)
	_, isErr := callText(t, cs, "calendar_create_events", map[string]any{
		"events": []map[string]any{{"summary": "fail", "start": "2026-06-24"}},
	})
	if !isErr {
		t.Error("IsError = false when every item failed")
	}
}

func TestBulkLimits(t *testing.T) {
	cs, _ := newTestSession(t)
	ids := make([]string, maxBulk+1)
	for i := range ids {
		ids[i] = "x"
	}
	tests := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{"empty", map[string]any{"event_ids": []string{}}, "no items"},
		{"too many", map[string]any{"event_ids": ids}, "max 50"},
		{"bad send_updates", map[string]any{"event_ids": []string{"a"}, "send_updates": "everyone"}, "send_updates must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, isErr := callText(t, cs, "calendar_delete_events", tt.args)
			if !isErr || !strings.Contains(text, tt.wantErr) {
				t.Errorf("got (%v) %q, want error containing %q", isErr, text, tt.wantErr)
			}
		})
	}
}

func TestDeleteEvents(t *testing.T) {
	cs, _ := newTestSession(t)
	text, isErr := callText(t, cs, "calendar_delete_events", map[string]any{
		"event_ids": []string{"a1", "gone", "missing"},
	})
	if isErr {
		t.Fatalf("unexpected IsError: %s", text)
	}
	for _, want := range []string{"Deleted 2/3.", "OK   #1 a1", "OK   #2 gone", "FAIL #3"} {
		if !strings.Contains(text, want) {
			t.Errorf("result missing %q:\n%s", want, text)
		}
	}
}

// One shared client must reuse its TLS connection across tool calls.
func TestClientReusesConnection(t *testing.T) {
	cs, fake := newTestSession(t)
	for range 5 {
		if text, isErr := callText(t, cs, "calendar_delete_event", map[string]any{"event_id": "a"}); isErr {
			t.Fatal(text)
		}
	}
	if n := fake.conns.Load(); n != 1 {
		t.Errorf("opened %d connections for 5 sequential calls, want 1", n)
	}
}

func TestToolSchemasRequiredFields(t *testing.T) {
	cs, _ := newTestSession(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	type schema struct {
		Required   []string           `json:"required"`
		Properties map[string]*schema `json:"properties"`
		Items      *schema            `json:"items"`
	}
	got := map[string]*schema{}
	for _, tool := range res.Tools {
		b, _ := json.Marshal(tool.InputSchema)
		var s schema
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatal(err)
		}
		got[tool.Name] = &s
	}
	tests := []struct {
		tool string
		path []string // property path to the object to check; "[]" steps into items
		want []string
	}{
		{"calendar_list_calendars", nil, nil},
		{"calendar_list_events", nil, nil},
		{"calendar_create_event", nil, []string{"start", "summary"}},
		{"calendar_create_events", nil, []string{"events"}},
		{"calendar_create_events", []string{"events", "[]"}, []string{"start", "summary"}},
		{"calendar_update_event", nil, []string{"event_id"}},
		{"calendar_update_events", []string{"updates", "[]"}, []string{"event_id"}},
		{"calendar_delete_event", nil, []string{"event_id"}},
		{"calendar_delete_events", nil, []string{"event_ids"}},
		{"calendar_quick_add", nil, []string{"text"}},
	}
	for _, tt := range tests {
		t.Run(tt.tool+strings.Join(tt.path, "."), func(t *testing.T) {
			s := got[tt.tool]
			for _, p := range tt.path {
				if s == nil {
					break
				}
				if p == "[]" {
					s = s.Items
				} else {
					s = s.Properties[p]
				}
			}
			if s == nil {
				t.Fatalf("schema path %v not found", tt.path)
			}
			req := slices.Sorted(slices.Values(s.Required))
			if !slices.Equal(req, tt.want) {
				t.Errorf("required = %v, want %v", req, tt.want)
			}
		})
	}
}
