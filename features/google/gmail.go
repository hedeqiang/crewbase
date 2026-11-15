package google

import (
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"mime"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/tools"
)

// Gmail REST API (developers.google.com/workspace/gmail/api/reference/rest).

type gmailHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type gmailPart struct {
	MimeType string        `json:"mimeType"`
	Filename string        `json:"filename"`
	Headers  []gmailHeader `json:"headers"`
	Body     struct {
		Size         int    `json:"size"`
		Data         string `json:"data"`
		AttachmentID string `json:"attachmentId"`
	} `json:"body"`
	Parts []gmailPart `json:"parts"`
}

type gmailMessage struct {
	ID       string    `json:"id"`
	ThreadID string    `json:"threadId"`
	LabelIDs []string  `json:"labelIds"`
	Snippet  string    `json:"snippet"`
	Payload  gmailPart `json:"payload"`
}

func (m gmailMessage) header(name string) string {
	for _, h := range m.Payload.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

func gmailTools(s *Service) []*tools.Tool {
	sendParams := map[string]any{
		"to":          tools.Prop("string", "Recipients, comma-separated (e.g. \"Ann <ann@example.com>, bob@example.com\")."),
		"cc":          tools.Prop("string", "Cc recipients, comma-separated (optional)."),
		"bcc":         tools.Prop("string", "Bcc recipients, comma-separated (optional)."),
		"subject":     tools.Prop("string", "Subject line. For replies it defaults to \"Re: <original subject>\"."),
		"body":        tools.Prop("string", "Plain-text body."),
		"reply_to_id": tools.Prop("string", "Message id being replied to (from gmail_search); threads the reply."),
	}
	draftParams := map[string]any{}
	for k, v := range sendParams {
		draftParams[k] = v
	}
	sendParams["draft_id"] = tools.Prop("string", "Send this existing draft instead (from gmail_draft); other fields are then ignored.")
	return []*tools.Tool{
		{
			Name: "gmail_search",
			Description: "Search the connected Gmail account with Gmail search syntax (e.g. \"from:ann is:unread newer_than:7d\", \"subject:invoice has:attachment\"). " +
				"Returns id, from, subject, date and snippet per message; read one in full with gmail_read.",
			Params: tools.Schema(map[string]any{
				"q":   tools.Prop("string", "Gmail search query (empty = latest messages in the inbox)."),
				"max": tools.Prop("integer", "Number of messages, 1–25 (default 10)."),
			}),
			Category: tools.CategoryRead,
			Classify: func(c tools.Call) (string, string) {
				return tools.CategoryRead, "搜索邮件：" + clip(c.String("q"), 80)
			},
			Execute: s.gmailSearch,
		},
		{
			Name:        "gmail_read",
			Description: "Read one Gmail message: headers, the text body (HTML converted to text, long bodies truncated) and attachment names and sizes.",
			Params: tools.Schema(map[string]any{
				"id": tools.Prop("string", "Message id from gmail_search."),
			}, "id"),
			Category: tools.CategoryRead,
			Classify: func(c tools.Call) (string, string) { return tools.CategoryRead, "读取邮件 " + c.String("id") },
			Execute:  s.gmailRead,
		},
		{
			Name:        "gmail_draft",
			Description: "Create a Gmail draft (plain text) without sending it. Use reply_to_id to draft a reply in the same thread. Returns the draft id for gmail_send.",
			Params:      tools.Schema(draftParams, "body"),
			Category:    tools.CategoryWrite,
			Classify: func(c tools.Call) (string, string) {
				return tools.CategoryWrite, "起草邮件" + recipients(c) + "：主题「" + clip(c.String("subject"), 60) + "」"
			},
			Execute: func(ctx context.Context, c tools.Call) (tools.Result, error) { return s.gmailCompose(ctx, c, false) },
		},
		{
			Name: "gmail_send",
			Description: "Send an email from the connected Gmail account (plain text), or send an existing draft with draft_id. " +
				"Sending is external: the user is asked to approve it. Prefer gmail_draft when the user has not asked to send.",
			Params:   tools.Schema(sendParams),
			Category: tools.CategoryPublish,
			Classify: func(c tools.Call) (string, string) {
				if id := c.String("draft_id"); id != "" {
					return tools.CategoryPublish, s.draftSummary(id)
				}
				return tools.CategoryPublish, "发送邮件" + recipients(c) + "：主题「" + clip(c.String("subject"), 60) + "」"
			},
			Execute: func(ctx context.Context, c tools.Call) (tools.Result, error) { return s.gmailCompose(ctx, c, true) },
		},
	}
}

func recipients(c tools.Call) string {
	var all []string
	for _, k := range []string{"to", "cc", "bcc"} {
		if v := strings.TrimSpace(c.String(k)); v != "" {
			all = append(all, v)
		}
	}
	if len(all) == 0 {
		if c.String("reply_to_id") != "" {
			return "（回复原发件人）"
		}
		return ""
	}
	return "给 " + clip(strings.Join(all, ", "), 160)
}

// draftSummary describes a draft for the approval card (best effort).
func (s *Service) draftSummary(id string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var d struct {
		Message gmailMessage `json:"message"`
	}
	if err := s.getJSON(ctx, s.ep.Gmail+"/drafts/"+url.PathEscape(id)+"?format=metadata", &d); err != nil {
		return "发送草稿 " + id
	}
	to := d.Message.header("To")
	if cc := d.Message.header("Cc"); cc != "" {
		to += ", " + cc
	}
	return "发送邮件给 " + clip(to, 160) + "：主题「" + clip(decodeHeader(d.Message.header("Subject")), 60) + "」"
}

func decodeHeader(v string) string {
	if d, err := new(mime.WordDecoder).DecodeHeader(v); err == nil {
		return d
	}
	return v
}

func (s *Service) gmailSearch(ctx context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		Q   string `json:"q"`
		Max int    `json:"max"`
	}
	if err := c.Decode(&a); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	if a.Max <= 0 {
		a.Max = 10
	}
	a.Max = min(a.Max, 25)
	q := url.Values{"maxResults": {fmt.Sprint(a.Max)}}
	if strings.TrimSpace(a.Q) != "" {
		q.Set("q", a.Q)
	} else {
		q.Set("labelIds", "INBOX")
	}
	var list struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
		ResultSizeEstimate int `json:"resultSizeEstimate"`
	}
	if err := s.getJSON(ctx, s.ep.Gmail+"/messages?"+q.Encode(), &list); err != nil {
		return toolErr(err), nil
	}
	if len(list.Messages) == 0 {
		return tools.Text(fmt.Sprintf("No messages match %q.", a.Q)), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d messages", len(list.Messages))
	if a.Q != "" {
		fmt.Fprintf(&b, " for %q", a.Q)
	}
	b.WriteString(":\n")
	meta := "?format=metadata&metadataHeaders=From&metadataHeaders=Subject&metadataHeaders=Date"
	for i, m := range list.Messages {
		var msg gmailMessage
		if err := s.getJSON(ctx, s.ep.Gmail+"/messages/"+url.PathEscape(m.ID)+meta, &msg); err != nil {
			fmt.Fprintf(&b, "\n[%d] id=%s (could not load: %v)\n", i+1, m.ID, err)
			continue
		}
		unread := ""
		for _, l := range msg.LabelIDs {
			if l == "UNREAD" {
				unread = " · unread"
			}
		}
		fmt.Fprintf(&b, "\n[%d] id=%s%s\nFrom: %s\nSubject: %s\nDate: %s\n%s\n", i+1, msg.ID, unread,
			oneLine(decodeHeader(msg.header("From"))), oneLine(decodeHeader(msg.header("Subject"))), msg.header("Date"),
			clip(html.UnescapeString(oneLine(msg.Snippet)), 300))
	}
	return tools.Text(capText(b.String(), maxList, "narrow the query")), nil
}

func (s *Service) gmailRead(ctx context.Context, c tools.Call) (tools.Result, error) {
	id := strings.TrimSpace(c.String("id"))
	if id == "" {
		return tools.Errorf("id is required"), nil
	}
	var msg gmailMessage
	if err := s.getJSON(ctx, s.ep.Gmail+"/messages/"+url.PathEscape(id)+"?format=full", &msg); err != nil {
		return toolErr(err), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "id=%s thread=%s\n", msg.ID, msg.ThreadID)
	for _, h := range []string{"From", "To", "Cc", "Date", "Subject"} {
		if v := msg.header(h); v != "" {
			fmt.Fprintf(&b, "%s: %s\n", h, oneLine(decodeHeader(v)))
		}
	}
	plain, htm, atts := walkParts(msg.Payload)
	body := plain
	if strings.TrimSpace(body) == "" && htm != "" {
		body = htmlToText(htm)
	}
	if strings.TrimSpace(body) == "" {
		body = html.UnescapeString(msg.Snippet)
	}
	b.WriteString("\n" + capText(strings.TrimSpace(body), maxBody, "the rest of the message is not shown") + "\n")
	if len(atts) > 0 {
		b.WriteString("\nAttachments:\n")
		for _, a := range atts {
			b.WriteString("- " + a + "\n")
		}
	}
	return tools.Text(b.String()), nil
}

// walkParts collects the text/plain and text/html bodies and attachment
// descriptions of a MIME tree.
func walkParts(p gmailPart) (plain, htm string, atts []string) {
	var walk func(p gmailPart)
	walk = func(p gmailPart) {
		if p.Filename != "" {
			atts = append(atts, fmt.Sprintf("%s (%s, %s)", p.Filename, p.MimeType, humanSize(p.Body.Size)))
			return
		}
		switch {
		case strings.HasPrefix(p.MimeType, "multipart/"):
			for _, c := range p.Parts {
				walk(c)
			}
		case p.MimeType == "text/plain" && plain == "":
			plain = decodeB64URL(p.Body.Data)
		case p.MimeType == "text/html" && htm == "":
			htm = decodeB64URL(p.Body.Data)
		}
	}
	walk(p)
	return
}

func decodeB64URL(s string) string {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return ""
	}
	return string(b)
}

func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

var (
	reDrop  = regexp.MustCompile(`(?is)<(script|style|head)[^>]*>.*?</(script|style|head)>`)
	reBreak = regexp.MustCompile(`(?i)<(br|/p|/div|/tr|/h[1-6]|/li|/table)[^>]*>`)
	reLi    = regexp.MustCompile(`(?i)<li[^>]*>`)
	reTag   = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpace = regexp.MustCompile(`[ \t\r\f\v]+`)
	reLines = regexp.MustCompile(`\n\s*\n\s*\n+`)
)

// htmlToText turns an HTML mail body into readable text.
func htmlToText(h string) string {
	h = reDrop.ReplaceAllString(h, "")
	h = reBreak.ReplaceAllString(h, "\n")
	h = reLi.ReplaceAllString(h, "\n- ")
	h = reTag.ReplaceAllString(h, "")
	h = html.UnescapeString(h)
	h = reSpace.ReplaceAllString(h, " ")
	lines := strings.Split(h, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(reLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// addrList validates a comma-separated address list.
func addrList(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if strings.ContainsAny(v, "\r\n") {
		return "", fmt.Errorf("addresses must be on one line")
	}
	list, err := mail.ParseAddressList(v)
	if err != nil {
		return "", fmt.Errorf("invalid address list %q: %v", v, err)
	}
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.String()
	}
	return strings.Join(out, ", "), nil
}

func (s *Service) gmailCompose(ctx context.Context, c tools.Call, send bool) (tools.Result, error) {
	var a struct {
		To, Cc, Bcc, Subject, Body string
		ReplyToID                  string `json:"reply_to_id"`
		DraftID                    string `json:"draft_id"`
	}
	if err := c.Decode(&a); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	if send && a.DraftID != "" {
		var out struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
		}
		if err := s.sendJSON(ctx, "POST", s.ep.Gmail+"/drafts/send", map[string]string{"id": a.DraftID}, &out); err != nil {
			return toolErr(err), nil
		}
		return tools.Text(fmt.Sprintf("Sent draft %s (message id %s).", a.DraftID, out.ID)), nil
	}
	if strings.ContainsAny(a.Subject, "\r\n") {
		return tools.Errorf("subject must be one line"), nil
	}
	var threadID, inReplyTo, refs string
	if a.ReplyToID != "" {
		var orig gmailMessage
		meta := "?format=metadata&metadataHeaders=From&metadataHeaders=Reply-To&metadataHeaders=Subject&metadataHeaders=Message-ID&metadataHeaders=References"
		if err := s.getJSON(ctx, s.ep.Gmail+"/messages/"+url.PathEscape(a.ReplyToID)+meta, &orig); err != nil {
			return toolErr(err), nil
		}
		threadID, inReplyTo = orig.ThreadID, orig.header("Message-ID")
		refs = strings.TrimSpace(orig.header("References") + " " + inReplyTo)
		if strings.TrimSpace(a.To) == "" {
			a.To = orig.header("Reply-To")
			if a.To == "" {
				a.To = orig.header("From")
			}
		}
		if strings.TrimSpace(a.Subject) == "" {
			subj := decodeHeader(orig.header("Subject"))
			if !strings.HasPrefix(strings.ToLower(subj), "re:") {
				subj = "Re: " + subj
			}
			a.Subject = subj
		}
	}
	to, err := addrList(a.To)
	if err != nil {
		return tools.Errorf("to: %v", err), nil
	}
	cc, err := addrList(a.Cc)
	if err != nil {
		return tools.Errorf("cc: %v", err), nil
	}
	bcc, err := addrList(a.Bcc)
	if err != nil {
		return tools.Errorf("bcc: %v", err), nil
	}
	if send && to == "" && cc == "" && bcc == "" {
		return tools.Errorf("no recipients: give to (or reply_to_id)"), nil
	}
	raw := buildMessage(to, cc, bcc, a.Subject, a.Body, inReplyTo, refs)
	msg := map[string]any{"raw": base64.URLEncoding.EncodeToString(raw)}
	if threadID != "" {
		msg["threadId"] = threadID
	}
	if send {
		var out struct {
			ID string `json:"id"`
		}
		if err := s.sendJSON(ctx, "POST", s.ep.Gmail+"/messages/send", msg, &out); err != nil {
			return toolErr(err), nil
		}
		return tools.Text(fmt.Sprintf("Sent to %s (message id %s).", firstNonEmpty(to, cc, bcc), out.ID)), nil
	}
	var out struct {
		ID      string       `json:"id"`
		Message gmailMessage `json:"message"`
	}
	if err := s.sendJSON(ctx, "POST", s.ep.Gmail+"/drafts", map[string]any{"message": msg}, &out); err != nil {
		return toolErr(err), nil
	}
	return tools.Text(fmt.Sprintf("Draft saved (draft_id %s). It has not been sent; gmail_send with draft_id sends it after the user approves.", out.ID)), nil
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// buildMessage renders an RFC 5322 plain-text message (UTF-8, base64 body).
func buildMessage(to, cc, bcc, subject, body, inReplyTo, refs string) []byte {
	var b strings.Builder
	h := func(k, v string) {
		v = strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
		if v != "" {
			b.WriteString(k + ": " + v + "\r\n")
		}
	}
	h("To", to)
	h("Cc", cc)
	h("Bcc", bcc)
	h("Subject", mime.BEncoding.Encode("UTF-8", subject))
	h("In-Reply-To", inReplyTo)
	h("References", refs)
	h("MIME-Version", "1.0")
	h("Content-Type", `text/plain; charset="UTF-8"`)
	h("Content-Transfer-Encoding", "base64")
	b.WriteString("\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return []byte(b.String())
}
