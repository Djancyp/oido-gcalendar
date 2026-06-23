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
- `calendar_list_events` — list upcoming events (default calendar `primary`).
- `calendar_create_event` — create a timed event (start/end in RFC3339).
- `calendar_update_event` — update fields of an event by ID.
- `calendar_delete_event` — delete an event by ID.
- `calendar_quick_add` — create an event from natural language ("Lunch tomorrow 1pm").
