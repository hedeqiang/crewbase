package google

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/hedeqiang/crewbase/core/tools"
)

// Google Docs, Sheets and Slides APIs.

// ---------------------------------------------------------------- Docs

type docsElement struct {
	Paragraph *struct {
		Elements []struct {
			TextRun *struct {
				Content string `json:"content"`
			} `json:"textRun"`
		} `json:"elements"`
	} `json:"paragraph"`
	Table *struct {
		TableRows []struct {
			TableCells []struct {
				Content []docsElement `json:"content"`
			} `json:"tableCells"`
		} `json:"tableRows"`
	} `json:"table"`
}

func docsText(els []docsElement, b *strings.Builder) {
	for _, e := range els {
		if e.Paragraph != nil {
			for _, x := range e.Paragraph.Elements {
				if x.TextRun != nil {
					b.WriteString(x.TextRun.Content)
				}
			}
		}
		if e.Table != nil {
			for _, r := range e.Table.TableRows {
				var cells []string
				for _, c := range r.TableCells {
					var cb strings.Builder
					docsText(c.Content, &cb)
					cells = append(cells, oneLine(cb.String()))
				}
				b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
			}
		}
	}
}

func docIDArg() map[string]any {
	return tools.Prop("string", "Document id (from drive_search, or the id in a docs.google.com/document/d/<id>/ URL).")
}

func docsTools(s *Service) []*tools.Tool {
	return []*tools.Tool{
		{
			Name:        "docs_read",
			Description: "Read a Google Doc as plain text (tables as | rows |; long documents truncated).",
			Params:      tools.Schema(map[string]any{"id": docIDArg()}, "id"),
			Category:    tools.CategoryRead,
			Classify:    func(c tools.Call) (string, string) { return tools.CategoryRead, "读取文档 " + c.String("id") },
			Execute:     s.docsRead,
		},
		{
			Name:        "docs_create",
			Description: "Create a Google Doc in the user's Drive (private) with a title and optional plain-text content. Returns its id and link.",
			Params: tools.Schema(map[string]any{
				"title": tools.Prop("string", "Document title."),
				"text":  tools.Prop("string", "Initial plain-text content (optional)."),
			}, "title"),
			Category: tools.CategoryWrite,
			Classify: func(c tools.Call) (string, string) {
				return tools.CategoryWrite, "新建文档「" + clip(c.String("title"), 60) + "」"
			},
			Execute: s.docsCreate,
		},
		{
			Name:        "docs_append",
			Description: "Append plain text to the end of a Google Doc.",
			Params:      tools.Schema(map[string]any{"id": docIDArg(), "text": tools.Prop("string", "Text to append (use \\n for new paragraphs).")}, "id", "text"),
			Category:    tools.CategoryWrite,
			Classify: func(c tools.Call) (string, string) {
				return tools.CategoryWrite, "在文档 " + c.String("id") + " 末尾追加文字"
			},
			Execute: s.docsAppend,
		},
		{
			Name:        "docs_replace",
			Description: "Replace every occurrence of a text in a Google Doc. Returns how many were replaced.",
			Params: tools.Schema(map[string]any{
				"id":         docIDArg(),
				"find":       tools.Prop("string", "Exact text to find."),
				"replace":    tools.Prop("string", "Replacement text (empty deletes it)."),
				"match_case": tools.Prop("boolean", "Case-sensitive (default true)."),
			}, "id", "find", "replace"),
			Category: tools.CategoryWrite,
			Classify: func(c tools.Call) (string, string) {
				return tools.CategoryWrite, "替换文档 " + c.String("id") + " 中的「" + clip(c.String("find"), 40) + "」"
			},
			Execute: s.docsReplace,
		},
	}
}

func (s *Service) docURL(id string) string {
	return s.ep.Docs + "/" + url.PathEscape(strings.TrimSpace(id))
}

func (s *Service) docsRead(ctx context.Context, c tools.Call) (tools.Result, error) {
	var d struct {
		DocumentID string `json:"documentId"`
		Title      string `json:"title"`
		Body       struct {
			Content []docsElement `json:"content"`
		} `json:"body"`
	}
	if err := s.getJSON(ctx, s.docURL(c.String("id")), &d); err != nil {
		return toolErr(err), nil
	}
	var b strings.Builder
	docsText(d.Body.Content, &b)
	return tools.Text(fmt.Sprintf("# %s (id=%s)\n\n%s", d.Title, d.DocumentID, capText(strings.TrimSpace(b.String()), maxBody, "the rest of the document is not shown"))), nil
}

func (s *Service) docsCreate(ctx context.Context, c tools.Call) (tools.Result, error) {
	var d struct {
		DocumentID string `json:"documentId"`
	}
	if err := s.sendJSON(ctx, "POST", s.ep.Docs, map[string]string{"title": c.String("title")}, &d); err != nil {
		return toolErr(err), nil
	}
	if t := c.String("text"); t != "" {
		req := []map[string]any{{"insertText": map[string]any{"location": map[string]int{"index": 1}, "text": t}}}
		if err := s.sendJSON(ctx, "POST", s.docURL(d.DocumentID)+":batchUpdate", map[string]any{"requests": req}, nil); err != nil {
			r := toolErr(err)
			r.Content = "Created document " + d.DocumentID + " but could not add its text: " + r.Content
			return r, nil
		}
	}
	return tools.Text(fmt.Sprintf("Created document id=%s https://docs.google.com/document/d/%s/edit", d.DocumentID, d.DocumentID)), nil
}

func (s *Service) docsAppend(ctx context.Context, c tools.Call) (tools.Result, error) {
	req := []map[string]any{{"insertText": map[string]any{"endOfSegmentLocation": map[string]any{}, "text": c.String("text")}}}
	if err := s.sendJSON(ctx, "POST", s.docURL(c.String("id"))+":batchUpdate", map[string]any{"requests": req}, nil); err != nil {
		return toolErr(err), nil
	}
	return tools.Text("Appended to document " + c.String("id") + "."), nil
}

func (s *Service) docsReplace(ctx context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		Find      string `json:"find"`
		Replace   string `json:"replace"`
		MatchCase *bool  `json:"match_case"`
	}
	_ = c.Decode(&a)
	if a.Find == "" {
		return tools.Errorf("find is empty"), nil
	}
	mc := a.MatchCase == nil || *a.MatchCase
	req := []map[string]any{{"replaceAllText": map[string]any{
		"containsText": map[string]any{"text": a.Find, "matchCase": mc}, "replaceText": a.Replace}}}
	var out struct {
		Replies []struct {
			ReplaceAllText struct {
				OccurrencesChanged int `json:"occurrencesChanged"`
			} `json:"replaceAllText"`
		} `json:"replies"`
	}
	if err := s.sendJSON(ctx, "POST", s.docURL(c.String("id"))+":batchUpdate", map[string]any{"requests": req}, &out); err != nil {
		return toolErr(err), nil
	}
	n := 0
	if len(out.Replies) > 0 {
		n = out.Replies[0].ReplaceAllText.OccurrencesChanged
	}
	return tools.Text(fmt.Sprintf("Replaced %d occurrence(s).", n)), nil
}

// ---------------------------------------------------------------- Sheets

func sheetIDArg() map[string]any {
	return tools.Prop("string", "Spreadsheet id (from drive_search, or the id in a docs.google.com/spreadsheets/d/<id>/ URL).")
}

func sheetsTools(s *Service) []*tools.Tool {
	values := map[string]any{"type": "array", "items": map[string]any{"type": "array", "items": map[string]any{}},
		"description": "Rows of cell values, e.g. [[\"Name\",\"Total\"],[\"Ann\",42]]. Formulas start with =."}
	return []*tools.Tool{
		{
			Name: "sheets_read",
			Description: "Read a Google Sheet. Without range: the sheet names and sizes. With range (A1 notation, e.g. \"Sheet1!A1:D20\" or \"Sheet1\"): " +
				"the values as tab-separated rows (large ranges truncated).",
			Params:   tools.Schema(map[string]any{"id": sheetIDArg(), "range": tools.Prop("string", "A1 range (optional).")}, "id"),
			Category: tools.CategoryRead,
			Classify: func(c tools.Call) (string, string) {
				return tools.CategoryRead, "读取表格 " + c.String("id") + " " + c.String("range")
			},
			Execute: s.sheetsRead,
		},
		{
			Name:        "sheets_write",
			Description: "Write values into a range of a Google Sheet, overwriting what is there (values are parsed as if typed: numbers, dates, formulas).",
			Params:      tools.Schema(map[string]any{"id": sheetIDArg(), "range": tools.Prop("string", "A1 range where the top-left cell goes, e.g. \"Sheet1!A1\"."), "values": values}, "id", "range", "values"),
			Category:    tools.CategoryWrite,
			Classify: func(c tools.Call) (string, string) {
				return tools.CategoryWrite, "写入表格 " + c.String("id") + " 的 " + c.String("range")
			},
			Execute: func(ctx context.Context, c tools.Call) (tools.Result, error) { return s.sheetsPut(ctx, c, false) },
		},
		{
			Name:        "sheets_append",
			Description: "Append rows after the last row of a table in a Google Sheet.",
			Params:      tools.Schema(map[string]any{"id": sheetIDArg(), "range": tools.Prop("string", "The table, e.g. \"Sheet1\" or \"Sheet1!A:D\"."), "values": values}, "id", "range", "values"),
			Category:    tools.CategoryWrite,
			Classify: func(c tools.Call) (string, string) {
				return tools.CategoryWrite, "向表格 " + c.String("id") + " 追加行"
			},
			Execute: func(ctx context.Context, c tools.Call) (tools.Result, error) { return s.sheetsPut(ctx, c, true) },
		},
	}
}

func (s *Service) sheetURL(id string) string {
	return s.ep.Sheets + "/" + url.PathEscape(strings.TrimSpace(id))
}

func (s *Service) sheetsRead(ctx context.Context, c tools.Call) (tools.Result, error) {
	id, rng := c.String("id"), strings.TrimSpace(c.String("range"))
	if rng == "" {
		var out struct {
			Properties struct {
				Title string `json:"title"`
			} `json:"properties"`
			Sheets []struct {
				Properties struct {
					Title          string `json:"title"`
					GridProperties struct {
						RowCount    int `json:"rowCount"`
						ColumnCount int `json:"columnCount"`
					} `json:"gridProperties"`
				} `json:"properties"`
			} `json:"sheets"`
		}
		if err := s.getJSON(ctx, s.sheetURL(id)+"?fields="+url.QueryEscape("properties.title,sheets.properties"), &out); err != nil {
			return toolErr(err), nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s (id=%s) — sheets:\n", out.Properties.Title, id)
		for _, sh := range out.Sheets {
			fmt.Fprintf(&b, "- %s (%d rows × %d columns)\n", sh.Properties.Title, sh.Properties.GridProperties.RowCount, sh.Properties.GridProperties.ColumnCount)
		}
		first := "Sheet1"
		if len(out.Sheets) > 0 {
			first = out.Sheets[0].Properties.Title
		}
		b.WriteString("Pass range (e.g. \"" + first + "!A1:Z50\") to read values.")
		return tools.Text(capText(b.String(), maxList, "")), nil
	}
	var out struct {
		Range  string  `json:"range"`
		Values [][]any `json:"values"`
	}
	if err := s.getJSON(ctx, s.sheetURL(id)+"/values/"+url.PathEscape(rng), &out); err != nil {
		return toolErr(err), nil
	}
	if len(out.Values) == 0 {
		return tools.Text(out.Range + " is empty."), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d rows, tab-separated):\n", out.Range, len(out.Values))
	for _, row := range out.Values {
		cells := make([]string, len(row))
		for i, v := range row {
			cells[i] = strings.ReplaceAll(fmt.Sprint(v), "\t", " ")
			cells[i] = strings.ReplaceAll(cells[i], "\n", " ")
		}
		b.WriteString(strings.Join(cells, "\t") + "\n")
	}
	return tools.Text(capText(b.String(), maxBody, "read a smaller range")), nil
}

func (s *Service) sheetsPut(ctx context.Context, c tools.Call, appendRows bool) (tools.Result, error) {
	var a struct {
		ID     string  `json:"id"`
		Range  string  `json:"range"`
		Values [][]any `json:"values"`
	}
	if err := c.Decode(&a); err != nil {
		return tools.Errorf("values must be an array of rows (arrays): %v", err), nil
	}
	if len(a.Values) == 0 {
		return tools.Errorf("values is empty"), nil
	}
	body := map[string]any{"range": a.Range, "majorDimension": "ROWS", "values": a.Values}
	u := s.sheetURL(a.ID) + "/values/" + url.PathEscape(a.Range)
	var out struct {
		UpdatedRange string `json:"updatedRange"`
		UpdatedCells int    `json:"updatedCells"`
		Updates      struct {
			UpdatedRange string `json:"updatedRange"`
			UpdatedCells int    `json:"updatedCells"`
		} `json:"updates"`
	}
	var err error
	if appendRows {
		err = s.sendJSON(ctx, "POST", u+":append?valueInputOption=USER_ENTERED&insertDataOption=INSERT_ROWS", body, &out)
		out.UpdatedRange, out.UpdatedCells = out.Updates.UpdatedRange, out.Updates.UpdatedCells
	} else {
		err = s.sendJSON(ctx, "PUT", u+"?valueInputOption=USER_ENTERED", body, &out)
	}
	if err != nil {
		return toolErr(err), nil
	}
	return tools.Text(fmt.Sprintf("Wrote %d cells in %s.", out.UpdatedCells, out.UpdatedRange)), nil
}

// ---------------------------------------------------------------- Slides

type slidesShape struct {
	Shape *struct {
		Text *struct {
			TextElements []struct {
				TextRun *struct {
					Content string `json:"content"`
				} `json:"textRun"`
			} `json:"textElements"`
		} `json:"text"`
	} `json:"shape"`
}

func slidesTools(s *Service) []*tools.Tool {
	return []*tools.Tool{
		{
			Name:        "slides_read",
			Description: "Read the text of a Google Slides presentation, slide by slide (including speaker notes).",
			Params: tools.Schema(map[string]any{
				"id": tools.Prop("string", "Presentation id (from drive_search, or the id in a docs.google.com/presentation/d/<id>/ URL)."),
			}, "id"),
			Category: tools.CategoryRead,
			Classify: func(c tools.Call) (string, string) { return tools.CategoryRead, "读取演示文稿 " + c.String("id") },
			Execute:  s.slidesRead,
		},
		{
			Name:        "slides_create",
			Description: "Create a Google Slides presentation from an outline: one title-and-body slide per item. Private to the user; returns its id and link.",
			Params: tools.Schema(map[string]any{
				"title": tools.Prop("string", "Presentation title."),
				"slides": map[string]any{"type": "array", "description": "Slides in order (at most 50).", "items": map[string]any{
					"type": "object", "properties": map[string]any{
						"title": tools.Prop("string", "Slide title."),
						"body":  tools.Prop("string", "Slide body; one bullet per line."),
					}}},
			}, "title", "slides"),
			Category: tools.CategoryWrite,
			Classify: func(c tools.Call) (string, string) {
				return tools.CategoryWrite, "新建演示文稿「" + clip(c.String("title"), 60) + "」"
			},
			Execute: s.slidesCreate,
		},
	}
}

func shapeText(els []slidesShape) []string {
	var out []string
	for _, e := range els {
		if e.Shape == nil || e.Shape.Text == nil {
			continue
		}
		var b strings.Builder
		for _, t := range e.Shape.Text.TextElements {
			if t.TextRun != nil {
				b.WriteString(t.TextRun.Content)
			}
		}
		if x := strings.TrimSpace(b.String()); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func (s *Service) slidesRead(ctx context.Context, c tools.Call) (tools.Result, error) {
	var p struct {
		PresentationID string `json:"presentationId"`
		Title          string `json:"title"`
		Slides         []struct {
			PageElements    []slidesShape `json:"pageElements"`
			SlideProperties struct {
				NotesPage struct {
					PageElements []slidesShape `json:"pageElements"`
				} `json:"notesPage"`
			} `json:"slideProperties"`
		} `json:"slides"`
	}
	if err := s.getJSON(ctx, s.ep.Slides+"/"+url.PathEscape(strings.TrimSpace(c.String("id"))), &p); err != nil {
		return toolErr(err), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s (id=%s, %d slides)\n", p.Title, p.PresentationID, len(p.Slides))
	for i, sl := range p.Slides {
		fmt.Fprintf(&b, "\n## Slide %d\n%s\n", i+1, strings.Join(shapeText(sl.PageElements), "\n"))
		if notes := shapeText(sl.SlideProperties.NotesPage.PageElements); len(notes) > 0 {
			fmt.Fprintf(&b, "Notes: %s\n", strings.Join(notes, " "))
		}
	}
	return tools.Text(capText(b.String(), maxBody, "the remaining slides are not shown")), nil
}

func (s *Service) slidesCreate(ctx context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		Title  string `json:"title"`
		Slides []struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		} `json:"slides"`
	}
	if err := c.Decode(&a); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	if len(a.Slides) == 0 || len(a.Slides) > 50 {
		return tools.Errorf("give 1–50 slides"), nil
	}
	var p struct {
		PresentationID string `json:"presentationId"`
		Slides         []struct {
			ObjectID string `json:"objectId"`
		} `json:"slides"`
	}
	if err := s.sendJSON(ctx, "POST", s.ep.Slides, map[string]string{"title": a.Title}, &p); err != nil {
		return toolErr(err), nil
	}
	var reqs []map[string]any
	for i, sl := range a.Slides {
		sid, tid, bid := fmt.Sprintf("cb_s%d", i), fmt.Sprintf("cb_t%d", i), fmt.Sprintf("cb_b%d", i)
		reqs = append(reqs, map[string]any{"createSlide": map[string]any{
			"objectId": sid, "insertionIndex": i,
			"slideLayoutReference": map[string]string{"predefinedLayout": "TITLE_AND_BODY"},
			"placeholderIdMappings": []map[string]any{
				{"layoutPlaceholder": map[string]any{"type": "TITLE", "index": 0}, "objectId": tid},
				{"layoutPlaceholder": map[string]any{"type": "BODY", "index": 0}, "objectId": bid},
			},
		}})
		if sl.Title != "" {
			reqs = append(reqs, map[string]any{"insertText": map[string]any{"objectId": tid, "text": sl.Title}})
		}
		if sl.Body != "" {
			reqs = append(reqs, map[string]any{"insertText": map[string]any{"objectId": bid, "text": sl.Body}})
		}
	}
	// the blank title slide a new presentation starts with
	for _, sl := range p.Slides {
		reqs = append(reqs, map[string]any{"deleteObject": map[string]string{"objectId": sl.ObjectID}})
	}
	link := "https://docs.google.com/presentation/d/" + p.PresentationID + "/edit"
	if err := s.sendJSON(ctx, "POST", s.ep.Slides+"/"+url.PathEscape(p.PresentationID)+":batchUpdate", map[string]any{"requests": reqs}, nil); err != nil {
		r := toolErr(err)
		r.Content = "Created presentation " + p.PresentationID + " (" + link + ") but could not add the slides: " + r.Content
		return r, nil
	}
	return tools.Text(fmt.Sprintf("Created presentation id=%s with %d slides: %s", p.PresentationID, len(a.Slides), link)), nil
}
