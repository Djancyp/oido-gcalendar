package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
)

func nowRFC3339() string { return time.Now().Format(time.RFC3339) }

// calendarService builds a Calendar client from the injected access token.
func calendarService(ctx context.Context) (*calendar.Service, error) {
	tok := os.Getenv("GOOGLE_ACCESS_TOKEN")
	if tok == "" {
		return nil, fmt.Errorf("not connected: GOOGLE_ACCESS_TOKEN is empty — open the extension settings and click Connect with Google")
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: tok})
	return calendar.NewService(ctx, option.WithTokenSource(ts))
}

func calOrPrimary(id string) string {
	if id == "" {
		return "primary"
	}
	return id
}

// ListCalendars returns the user's calendars.
func ListCalendars(ctx context.Context) ([]*calendar.CalendarListEntry, error) {
	svc, err := calendarService(ctx)
	if err != nil {
		return nil, err
	}
	res, err := svc.CalendarList.List().Do()
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}

// ListEvents lists upcoming events on a calendar. timeMin/timeMax are RFC3339;
// empty timeMin defaults to now.
func ListEvents(ctx context.Context, calendarID, timeMin, timeMax string, count int) ([]*calendar.Event, error) {
	svc, err := calendarService(ctx)
	if err != nil {
		return nil, err
	}
	if count <= 0 {
		count = 20
	}
	call := svc.Events.List(calOrPrimary(calendarID)).
		SingleEvents(true).
		OrderBy("startTime").
		MaxResults(int64(count))
	if timeMin == "" {
		timeMin = nowRFC3339()
	}
	call = call.TimeMin(timeMin)
	if timeMax != "" {
		call = call.TimeMax(timeMax)
	}
	res, err := call.Do()
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}

// CreateEvent creates a timed event. start/end are RFC3339 timestamps.
func CreateEvent(ctx context.Context, calendarID, summary, description, location, start, end string, attendees []string) (*calendar.Event, error) {
	svc, err := calendarService(ctx)
	if err != nil {
		return nil, err
	}
	ev := &calendar.Event{
		Summary:     summary,
		Description: description,
		Location:    location,
		Start:       &calendar.EventDateTime{DateTime: start},
		End:         &calendar.EventDateTime{DateTime: end},
	}
	for _, a := range attendees {
		if a != "" {
			ev.Attendees = append(ev.Attendees, &calendar.EventAttendee{Email: a})
		}
	}
	return svc.Events.Insert(calOrPrimary(calendarID), ev).Do()
}

// UpdateEvent patches non-empty fields of an existing event.
func UpdateEvent(ctx context.Context, calendarID, eventID, summary, description, location, start, end string) (*calendar.Event, error) {
	svc, err := calendarService(ctx)
	if err != nil {
		return nil, err
	}
	ev := &calendar.Event{}
	if summary != "" {
		ev.Summary = summary
	}
	if description != "" {
		ev.Description = description
	}
	if location != "" {
		ev.Location = location
	}
	if start != "" {
		ev.Start = &calendar.EventDateTime{DateTime: start}
	}
	if end != "" {
		ev.End = &calendar.EventDateTime{DateTime: end}
	}
	return svc.Events.Patch(calOrPrimary(calendarID), eventID, ev).Do()
}

// DeleteEvent removes an event.
func DeleteEvent(ctx context.Context, calendarID, eventID string) error {
	svc, err := calendarService(ctx)
	if err != nil {
		return err
	}
	return svc.Events.Delete(calOrPrimary(calendarID), eventID).Do()
}

// QuickAdd creates an event from natural-language text (e.g. "Lunch tomorrow 1pm").
func QuickAdd(ctx context.Context, calendarID, text string) (*calendar.Event, error) {
	svc, err := calendarService(ctx)
	if err != nil {
		return nil, err
	}
	return svc.Events.QuickAdd(calOrPrimary(calendarID), text).Do()
}
