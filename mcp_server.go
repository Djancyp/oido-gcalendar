package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type handler struct{}

type ListCalendarsArgs struct{}

type ListEventsArgs struct {
	CalendarID string `json:"calendar_id" jsonschema:"Calendar ID (default 'primary')"`
	TimeMin    string `json:"time_min" jsonschema:"RFC3339 lower bound (default now), e.g. 2026-06-23T00:00:00Z"`
	TimeMax    string `json:"time_max" jsonschema:"RFC3339 upper bound (optional)"`
	Count      int    `json:"count" jsonschema:"Max events (default 20)"`
}

type CreateEventArgs struct {
	CalendarID  string   `json:"calendar_id" jsonschema:"Calendar ID (default 'primary')"`
	Summary     string   `json:"summary" jsonschema:"Event title"`
	Description string   `json:"description" jsonschema:"Event description (optional)"`
	Location    string   `json:"location" jsonschema:"Event location (optional)"`
	Start       string   `json:"start" jsonschema:"Start time, RFC3339, e.g. 2026-06-24T13:00:00Z"`
	End         string   `json:"end" jsonschema:"End time, RFC3339"`
	Attendees   []string `json:"attendees" jsonschema:"Attendee email addresses (optional)"`
}

type UpdateEventArgs struct {
	CalendarID  string `json:"calendar_id" jsonschema:"Calendar ID (default 'primary')"`
	EventID     string `json:"event_id" jsonschema:"ID of the event to update"`
	Summary     string `json:"summary" jsonschema:"New title (optional)"`
	Description string `json:"description" jsonschema:"New description (optional)"`
	Location    string `json:"location" jsonschema:"New location (optional)"`
	Start       string `json:"start" jsonschema:"New start (RFC3339, optional)"`
	End         string `json:"end" jsonschema:"New end (RFC3339, optional)"`
}

type DeleteEventArgs struct {
	CalendarID string `json:"calendar_id" jsonschema:"Calendar ID (default 'primary')"`
	EventID    string `json:"event_id" jsonschema:"ID of the event to delete"`
}

type QuickAddArgs struct {
	CalendarID string `json:"calendar_id" jsonschema:"Calendar ID (default 'primary')"`
	Text       string `json:"text" jsonschema:"Natural-language event, e.g. 'Lunch with Sam tomorrow 1pm'"`
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func errResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Error: " + err.Error()}}, IsError: true}
}

func (h *handler) ListCalendars(ctx context.Context, _ *mcp.CallToolRequest, _ ListCalendarsArgs) (*mcp.CallToolResult, any, error) {
	cals, err := ListCalendars(ctx)
	if err != nil {
		return errResult(err), nil, nil
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Calendars (%d):\n\nID | Summary\n", len(cals)))
	for _, c := range cals {
		b.WriteString(fmt.Sprintf("%s | %s\n", c.Id, c.Summary))
	}
	return textResult(b.String()), nil, nil
}

func (h *handler) ListEvents(ctx context.Context, _ *mcp.CallToolRequest, a ListEventsArgs) (*mcp.CallToolResult, any, error) {
	events, err := ListEvents(ctx, a.CalendarID, a.TimeMin, a.TimeMax, a.Count)
	if err != nil {
		return errResult(err), nil, nil
	}
	if len(events) == 0 {
		return textResult("No events found."), nil, nil
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Events (%d):\n\nID | Start | Summary\n", len(events)))
	for _, e := range events {
		start := ""
		if e.Start != nil {
			start = e.Start.DateTime
			if start == "" {
				start = e.Start.Date
			}
		}
		b.WriteString(fmt.Sprintf("%s | %s | %s\n", e.Id, start, e.Summary))
	}
	return textResult(b.String()), nil, nil
}

func (h *handler) CreateEvent(ctx context.Context, _ *mcp.CallToolRequest, a CreateEventArgs) (*mcp.CallToolResult, any, error) {
	ev, err := CreateEvent(ctx, a.CalendarID, a.Summary, a.Description, a.Location, a.Start, a.End, a.Attendees)
	if err != nil {
		return errResult(err), nil, nil
	}
	return textResult(fmt.Sprintf("Created event %q (id: %s) %s", ev.Summary, ev.Id, ev.HtmlLink)), nil, nil
}

func (h *handler) UpdateEvent(ctx context.Context, _ *mcp.CallToolRequest, a UpdateEventArgs) (*mcp.CallToolResult, any, error) {
	ev, err := UpdateEvent(ctx, a.CalendarID, a.EventID, a.Summary, a.Description, a.Location, a.Start, a.End)
	if err != nil {
		return errResult(err), nil, nil
	}
	return textResult(fmt.Sprintf("Updated event %q (id: %s)", ev.Summary, ev.Id)), nil, nil
}

func (h *handler) DeleteEvent(ctx context.Context, _ *mcp.CallToolRequest, a DeleteEventArgs) (*mcp.CallToolResult, any, error) {
	if err := DeleteEvent(ctx, a.CalendarID, a.EventID); err != nil {
		return errResult(err), nil, nil
	}
	return textResult("Deleted event " + a.EventID), nil, nil
}

func (h *handler) QuickAdd(ctx context.Context, _ *mcp.CallToolRequest, a QuickAddArgs) (*mcp.CallToolResult, any, error) {
	ev, err := QuickAdd(ctx, a.CalendarID, a.Text)
	if err != nil {
		return errResult(err), nil, nil
	}
	return textResult(fmt.Sprintf("Created event %q (id: %s)", ev.Summary, ev.Id)), nil, nil
}

// RunMCPServer registers tools and serves over stdio.
func RunMCPServer() {
	h := &handler{}
	server := mcp.NewServer(&mcp.Implementation{Name: "oido-gcalendar", Version: "1.0.0"}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_list_calendars",
		Description: "List the user's calendars (id + name).",
	}, h.ListCalendars)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_list_events",
		Description: "List upcoming events on a calendar (default 'primary').",
	}, h.ListEvents)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_create_event",
		Description: "Create a timed event with start/end in RFC3339.",
	}, h.CreateEvent)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_update_event",
		Description: "Update fields of an existing event by ID.",
	}, h.UpdateEvent)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_delete_event",
		Description: "Delete an event by ID.",
	}, h.DeleteEvent)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_quick_add",
		Description: "Create an event from natural-language text, e.g. 'Lunch tomorrow 1pm'.",
	}, h.QuickAdd)

	log.Println("Oido Google Calendar MCP Server starting on stdio...")
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
