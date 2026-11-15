package google

import "github.com/hedeqiang/crewbase/core/tools"

// Product is one Google product the person connects separately.
//
// Scopes are the narrowest that make the tools useful (sensitivity per
// Google's API docs, 2026-11; "restricted" scopes need Google's security
// assessment before the app may leave testing mode with external users):
//
//	Gmail     gmail.readonly (restricted) — search and read mail
//	          gmail.compose  (restricted) — create drafts and send them
//	Calendar  calendar.events (sensitive) — list / create / change events
//	          calendar.freebusy (sensitive) — free/busy across calendars
//	Drive     drive.readonly (restricted) — search and read every file
//	          drive.file (non-sensitive) — upload, and share what Crewbase
//	          created or the person opened with it
//	Docs      documents (sensitive) — read and edit documents
//	Sheets    spreadsheets (sensitive) — read and write spreadsheets
//	Slides    presentations (sensitive) — read and create presentations
type Product struct {
	ID        string
	Name      string
	CatalogID string
	Scopes    []string
	tools     func(s *Service) []*tools.Tool
}

const scopePrefix = "https://www.googleapis.com/auth/"

// Products in display order.
var Products = []Product{
	{ID: "gmail", Name: "Gmail", CatalogID: "gmail",
		Scopes: []string{scopePrefix + "gmail.readonly", scopePrefix + "gmail.compose"}, tools: gmailTools},
	{ID: "calendar", Name: "Google Calendar", CatalogID: "google-calendar",
		Scopes: []string{scopePrefix + "calendar.events", scopePrefix + "calendar.freebusy"}, tools: calendarTools},
	{ID: "drive", Name: "Google Drive", CatalogID: "google-drive",
		Scopes: []string{scopePrefix + "drive.readonly", scopePrefix + "drive.file"}, tools: driveTools},
	{ID: "docs", Name: "Google Docs", CatalogID: "google-docs",
		Scopes: []string{scopePrefix + "documents"}, tools: docsTools},
	{ID: "sheets", Name: "Google Sheets", CatalogID: "google-sheets",
		Scopes: []string{scopePrefix + "spreadsheets"}, tools: sheetsTools},
	{ID: "slides", Name: "Google Slides", CatalogID: "google-slides",
		Scopes: []string{scopePrefix + "presentations"}, tools: slidesTools},
}

func productByID(id string) (Product, bool) {
	for _, p := range Products {
		if p.ID == id {
			return p, true
		}
	}
	return Product{}, false
}

// Output caps (runes) for what tools return to the model.
const (
	maxList = 12_000 // search / list results
	maxBody = 20_000 // one message, document, sheet range …
)

func arrayOf(typ, desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": typ}, "description": desc}
}
