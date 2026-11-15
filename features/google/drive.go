package google

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/toolsbasic"
)

// Google Drive API v3 (developers.google.com/workspace/drive/api/reference/rest/v3).

const (
	mimeDoc    = "application/vnd.google-apps.document"
	mimeSheet  = "application/vnd.google-apps.spreadsheet"
	mimeSlides = "application/vnd.google-apps.presentation"
	mimeFolder = "application/vnd.google-apps.folder"
)

// maxUpload caps drive_upload (simple multipart upload).
const maxUpload = 25 << 20

type driveFile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MimeType     string `json:"mimeType"`
	ModifiedTime string `json:"modifiedTime"`
	Size         string `json:"size"`
	WebViewLink  string `json:"webViewLink"`
	Owners       []struct {
		EmailAddress string `json:"emailAddress"`
	} `json:"owners"`
}

const driveFields = "id,name,mimeType,modifiedTime,size,webViewLink,owners(emailAddress)"

func driveTools(s *Service) []*tools.Tool {
	return []*tools.Tool{
		{
			Name: "drive_search",
			Description: "Search Google Drive. q is either plain words (matched against names and contents) or Drive query syntax " +
				"(e.g. \"name contains 'budget' and mimeType = 'application/vnd.google-apps.spreadsheet'\"). Returns id, name, type, modified time and link.",
			Params: tools.Schema(map[string]any{
				"q":   tools.Prop("string", "Words or a Drive query (empty = recently modified files)."),
				"max": tools.Prop("integer", "Number of files, 1–50 (default 20)."),
			}),
			Category: tools.CategoryRead,
			Classify: func(c tools.Call) (string, string) {
				return tools.CategoryRead, "搜索云端硬盘：" + clip(c.String("q"), 80)
			},
			Execute: s.driveSearch,
		},
		{
			Name: "drive_read",
			Description: "Read a Drive file as text: Google Docs and Slides as plain text, Google Sheets as CSV (first sheet), text files as-is (long content truncated). " +
				"Other binary files return metadata only.",
			Params: tools.Schema(map[string]any{
				"id": tools.Prop("string", "File id from drive_search."),
			}, "id"),
			Category: tools.CategoryRead,
			Classify: func(c tools.Call) (string, string) {
				return tools.CategoryRead, "读取云端硬盘文件 " + c.String("id")
			},
			Execute: s.driveRead,
		},
		{
			Name:        "drive_upload",
			Description: fmt.Sprintf("Upload a file from the agent's workspace to the user's own Google Drive (private; up to %d MB). Returns the new file's id and link.", maxUpload>>20),
			Params: tools.Schema(map[string]any{
				"path":      tools.Prop("string", "File path in the workspace."),
				"name":      tools.Prop("string", "Name in Drive (default: the file's name)."),
				"folder_id": tools.Prop("string", "Parent folder id (optional; default My Drive)."),
				"convert":   tools.Prop("boolean", "Convert to a Google Doc / Sheet / Slides when possible (e.g. .docx, .csv, .pptx)."),
			}, "path"),
			PathArgs: []string{"path"},
			Category: tools.CategoryWrite,
			Classify: func(c tools.Call) (string, string) {
				return tools.CategoryWrite, "上传到云端硬盘：" + c.String("path")
			},
			Execute: s.driveUpload,
		},
		{
			Name: "drive_share",
			Description: "Share a Drive file with people (by email) or with anyone who has the link. External: the user is asked to approve. " +
				"Works on files Crewbase created or uploaded; other files must be shared by the user in Drive.",
			Params: tools.Schema(map[string]any{
				"id":     tools.Prop("string", "File id."),
				"emails": arrayOf("string", "People to share with (omit with anyone=true)."),
				"role":   map[string]any{"type": "string", "enum": []string{"reader", "commenter", "writer"}, "description": "Access level (default reader)."},
				"anyone": tools.Prop("boolean", "Anyone with the link can open it."),
				"notify": tools.Prop("boolean", "Email the people a notification (default true)."),
			}, "id"),
			Category: tools.CategoryPublish,
			Classify: func(c tools.Call) (string, string) {
				var a struct {
					Emails []string `json:"emails"`
					Anyone bool     `json:"anyone"`
					Role   string   `json:"role"`
				}
				_ = c.Decode(&a)
				who := strings.Join(a.Emails, ", ")
				if a.Anyone {
					who = "任何拿到链接的人"
				}
				if a.Role == "" {
					a.Role = "reader"
				}
				return tools.CategoryPublish, "共享云端硬盘文件 " + c.String("id") + " 给 " + clip(who, 160) + "（" + a.Role + "）"
			},
			Execute: s.driveShare,
		},
	}
}

func looksLikeQuery(q string) bool {
	for _, k := range []string{" contains ", "mimeType", "modifiedTime", " in parents", "trashed", "name =", "fullText", "starred", "sharedWithMe"} {
		if strings.Contains(q, k) {
			return true
		}
	}
	return false
}

func (s *Service) driveSearch(ctx context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		Q   string `json:"q"`
		Max int    `json:"max"`
	}
	if err := c.Decode(&a); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	if a.Max <= 0 {
		a.Max = 20
	}
	q := strings.TrimSpace(a.Q)
	switch {
	case q == "":
		q = "trashed = false"
	case !looksLikeQuery(q):
		q = "fullText contains '" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(q) + "' and trashed = false"
	}
	v := url.Values{"q": {q}, "pageSize": {fmt.Sprint(min(a.Max, 50))}, "fields": {"files(" + driveFields + ")"},
		"supportsAllDrives": {"true"}, "includeItemsFromAllDrives": {"true"}}
	if strings.TrimSpace(a.Q) == "" {
		v.Set("orderBy", "modifiedTime desc")
	}
	var out struct {
		Files []driveFile `json:"files"`
	}
	if err := s.getJSON(ctx, s.ep.Drive+"/files?"+v.Encode(), &out); err != nil {
		return toolErr(err), nil
	}
	if len(out.Files) == 0 {
		return tools.Text(fmt.Sprintf("No files match %q.", a.Q)), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d files:\n", len(out.Files))
	for _, f := range out.Files {
		fmt.Fprintf(&b, "\n- id=%s\n  %s (%s)\n  modified %s", f.ID, oneLine(f.Name), shortMime(f.MimeType), f.ModifiedTime)
		if f.Size != "" {
			var n int
			fmt.Sscan(f.Size, &n)
			fmt.Fprintf(&b, " · %s", humanSize(n))
		}
		if f.WebViewLink != "" {
			fmt.Fprintf(&b, "\n  %s", f.WebViewLink)
		}
		b.WriteString("\n")
	}
	return tools.Text(capText(b.String(), maxList, "narrow the query")), nil
}

func shortMime(m string) string {
	switch m {
	case mimeDoc:
		return "Google Doc"
	case mimeSheet:
		return "Google Sheet"
	case mimeSlides:
		return "Google Slides"
	case mimeFolder:
		return "folder"
	}
	return m
}

func isText(m string) bool {
	return strings.HasPrefix(m, "text/") || m == "application/json" || m == "application/xml" ||
		strings.HasSuffix(m, "+json") || strings.HasSuffix(m, "+xml") || m == "application/javascript" || m == "application/x-yaml"
}

func (s *Service) driveRead(ctx context.Context, c tools.Call) (tools.Result, error) {
	id := strings.TrimSpace(c.String("id"))
	fu := s.ep.Drive + "/files/" + url.PathEscape(id)
	var f driveFile
	if err := s.getJSON(ctx, fu+"?supportsAllDrives=true&fields="+url.QueryEscape(driveFields), &f); err != nil {
		return toolErr(err), nil
	}
	head := fmt.Sprintf("%s (%s, id=%s, modified %s)\n%s\n\n", f.Name, shortMime(f.MimeType), f.ID, f.ModifiedTime, f.WebViewLink)
	var export string
	switch f.MimeType {
	case mimeDoc, mimeSlides:
		export = "text/plain"
	case mimeSheet:
		export = "text/csv"
	}
	var data []byte
	var err error
	switch {
	case export != "":
		data, _, err = s.do(ctx, "GET", fu+"/export?mimeType="+url.QueryEscape(export), "", nil)
	case isText(f.MimeType):
		data, _, err = s.do(ctx, "GET", fu+"?alt=media&supportsAllDrives=true", "", nil)
	default:
		size := ""
		if f.Size != "" {
			var n int
			fmt.Sscan(f.Size, &n)
			size = ", " + humanSize(n)
		}
		return tools.Text(head + fmt.Sprintf("This is a binary file (%s%s); its content cannot be shown as text.", f.MimeType, size)), nil
	}
	if err != nil {
		return toolErr(err), nil
	}
	if !utf8.Valid(data) {
		return tools.Text(head + "The file is not valid UTF-8 text; content not shown."), nil
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	if f.MimeType == mimeSheet {
		head += "(first sheet as CSV; use sheets_read for other sheets or ranges)\n"
	}
	return tools.Text(head + capText(text, maxBody, "the rest of the file is not shown")), nil
}

func (s *Service) driveUpload(ctx context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		Path     string `json:"path"`
		Name     string `json:"name"`
		FolderID string `json:"folder_id"`
		Convert  bool   `json:"convert"`
	}
	if err := c.Decode(&a); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	p, err := toolsbasic.ResolvePath(c, a.Path)
	if err != nil {
		return tools.Errorf("%v", err), nil
	}
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return tools.Errorf("%s is not a file", a.Path), nil
	}
	if st.Size() > maxUpload {
		return tools.Errorf("the file is larger than %d MB", maxUpload>>20), nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return tools.Errorf("%v", err), nil
	}
	name := a.Name
	if name == "" {
		name = filepath.Base(p)
	}
	ctype := mime.TypeByExtension(strings.ToLower(filepath.Ext(p)))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	meta := map[string]any{"name": name}
	if a.FolderID != "" {
		meta["parents"] = []string{a.FolderID}
	}
	if a.Convert {
		switch strings.ToLower(filepath.Ext(p)) {
		case ".doc", ".docx", ".odt", ".rtf", ".txt", ".md", ".html":
			meta["mimeType"] = mimeDoc
		case ".xls", ".xlsx", ".ods", ".csv", ".tsv":
			meta["mimeType"] = mimeSheet
		case ".ppt", ".pptx", ".odp":
			meta["mimeType"] = mimeSlides
		}
	}
	mb, _ := json.Marshal(meta)
	rb := make([]byte, 12)
	_, _ = rand.Read(rb)
	boundary := "crewbase" + hex.EncodeToString(rb)
	var body bytes.Buffer
	fmt.Fprintf(&body, "--%s\r\nContent-Type: application/json; charset=UTF-8\r\n\r\n%s\r\n--%s\r\nContent-Type: %s\r\n\r\n", boundary, mb, boundary, ctype)
	body.Write(data)
	fmt.Fprintf(&body, "\r\n--%s--\r\n", boundary)
	u := s.ep.Upload + "/files?uploadType=multipart&supportsAllDrives=true&fields=" + url.QueryEscape(driveFields)
	out, _, err := s.do(ctx, "POST", u, "multipart/related; boundary="+boundary, body.Bytes())
	if err != nil {
		return toolErr(err), nil
	}
	var f driveFile
	_ = json.Unmarshal(out, &f)
	return tools.Text(fmt.Sprintf("Uploaded %s as %q (%s, id=%s). Only the user can see it. %s", a.Path, f.Name, shortMime(f.MimeType), f.ID, f.WebViewLink)), nil
}

func (s *Service) driveShare(ctx context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		ID     string   `json:"id"`
		Emails []string `json:"emails"`
		Role   string   `json:"role"`
		Anyone bool     `json:"anyone"`
		Notify *bool    `json:"notify"`
	}
	if err := c.Decode(&a); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	if a.Role == "" {
		a.Role = "reader"
	}
	if a.Role != "reader" && a.Role != "commenter" && a.Role != "writer" {
		return tools.Errorf("role must be reader, commenter or writer"), nil
	}
	if !a.Anyone && len(a.Emails) == 0 {
		return tools.Errorf("give emails or anyone=true"), nil
	}
	notify := a.Notify == nil || *a.Notify
	base := s.ep.Drive + "/files/" + url.PathEscape(a.ID) + "/permissions?supportsAllDrives=true"
	var done []string
	if a.Anyone {
		if err := s.sendJSON(ctx, "POST", base, map[string]string{"type": "anyone", "role": a.Role}, nil); err != nil {
			return toolErr(err), nil
		}
		done = append(done, "anyone with the link")
	}
	for _, e := range a.Emails {
		u := base + "&sendNotificationEmail=" + fmt.Sprint(notify)
		if err := s.sendJSON(ctx, "POST", u, map[string]string{"type": "user", "role": a.Role, "emailAddress": strings.TrimSpace(e)}, nil); err != nil {
			r := toolErr(err)
			if len(done) > 0 {
				r.Content = "Shared with " + strings.Join(done, ", ") + "; then failed for " + e + ": " + r.Content
			}
			return r, nil
		}
		done = append(done, e)
	}
	return tools.Text(fmt.Sprintf("Shared %s as %s with %s.", a.ID, a.Role, strings.Join(done, ", "))), nil
}
