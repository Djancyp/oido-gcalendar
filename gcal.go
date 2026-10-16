package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"sync"
	"time"
	_ "time/tzdata" // CGO_ENABLED=0 builds may run where /usr/share/zoneinfo is missing.

	"golang.org/x/oauth2"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const (
	// maxBulk caps items per bulk call so one tool call stays well under the
	// Calendar per-user rate limit even after retries.
	maxBulk = 50
	// bulkConcurrency is how many Calendar requests a bulk call runs at once.
	bulkConcurrency = 4
	maxRetries      = 4
	localLayout     = "2006-01-02T15:04:05"
)

// retryBase is the first rate-limit backoff; tests shrink it.
var retryBase = 500 * time.Millisecond

// calendarAPI wraps one Calendar client. Build it once per process: each
// calendar.NewService clones http.DefaultTransport, so a client per call means
// a fresh TCP+TLS handshake per call and an idle socket left open for 90s.
type calendarAPI struct{ svc *calendar.Service }

// newCalendarAPI builds a client from the access token oido-core injects at
// launch. The token is fixed for the life of the process.
func newCalendarAPI(ctx context.Context, opts ...option.ClientOption) (*calendarAPI, error) {
	if len(opts) == 0 {
		tok := os.Getenv("GOOGLE_ACCESS_TOKEN")
		if tok == "" {
			return nil, errors.New("not connected: GOOGLE_ACCESS_TOKEN is empty — open the extension settings and click Connect with Google")
		}
		opts = []option.ClientOption{option.WithTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: tok}))}
	}
	svc, err := calendar.NewService(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &calendarAPI{svc: svc}, nil
}

func calOrPrimary(id string) string {
	if id == "" {
		return "primary"
	}
	return id
}

// EventInput describes an event to create.
type EventInput struct {
	Summary     string   `json:"summary" jsonschema:"Event title"`
	Start       string   `json:"start" jsonschema:"Start: RFC3339 (2026-06-24T13:00:00+02:00), local time with time_zone (2026-06-24T13:00:00), or YYYY-MM-DD for an all-day event"`
	End         string   `json:"end,omitempty" jsonschema:"End, same format as start (optional: defaults to 1 hour after start, or 1 day for all-day). All-day end is exclusive"`
	TimeZone    string   `json:"time_zone,omitempty" jsonschema:"IANA time zone, e.g. Europe/Berlin (required for recurring timed events)"`
	Description string   `json:"description,omitempty" jsonschema:"Event description"`
	Location    string   `json:"location,omitempty" jsonschema:"Event location"`
	Attendees   []string `json:"attendees,omitempty" jsonschema:"Attendee email addresses"`
	Recurrence  []string `json:"recurrence,omitempty" jsonschema:"RFC5545 lines, e.g. [\"RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=10\"]"`
}

// EventPatch describes changes to an existing event. Empty fields are left as is.
type EventPatch struct {
	EventID     string `json:"event_id" jsonschema:"ID of the event to update"`
	Summary     string `json:"summary,omitempty" jsonschema:"New title"`
	Description string `json:"description,omitempty" jsonschema:"New description"`
	Location    string `json:"location,omitempty" jsonschema:"New location"`
	Start       string `json:"start,omitempty" jsonschema:"New start, same formats as create"`
	End         string `json:"end,omitempty" jsonschema:"New end, same formats as create"`
	TimeZone    string `json:"time_zone,omitempty" jsonschema:"IANA time zone for start/end"`
}

// parseDateTime accepts RFC3339, or a local time when tz names a zone.
func parseDateTime(s, tz string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return time.Time{}, fmt.Errorf("unknown time_zone %q", tz)
		}
		if t, err := time.ParseInLocation(localLayout, s, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q: want RFC3339 (2026-06-24T13:00:00+02:00), YYYY-MM-DD, or local time plus time_zone", s)
}

// eventTimes resolves start/end. A YYYY-MM-DD start makes an all-day event;
// an empty end defaults to one hour (timed) or one day (all-day) after start.
func eventTimes(start, end, tz string) (*calendar.EventDateTime, *calendar.EventDateTime, error) {
	if start == "" {
		return nil, nil, errors.New("start is required")
	}
	if d, err := time.Parse(time.DateOnly, start); err == nil {
		e := d.AddDate(0, 0, 1)
		if end != "" {
			if e, err = time.Parse(time.DateOnly, end); err != nil {
				return nil, nil, fmt.Errorf("all-day event: end %q must be YYYY-MM-DD too", end)
			}
			if !e.After(d) {
				return nil, nil, errors.New("all-day end must be after start (end is exclusive: use the day after the last day)")
			}
		}
		return &calendar.EventDateTime{Date: start}, &calendar.EventDateTime{Date: e.Format(time.DateOnly)}, nil
	}
	s, err := parseDateTime(start, tz)
	if err != nil {
		return nil, nil, err
	}
	e := s.Add(time.Hour)
	if end != "" {
		if e, err = parseDateTime(end, tz); err != nil {
			return nil, nil, err
		}
		if !e.After(s) {
			return nil, nil, errors.New("end must be after start")
		}
	}
	return &calendar.EventDateTime{DateTime: s.Format(time.RFC3339), TimeZone: tz},
		&calendar.EventDateTime{DateTime: e.Format(time.RFC3339), TimeZone: tz}, nil
}

// patchTime converts one start or end value for a patch. It nulls the other
// representation so an event can switch between timed and all-day.
func patchTime(v, tz string) (*calendar.EventDateTime, error) {
	if _, err := time.Parse(time.DateOnly, v); err == nil {
		return &calendar.EventDateTime{Date: v, NullFields: []string{"DateTime"}}, nil
	}
	t, err := parseDateTime(v, tz)
	if err != nil {
		return nil, err
	}
	return &calendar.EventDateTime{DateTime: t.Format(time.RFC3339), TimeZone: tz, NullFields: []string{"Date"}}, nil
}

func checkSendUpdates(v string) error {
	switch v {
	case "", "all", "externalOnly", "none":
		return nil
	}
	return fmt.Errorf("send_updates must be all, externalOnly or none, got %q", v)
}

// isRateLimited reports whether Google rejected the request for rate limits.
// Such requests were not applied, so retrying is safe even for inserts.
func isRateLimited(err error) bool {
	gerr, ok := errors.AsType[*googleapi.Error](err)
	if !ok {
		return false
	}
	if gerr.Code == http.StatusTooManyRequests {
		return true
	}
	if gerr.Code == http.StatusForbidden {
		for _, e := range gerr.Errors {
			if e.Reason == "rateLimitExceeded" || e.Reason == "userRateLimitExceeded" {
				return true
			}
		}
	}
	return false
}

// withRetry runs fn, retrying rate-limit errors with jittered exponential backoff.
func withRetry[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	backoff := retryBase
	for attempt := 0; ; attempt++ {
		v, err := fn()
		if err == nil || attempt == maxRetries || !isRateLimited(err) {
			return v, err
		}
		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case <-time.After(backoff + rand.N(backoff)):
		}
		backoff *= 2
	}
}

// forEach runs fn over items with bounded concurrency. Results and errors are
// index-aligned with items; one failure does not stop the others.
func forEach[T, R any](ctx context.Context, items []T, fn func(context.Context, T) (R, error)) ([]R, []error) {
	res := make([]R, len(items))
	errs := make([]error, len(items))
	sem := make(chan struct{}, bulkConcurrency)
	var wg sync.WaitGroup
	for i, it := range items {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			res[i], errs[i] = fn(ctx, it)
		})
	}
	wg.Wait()
	return res, errs
}

// ListCalendars returns the user's calendars.
func (c *calendarAPI) ListCalendars(ctx context.Context) ([]*calendar.CalendarListEntry, error) {
	res, err := c.svc.CalendarList.List().Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}

// ListEvents lists events on a calendar, expanding recurring events.
// timeMin/timeMax are RFC3339; empty timeMin defaults to now. query is a
// free-text filter over title, description, location and attendees. more
// reports whether further events exist beyond count.
func (c *calendarAPI) ListEvents(ctx context.Context, calendarID, timeMin, timeMax, query string, count int) (events []*calendar.Event, more bool, err error) {
	if count <= 0 {
		count = 20
	}
	if timeMin == "" {
		timeMin = time.Now().Format(time.RFC3339)
	}
	call := c.svc.Events.List(calOrPrimary(calendarID)).
		SingleEvents(true).
		OrderBy("startTime").
		MaxResults(int64(min(count, 2500))).
		TimeMin(timeMin)
	if timeMax != "" {
		call = call.TimeMax(timeMax)
	}
	if query != "" {
		call = call.Q(query)
	}
	res, err := call.Context(ctx).Do()
	if err != nil {
		return nil, false, err
	}
	return res.Items, res.NextPageToken != "", nil
}

// CreateEvent creates an event, timed or all-day, optionally recurring.
func (c *calendarAPI) CreateEvent(ctx context.Context, calendarID, sendUpdates string, in EventInput) (*calendar.Event, error) {
	if in.Summary == "" {
		return nil, errors.New("summary is required")
	}
	start, end, err := eventTimes(in.Start, in.End, in.TimeZone)
	if err != nil {
		return nil, err
	}
	if len(in.Recurrence) > 0 && start.DateTime != "" && in.TimeZone == "" {
		return nil, errors.New("recurring timed events need time_zone (e.g. Europe/Berlin)")
	}
	ev := &calendar.Event{
		Summary:     in.Summary,
		Description: in.Description,
		Location:    in.Location,
		Start:       start,
		End:         end,
		Recurrence:  in.Recurrence,
	}
	for _, a := range in.Attendees {
		if a != "" {
			ev.Attendees = append(ev.Attendees, &calendar.EventAttendee{Email: a})
		}
	}
	return withRetry(ctx, func() (*calendar.Event, error) {
		call := c.svc.Events.Insert(calOrPrimary(calendarID), ev)
		if sendUpdates != "" {
			call = call.SendUpdates(sendUpdates)
		}
		return call.Context(ctx).Do()
	})
}

// UpdateEvent patches the non-empty fields of p onto an existing event.
func (c *calendarAPI) UpdateEvent(ctx context.Context, calendarID, sendUpdates string, p EventPatch) (*calendar.Event, error) {
	if p.EventID == "" {
		return nil, errors.New("event_id is required")
	}
	ev := &calendar.Event{Summary: p.Summary, Description: p.Description, Location: p.Location}
	var err error
	if p.Start != "" {
		if ev.Start, err = patchTime(p.Start, p.TimeZone); err != nil {
			return nil, err
		}
	}
	if p.End != "" {
		if ev.End, err = patchTime(p.End, p.TimeZone); err != nil {
			return nil, err
		}
	}
	return withRetry(ctx, func() (*calendar.Event, error) {
		call := c.svc.Events.Patch(calOrPrimary(calendarID), p.EventID, ev)
		if sendUpdates != "" {
			call = call.SendUpdates(sendUpdates)
		}
		return call.Context(ctx).Do()
	})
}

// DeleteEvent removes an event. An event that is already deleted (410 Gone)
// counts as success, so a retried bulk delete does not report false failures.
func (c *calendarAPI) DeleteEvent(ctx context.Context, calendarID, sendUpdates, eventID string) error {
	if eventID == "" {
		return errors.New("event_id is required")
	}
	_, err := withRetry(ctx, func() (struct{}, error) {
		call := c.svc.Events.Delete(calOrPrimary(calendarID), eventID)
		if sendUpdates != "" {
			call = call.SendUpdates(sendUpdates)
		}
		return struct{}{}, call.Context(ctx).Do()
	})
	if gerr, ok := errors.AsType[*googleapi.Error](err); ok && gerr.Code == http.StatusGone {
		return nil
	}
	return err
}

// QuickAdd creates an event from natural-language text (e.g. "Lunch tomorrow 1pm").
func (c *calendarAPI) QuickAdd(ctx context.Context, calendarID, text string) (*calendar.Event, error) {
	if text == "" {
		return nil, errors.New("text is required")
	}
	return withRetry(ctx, func() (*calendar.Event, error) {
		return c.svc.Events.QuickAdd(calOrPrimary(calendarID), text).Context(ctx).Do()
	})
}
