package google

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/tools"
)

// Google Calendar API v3 (developers.google.com/workspace/calendar/api/v3/reference).

type calTime struct {
	DateTime string `json:"dateTime,omitempty"`
	Date     string `json:"date,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
}

func (t calTime) String() string {
	if t.DateTime != "" {
		return t.DateTime
	}
	return t.Date + " (all day)"
}

type calAttendee struct {
	Email          string `json:"email"`
	ResponseStatus string `json:"responseStatus,omitempty"`
	Self           bool   `json:"self,omitempty"`
	Organizer      bool   `json:"organizer,omitempty"`
}

type calEvent struct {
	ID          string        `json:"id"`
	Status      string        `json:"status,omitempty"`
	Summary     string        `json:"summary,omitempty"`
	Description string        `json:"description,omitempty"`
	Location    string        `json:"location,omitempty"`
	Start       calTime       `json:"start"`
	End         calTime       `json:"end"`
	Attendees   []calAttendee `json:"attendees,omitempty"`
	HTMLLink    string        `json:"htmlLink,omitempty"`
	Organizer   struct {
		Email string `json:"email"`
	} `json:"organizer"`
}

func calendarID(c tools.Call) string {
	if v := strings.TrimSpace(c.String("calendar")); v != "" {
		return v
	}
	return "primary"
}

// attendeesOf reads the attendees argument (array of emails or a
// comma-separated string).
func attendeesOf(c tools.Call) []string {
	var a struct {
		Attendees any `json:"attendees"`
	}
	_ = c.Decode(&a)
	var out []string
	switch v := a.Attendees.(type) {
	case string:
		for _, x := range strings.Split(v, ",") {
			if x = strings.TrimSpace(x); x != "" {
				out = append(out, x)
			}
		}
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	}
	return out
}

func calendarTools(s *Service) []*tools.Tool {
	eventProps := map[string]any{
		"calendar":    tools.Prop("string", "Calendar id (default \"primary\")."),
		"summary":     tools.Prop("string", "Event title."),
		"start":       tools.Prop("string", "Start: RFC 3339 date-time with offset (2026-11-20T15:00:00+08:00) or a date (2026-11-20) for all-day."),
		"end":         tools.Prop("string", "End, same format as start (exclusive date for all-day events)."),
		"time_zone":   tools.Prop("string", "IANA time zone for the times (optional, e.g. Asia/Shanghai)."),
		"description": tools.Prop("string", "Description (optional)."),
		"location":    tools.Prop("string", "Location (optional)."),
		"attendees":   arrayOf("string", "Guest email addresses (optional). Guests receive an invitation."),
	}
	updateProps := map[string]any{"id": tools.Prop("string", "Event id from calendar_list_events.")}
	for k, v := range eventProps {
		updateProps[k] = v
	}
	updateProps["attendees"] = arrayOf("string", "Replace the guest list (optional). Guests are notified.")
	return []*tools.Tool{
		{
			Name:        "calendar_list_events",
			Description: "List events of the connected Google Calendar in a time range (default: the next 7 days), optionally filtered by text.",
			Params: tools.Schema(map[string]any{
				"time_min": tools.Prop("string", "Range start, RFC 3339 (default now)."),
				"time_max": tools.Prop("string", "Range end, RFC 3339 (default 7 days after time_min)."),
				"calendar": tools.Prop("string", "Calendar id (default \"primary\")."),
				"q":        tools.Prop("string", "Free-text filter (optional)."),
				"max":      tools.Prop("integer", "Maximum events, 1–100 (default 25)."),
			}),
			Category: tools.CategoryRead,
			Classify: func(c tools.Call) (string, string) { return tools.CategoryRead, "查看日程 " + calendarID(c) },
			Execute:  s.calList,
		},
		{
			Name:        "calendar_free_busy",
			Description: "Busy time ranges for one or more calendars (people's emails or calendar ids) between time_min and time_max — use it to find a free slot.",
			Params: tools.Schema(map[string]any{
				"time_min":  tools.Prop("string", "Range start, RFC 3339."),
				"time_max":  tools.Prop("string", "Range end, RFC 3339."),
				"calendars": arrayOf("string", "Calendar ids / emails (default: the user's primary calendar)."),
				"time_zone": tools.Prop("string", "IANA time zone for the answer (optional)."),
			}, "time_min", "time_max"),
			Category: tools.CategoryRead,
			Classify: func(c tools.Call) (string, string) { return tools.CategoryRead, "查询空闲时间" },
			Execute:  s.calFreeBusy,
		},
		{
			Name:        "calendar_create_event",
			Description: "Create an event in the connected Google Calendar. With attendees Google emails them an invitation, so the user is asked to approve.",
			Params:      tools.Schema(eventProps, "summary", "start", "end"),
			Category:    tools.CategoryWrite,
			Classify: func(c tools.Call) (string, string) {
				sum := "创建日程「" + clip(c.String("summary"), 60) + "」" + c.String("start")
				if g := attendeesOf(c); len(g) > 0 {
					return tools.CategoryPublish, sum + "，并邀请 " + clip(strings.Join(g, ", "), 160)
				}
				return tools.CategoryWrite, sum
			},
			Execute: s.calCreate,
		},
		{
			Name:        "calendar_update_event",
			Description: "Change fields of an event (only the fields given). If the event has guests they are notified, so the user is asked to approve.",
			Params:      tools.Schema(updateProps, "id"),
			Category:    tools.CategoryWrite,
			Classify: func(c tools.Call) (string, string) {
				sum := "修改日程 " + c.String("id")
				if t := c.String("summary"); t != "" {
					sum += "「" + clip(t, 60) + "」"
				}
				if g := attendeesOf(c); len(g) > 0 {
					return tools.CategoryPublish, sum + "，并通知 " + clip(strings.Join(g, ", "), 160)
				}
				if s.eventHasGuests(c) {
					return tools.CategoryPublish, sum + "（会通知已邀请的人）"
				}
				return tools.CategoryWrite, sum
			},
			Execute: s.calUpdate,
		},
		{
			Name:        "calendar_delete_event",
			Description: "Delete (cancel) an event. Guests are notified of the cancellation.",
			Params: tools.Schema(map[string]any{
				"id":       tools.Prop("string", "Event id."),
				"calendar": tools.Prop("string", "Calendar id (default \"primary\")."),
			}, "id"),
			Category: tools.CategoryDelete,
			Classify: func(c tools.Call) (string, string) { return tools.CategoryDelete, "删除日程 " + c.String("id") },
			Execute:  s.calDelete,
		},
		{
			Name:        "calendar_rsvp",
			Description: "Answer an invitation as the connected user: accepted, declined or tentative. The organizer is told.",
			Params: tools.Schema(map[string]any{
				"id":       tools.Prop("string", "Event id."),
				"response": map[string]any{"type": "string", "enum": []string{"accepted", "declined", "tentative"}, "description": "The answer."},
				"calendar": tools.Prop("string", "Calendar id (default \"primary\")."),
			}, "id", "response"),
			Category: tools.CategoryPublish,
			Classify: func(c tools.Call) (string, string) {
				word := map[string]string{"accepted": "接受", "declined": "拒绝", "tentative": "暂定"}[c.String("response")]
				return tools.CategoryPublish, "回复日程邀请 " + c.String("id") + "：" + word
			},
			Execute: s.calRSVP,
		},
	}
}

func (s *Service) eventsURL(cal string) string {
	return s.ep.Calendar + "/calendars/" + url.PathEscape(cal) + "/events"
}

// eventHasGuests reports whether an existing event has guests (for the
// approval category of an update; best effort).
func (s *Service) eventHasGuests(c tools.Call) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var ev calEvent
	if err := s.getJSON(ctx, s.eventsURL(calendarID(c))+"/"+url.PathEscape(c.String("id")), &ev); err != nil {
		return true // unknown: be careful
	}
	for _, a := range ev.Attendees {
		if !a.Self {
			return true
		}
	}
	return false
}

func formatEvent(b *strings.Builder, ev calEvent) {
	fmt.Fprintf(b, "\n- id=%s\n  %s\n  %s → %s\n", ev.ID, oneLine(ev.Summary), ev.Start, ev.End)
	if ev.Location != "" {
		fmt.Fprintf(b, "  Location: %s\n", oneLine(ev.Location))
	}
	if len(ev.Attendees) > 0 {
		var g []string
		for _, a := range ev.Attendees {
			x := a.Email
			if a.ResponseStatus != "" {
				x += " (" + a.ResponseStatus + ")"
			}
			if a.Self {
				x += " [you]"
			}
			g = append(g, x)
		}
		fmt.Fprintf(b, "  Guests: %s\n", clip(strings.Join(g, ", "), 600))
	}
	if ev.Description != "" {
		fmt.Fprintf(b, "  %s\n", clip(oneLine(ev.Description), 300))
	}
}

func (s *Service) calList(ctx context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		TimeMin string `json:"time_min"`
		TimeMax string `json:"time_max"`
		Q       string `json:"q"`
		Max     int    `json:"max"`
	}
	if err := c.Decode(&a); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	from := time.Now()
	if a.TimeMin != "" {
		t, err := time.Parse(time.RFC3339, a.TimeMin)
		if err != nil {
			return tools.Errorf("time_min must be RFC 3339 (2026-11-20T09:00:00+08:00)"), nil
		}
		from = t
	}
	to := from.Add(7 * 24 * time.Hour)
	if a.TimeMax != "" {
		t, err := time.Parse(time.RFC3339, a.TimeMax)
		if err != nil {
			return tools.Errorf("time_max must be RFC 3339"), nil
		}
		to = t
	}
	if a.Max <= 0 {
		a.Max = 25
	}
	q := url.Values{
		"timeMin": {from.Format(time.RFC3339)}, "timeMax": {to.Format(time.RFC3339)},
		"singleEvents": {"true"}, "orderBy": {"startTime"}, "maxResults": {fmt.Sprint(min(a.Max, 100))},
	}
	if a.Q != "" {
		q.Set("q", a.Q)
	}
	var out struct {
		Items    []calEvent `json:"items"`
		TimeZone string     `json:"timeZone"`
	}
	if err := s.getJSON(ctx, s.eventsURL(calendarID(c))+"?"+q.Encode(), &out); err != nil {
		return toolErr(err), nil
	}
	if len(out.Items) == 0 {
		return tools.Text(fmt.Sprintf("No events between %s and %s.", from.Format(time.RFC3339), to.Format(time.RFC3339))), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d events (calendar time zone %s):\n", len(out.Items), out.TimeZone)
	for _, ev := range out.Items {
		formatEvent(&b, ev)
	}
	return tools.Text(capText(b.String(), maxList, "narrow the time range")), nil
}

func (s *Service) calFreeBusy(ctx context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		TimeMin   string   `json:"time_min"`
		TimeMax   string   `json:"time_max"`
		Calendars []string `json:"calendars"`
		TimeZone  string   `json:"time_zone"`
	}
	if err := c.Decode(&a); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	if len(a.Calendars) == 0 {
		a.Calendars = []string{"primary"}
	}
	items := []map[string]string{}
	for _, id := range a.Calendars[:min(len(a.Calendars), 50)] {
		items = append(items, map[string]string{"id": id})
	}
	body := map[string]any{"timeMin": a.TimeMin, "timeMax": a.TimeMax, "items": items}
	if a.TimeZone != "" {
		body["timeZone"] = a.TimeZone
	}
	var out struct {
		Calendars map[string]struct {
			Busy []struct {
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"busy"`
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"calendars"`
	}
	if err := s.sendJSON(ctx, "POST", s.ep.Calendar+"/freeBusy", body, &out); err != nil {
		return toolErr(err), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Busy times %s → %s:\n", a.TimeMin, a.TimeMax)
	for _, id := range a.Calendars {
		cal, ok := out.Calendars[id]
		switch {
		case !ok:
			fmt.Fprintf(&b, "\n%s: no answer\n", id)
		case len(cal.Errors) > 0:
			fmt.Fprintf(&b, "\n%s: unavailable (%s)\n", id, cal.Errors[0].Reason)
		case len(cal.Busy) == 0:
			fmt.Fprintf(&b, "\n%s: free the whole time\n", id)
		default:
			fmt.Fprintf(&b, "\n%s:\n", id)
			for _, x := range cal.Busy {
				fmt.Fprintf(&b, "- busy %s → %s\n", x.Start, x.End)
			}
		}
	}
	return tools.Text(capText(b.String(), maxList, "narrow the time range")), nil
}

func eventTime(v, tz string) calTime {
	v = strings.TrimSpace(v)
	if len(v) == len("2006-01-02") {
		return calTime{Date: v}
	}
	return calTime{DateTime: v, TimeZone: tz}
}

func (s *Service) calCreate(ctx context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		Summary     string `json:"summary"`
		Start       string `json:"start"`
		End         string `json:"end"`
		TimeZone    string `json:"time_zone"`
		Description string `json:"description"`
		Location    string `json:"location"`
	}
	if err := c.Decode(&a); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	ev := map[string]any{"summary": a.Summary, "start": eventTime(a.Start, a.TimeZone), "end": eventTime(a.End, a.TimeZone)}
	if a.Description != "" {
		ev["description"] = a.Description
	}
	if a.Location != "" {
		ev["location"] = a.Location
	}
	guests := attendeesOf(c)
	if len(guests) > 0 {
		var at []map[string]string
		for _, g := range guests {
			at = append(at, map[string]string{"email": g})
		}
		ev["attendees"] = at
	}
	u := s.eventsURL(calendarID(c)) + "?sendUpdates=" + map[bool]string{true: "all", false: "none"}[len(guests) > 0]
	var out calEvent
	if err := s.sendJSON(ctx, "POST", u, ev, &out); err != nil {
		return toolErr(err), nil
	}
	return tools.Text(fmt.Sprintf("Created event id=%s %q %s → %s %s", out.ID, out.Summary, out.Start, out.End, out.HTMLLink)), nil
}

func (s *Service) calUpdate(ctx context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		ID          string  `json:"id"`
		Summary     *string `json:"summary"`
		Start       *string `json:"start"`
		End         *string `json:"end"`
		TimeZone    string  `json:"time_zone"`
		Description *string `json:"description"`
		Location    *string `json:"location"`
		Attendees   any     `json:"attendees"`
	}
	if err := c.Decode(&a); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	patch := map[string]any{}
	if a.Summary != nil {
		patch["summary"] = *a.Summary
	}
	if a.Start != nil {
		patch["start"] = eventTime(*a.Start, a.TimeZone)
	}
	if a.End != nil {
		patch["end"] = eventTime(*a.End, a.TimeZone)
	}
	if a.Description != nil {
		patch["description"] = *a.Description
	}
	if a.Location != nil {
		patch["location"] = *a.Location
	}
	if a.Attendees != nil {
		at := []map[string]string{}
		for _, g := range attendeesOf(c) {
			at = append(at, map[string]string{"email": g})
		}
		patch["attendees"] = at
	}
	if len(patch) == 0 {
		return tools.Errorf("nothing to change: give at least one field"), nil
	}
	var out calEvent
	if err := s.sendJSON(ctx, "PATCH", s.eventsURL(calendarID(c))+"/"+url.PathEscape(a.ID)+"?sendUpdates=all", patch, &out); err != nil {
		return toolErr(err), nil
	}
	return tools.Text(fmt.Sprintf("Updated event id=%s %q %s → %s", out.ID, out.Summary, out.Start, out.End)), nil
}

func (s *Service) calDelete(ctx context.Context, c tools.Call) (tools.Result, error) {
	id := c.String("id")
	if _, _, err := s.do(ctx, "DELETE", s.eventsURL(calendarID(c))+"/"+url.PathEscape(id)+"?sendUpdates=all", "", nil); err != nil {
		return toolErr(err), nil
	}
	return tools.Text("Deleted event " + id + "."), nil
}

func (s *Service) calRSVP(ctx context.Context, c tools.Call) (tools.Result, error) {
	id, resp := c.String("id"), c.String("response")
	if resp != "accepted" && resp != "declined" && resp != "tentative" {
		return tools.Errorf("response must be accepted, declined or tentative"), nil
	}
	u := s.eventsURL(calendarID(c)) + "/" + url.PathEscape(id)
	var ev calEvent
	if err := s.getJSON(ctx, u, &ev); err != nil {
		return toolErr(err), nil
	}
	found := false
	for i := range ev.Attendees {
		if ev.Attendees[i].Self {
			ev.Attendees[i].ResponseStatus, found = resp, true
		}
	}
	if !found {
		return tools.Errorf("you are not a guest of this event (nothing to answer)"), nil
	}
	if err := s.sendJSON(ctx, "PATCH", u+"?sendUpdates=all", map[string]any{"attendees": ev.Attendees}, nil); err != nil {
		return toolErr(err), nil
	}
	return tools.Text(fmt.Sprintf("Answered %q to %q.", resp, ev.Summary)), nil
}
