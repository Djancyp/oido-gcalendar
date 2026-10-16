# Oido Google Calendar

Manage Google Calendar events. Authenticated with your own Google OAuth
connection (set up in the extension settings → Connect with Google).

## Setup

1. In Google Cloud, create an OAuth client (type: Web application) and add the
   redirect URI shown by your Oido studio: `https://<studio>/api/oauth/callback`.
2. In the extension settings, paste the **Client ID** and **Client secret**, Save.
3. Click **Connect with Google** and grant access.

## Tools

- `calendar_list_calendars` — list your calendars (id + name).
- `calendar_list_events` — list events from `time_min` (default now), optional
  free-text `query`; recurring events are expanded.
- `calendar_create_event` — create one event.
- `calendar_create_events` — create up to 50 events in one call.
- `calendar_update_event` / `calendar_update_events` — update one / up to 50
  events by ID; empty fields are left unchanged.
- `calendar_delete_event` / `calendar_delete_events` — delete one / up to 50
  events by ID. Find IDs with `calendar_list_events` (`query` + time range).
- `calendar_quick_add` — create an event from natural language ("Lunch tomorrow 1pm").

### Times

- Timed: RFC3339 (`2026-06-24T13:00:00+02:00`), or local time
  (`2026-06-24T13:00:00`) plus `time_zone` (`Europe/Berlin`).
- All-day: `YYYY-MM-DD`. The end date is exclusive (the day after the last day).
- `end` is optional: defaults to 1 hour after start, or 1 day for all-day.
- Repeating events: pass `recurrence`, e.g. `["RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=10"]`,
  instead of creating each one. Recurring timed events need `time_zone`.

### Bulk calls

Bulk tools report `OK`/`FAIL` per item. On partial failure retry only the
`FAIL` items, or events get duplicated. Rate-limited requests are retried
automatically. Attendees get no email unless `send_updates` is `all` or
`externalOnly`.
