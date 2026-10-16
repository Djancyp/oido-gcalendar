package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/api/calendar/v3"
)

// handler serves the MCP tools. cal returns the process-wide Calendar client.
type handler struct {
	cal func() (*calendarAPI, error)
}

// Fields without omitempty are required in the generated input schema.

type ListCalendarsArgs struct{}

type ListEventsArgs struct {
	CalendarID string `json:"calendar_id,omitempty" jsonschema:"Calendar ID (default 'primary')"`
	TimeMin    string `json:"time_min,omitempty" jsonschema:"RFC3339 lower bound (default now), e.g. 2026-06-23T00:00:00Z"`
	TimeMax    string `json:"time_max,omitempty" jsonschema:"RFC3339 upper bound"`
	Query      string `json:"query,omitempty" jsonschema:"Free-text filter over title, description, location and attendees"`
	Count      int    `json:"count,omitempty" jsonschema:"Max events (default 20, max 2500)"`
}

type CreateEventArgs struct {
	CalendarID  string `json:"calendar_id,omitempty" jsonschema:"Calendar ID (default 'primary')"`
	SendUpdates string `json:"send_updates,omitempty" jsonschema:"Email attendees: all, externalOnly or none (default none)"`
	EventInput
}

type CreateEventsArgs struct {
	CalendarID  string       `json:"calendar_id,omitempty" jsonschema:"Calendar ID for all events (default 'primary')"`
	SendUpdates string       `json:"send_updates,omitempty" jsonschema:"Email attendees: all, externalOnly or none (default none)"`
	Events      []EventInput `json:"events" jsonschema:"Events to create (1-50)"`
}

type UpdateEventArgs struct {
	CalendarID  string `json:"calendar_id,omitempty" jsonschema:"Calendar ID (default 'primary')"`
	SendUpdates string `json:"send_updates,omitempty" jsonschema:"Email attendees: all, externalOnly or none (default none)"`
	EventPatch
}

type UpdateEventsArgs struct {
	CalendarID  string       `json:"calendar_id,omitempty" jsonschema:"Calendar ID for all events (default 'primary')"`
	SendUpdates string       `json:"send_updates,omitempty" jsonschema:"Email attendees: all, externalOnly or none (default none)"`
	Updates     []EventPatch `json:"updates" jsonschema:"Changes to apply, one per event (1-50)"`
}

type DeleteEventArgs struct {
	CalendarID  string `json:"calendar_id,omitempty" jsonschema:"Calendar ID (default 'primary')"`
	SendUpdates string `json:"send_updates,omitempty" jsonschema:"Email attendees: all, externalOnly or none (default none)"`
	EventID     string `json:"event_id" jsonschema:"ID of the event to delete"`
}

type DeleteEventsArgs struct {
	CalendarID  string   `json:"calendar_id,omitempty" jsonschema:"Calendar ID for all events (default 'primary')"`
	SendUpdates string   `json:"send_updates,omitempty" jsonschema:"Email attendees: all, externalOnly or none (default none)"`
	EventIDs    []string `json:"event_ids" jsonschema:"IDs of the events to delete (1-50)"`
}

type QuickAddArgs struct {
	CalendarID string `json:"calendar_id,omitempty" jsonschema:"Calendar ID (default 'primary')"`
	Text       string `json:"text" jsonschema:"Natural-language event, e.g. 'Lunch with Sam tomorrow 1pm'"`
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func errResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Error: " + err.Error()}}, IsError: true}
}

func checkBulk(n int) error {
	if n == 0 {
		return errors.New("no items given")
	}
	if n > maxBulk {
		return fmt.Errorf("%d items given, max %d per call: split into several calls", n, maxBulk)
	}
	return nil
}

func eventStart(e *calendar.EventDateTime) string {
	if e == nil {
		return ""
	}
	if e.DateTime != "" {
		return e.DateTime
	}
	return e.Date
}

// bulkResult reports one line per item, in input order. The call is flagged
// as an error only when every item failed: on partial failure the model should
// retry just the failed items, not resend the whole batch and duplicate events.
func bulkResult(verb string, errs []error, line func(i int) string) *mcp.CallToolResult {
	var b strings.Builder
	failed := 0
	for i, err := range errs {
		if err != nil {
			failed++
			fmt.Fprintf(&b, "FAIL #%d: %v\n", i+1, err)
			continue
		}
		fmt.Fprintf(&b, "OK   #%d %s\n", i+1, line(i))
	}
	head := fmt.Sprintf("%s %d/%d.", verb, len(errs)-failed, len(errs))
	if failed > 0 {
		head += fmt.Sprintf(" %d failed: retry only the FAIL items.", failed)
	}
	res := textResult(head + "\n\n" + b.String())
	res.IsError = failed == len(errs)
	return res
}

func (h *handler) ListCalendars(ctx context.Context, _ *mcp.CallToolRequest, _ ListCalendarsArgs) (*mcp.CallToolResult, any, error) {
	c, err := h.cal()
	if err != nil {
		return errResult(err), nil, nil
	}
	cals, err := c.ListCalendars(ctx)
	if err != nil {
		return errResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Calendars (%d):\n\nID | Summary\n", len(cals))
	for _, cal := range cals {
		fmt.Fprintf(&b, "%s | %s\n", cal.Id, cal.Summary)
	}
	return textResult(b.String()), nil, nil
}

func (h *handler) ListEvents(ctx context.Context, _ *mcp.CallToolRequest, a ListEventsArgs) (*mcp.CallToolResult, any, error) {
	c, err := h.cal()
	if err != nil {
		return errResult(err), nil, nil
	}
	events, more, err := c.ListEvents(ctx, a.CalendarID, a.TimeMin, a.TimeMax, a.Query, a.Count)
	if err != nil {
		return errResult(err), nil, nil
	}
	if len(events) == 0 {
		return textResult("No events found."), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Events (%d):\n\nID | Start | End | Summary | Location\n", len(events))
	for _, e := range events {
		fmt.Fprintf(&b, "%s | %s | %s | %s | %s\n", e.Id, eventStart(e.Start), eventStart(e.End), e.Summary, e.Location)
	}
	if more {
		b.WriteString("\nMore events exist: narrow time_min/time_max or raise count.\n")
	}
	return textResult(b.String()), nil, nil
}

func (h *handler) CreateEvent(ctx context.Context, _ *mcp.CallToolRequest, a CreateEventArgs) (*mcp.CallToolResult, any, error) {
	if err := checkSendUpdates(a.SendUpdates); err != nil {
		return errResult(err), nil, nil
	}
	c, err := h.cal()
	if err != nil {
		return errResult(err), nil, nil
	}
	ev, err := c.CreateEvent(ctx, a.CalendarID, a.SendUpdates, a.EventInput)
	if err != nil {
		return errResult(err), nil, nil
	}
	return textResult(fmt.Sprintf("Created event %q (id: %s) %s", ev.Summary, ev.Id, ev.HtmlLink)), nil, nil
}

func (h *handler) CreateEvents(ctx context.Context, _ *mcp.CallToolRequest, a CreateEventsArgs) (*mcp.CallToolResult, any, error) {
	if err := errors.Join(checkBulk(len(a.Events)), checkSendUpdates(a.SendUpdates)); err != nil {
		return errResult(err), nil, nil
	}
	c, err := h.cal()
	if err != nil {
		return errResult(err), nil, nil
	}
	evs, errs := forEach(ctx, a.Events, func(ctx context.Context, in EventInput) (*calendar.Event, error) {
		return c.CreateEvent(ctx, a.CalendarID, a.SendUpdates, in)
	})
	return bulkResult("Created", errs, func(i int) string {
		return fmt.Sprintf("%s | %s | %s", evs[i].Id, eventStart(evs[i].Start), evs[i].Summary)
	}), nil, nil
}

func (h *handler) UpdateEvent(ctx context.Context, _ *mcp.CallToolRequest, a UpdateEventArgs) (*mcp.CallToolResult, any, error) {
	if err := checkSendUpdates(a.SendUpdates); err != nil {
		return errResult(err), nil, nil
	}
	c, err := h.cal()
	if err != nil {
		return errResult(err), nil, nil
	}
	ev, err := c.UpdateEvent(ctx, a.CalendarID, a.SendUpdates, a.EventPatch)
	if err != nil {
		return errResult(err), nil, nil
	}
	return textResult(fmt.Sprintf("Updated event %q (id: %s)", ev.Summary, ev.Id)), nil, nil
}

func (h *handler) UpdateEvents(ctx context.Context, _ *mcp.CallToolRequest, a UpdateEventsArgs) (*mcp.CallToolResult, any, error) {
	if err := errors.Join(checkBulk(len(a.Updates)), checkSendUpdates(a.SendUpdates)); err != nil {
		return errResult(err), nil, nil
	}
	c, err := h.cal()
	if err != nil {
		return errResult(err), nil, nil
	}
	evs, errs := forEach(ctx, a.Updates, func(ctx context.Context, p EventPatch) (*calendar.Event, error) {
		return c.UpdateEvent(ctx, a.CalendarID, a.SendUpdates, p)
	})
	return bulkResult("Updated", errs, func(i int) string {
		return fmt.Sprintf("%s | %s | %s", evs[i].Id, eventStart(evs[i].Start), evs[i].Summary)
	}), nil, nil
}

func (h *handler) DeleteEvent(ctx context.Context, _ *mcp.CallToolRequest, a DeleteEventArgs) (*mcp.CallToolResult, any, error) {
	if err := checkSendUpdates(a.SendUpdates); err != nil {
		return errResult(err), nil, nil
	}
	c, err := h.cal()
	if err != nil {
		return errResult(err), nil, nil
	}
	if err := c.DeleteEvent(ctx, a.CalendarID, a.SendUpdates, a.EventID); err != nil {
		return errResult(err), nil, nil
	}
	return textResult("Deleted event " + a.EventID), nil, nil
}

func (h *handler) DeleteEvents(ctx context.Context, _ *mcp.CallToolRequest, a DeleteEventsArgs) (*mcp.CallToolResult, any, error) {
	if err := errors.Join(checkBulk(len(a.EventIDs)), checkSendUpdates(a.SendUpdates)); err != nil {
		return errResult(err), nil, nil
	}
	c, err := h.cal()
	if err != nil {
		return errResult(err), nil, nil
	}
	_, errs := forEach(ctx, a.EventIDs, func(ctx context.Context, id string) (struct{}, error) {
		return struct{}{}, c.DeleteEvent(ctx, a.CalendarID, a.SendUpdates, id)
	})
	return bulkResult("Deleted", errs, func(i int) string { return a.EventIDs[i] }), nil, nil
}

func (h *handler) QuickAdd(ctx context.Context, _ *mcp.CallToolRequest, a QuickAddArgs) (*mcp.CallToolResult, any, error) {
	c, err := h.cal()
	if err != nil {
		return errResult(err), nil, nil
	}
	ev, err := c.QuickAdd(ctx, a.CalendarID, a.Text)
	if err != nil {
		return errResult(err), nil, nil
	}
	return textResult(fmt.Sprintf("Created event %q (id: %s) start %s", ev.Summary, ev.Id, eventStart(ev.Start))), nil, nil
}

var (
	readOnly    = &mcp.ToolAnnotations{ReadOnlyHint: true}
	additive    = &mcp.ToolAnnotations{DestructiveHint: new(false)}
	overwriting = &mcp.ToolAnnotations{IdempotentHint: true}
)

// newServer registers all tools on a new MCP server.
func newServer(h *handler) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "oido-gcalendar", Version: "1.1.0"}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_list_calendars",
		Description: "List the user's calendars (id + name).",
		Annotations: readOnly,
	}, h.ListCalendars)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_list_events",
		Description: "List events on a calendar (default 'primary') from time_min (default now), optionally filtered by free-text query. Recurring events are expanded into instances.",
		Annotations: readOnly,
	}, h.ListEvents)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_create_event",
		Description: "Create one event: timed (RFC3339) or all-day (YYYY-MM-DD), optionally recurring via RRULE. For several events use calendar_create_events.",
		Annotations: additive,
	}, h.CreateEvent)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_create_events",
		Description: "Create up to 50 events in one call. Same fields per event as calendar_create_event. Reports per-event OK/FAIL; on partial failure retry only the failed ones.",
		Annotations: additive,
	}, h.CreateEvents)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_update_event",
		Description: "Update fields of one event by ID; empty fields are left unchanged. For several events use calendar_update_events.",
		Annotations: overwriting,
	}, h.UpdateEvent)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_update_events",
		Description: "Update up to 50 events in one call, e.g. reschedule or rename a batch. Reports per-event OK/FAIL.",
		Annotations: overwriting,
	}, h.UpdateEvents)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_delete_event",
		Description: "Delete one event by ID. For several events use calendar_delete_events.",
		Annotations: overwriting,
	}, h.DeleteEvent)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_delete_events",
		Description: "Delete up to 50 events by ID in one call. Get IDs from calendar_list_events (use query/time range to find them). Already-deleted events count as OK.",
		Annotations: overwriting,
	}, h.DeleteEvents)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_quick_add",
		Description: "Create an event from natural-language text, e.g. 'Lunch tomorrow 1pm'.",
		Annotations: additive,
	}, h.QuickAdd)
	return server
}

// RunMCPServer registers tools and serves over stdio until stdin closes or
// the process gets SIGINT/SIGTERM.
func RunMCPServer() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Built on first use with a background ctx, so a cancelled first request
	// cannot poison the shared client. Per-request ctx is applied per call.
	cal := sync.OnceValues(func() (*calendarAPI, error) {
		return newCalendarAPI(context.Background())
	})

	log.Println("Oido Google Calendar MCP Server starting on stdio...")
	if err := newServer(&handler{cal: cal}).Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("server error: %v", err)
	}
}
