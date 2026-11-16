package imconfig

// Picking the group in the connect dialog instead of pasting its id: for
// 飞书 / Lark the groups the bot is in are listed (GET /open-apis/im/v1/
// chats with the app's tenant token; it needs one of the im:chat
// permissions, e.g. im:chat:readonly). Nothing is stored and nothing is
// sent to the groups.
//
//	POST /api/im/config/chats  {platform, region, appId, botToken, id}
//	  → {chats: [{id, name, description, external}], hint?}

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Chat is a group the bot can post to.
type Chat struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	External    bool   `json:"external,omitempty"`
}

// ChatList is what the dialog shows. Hint names what to do when the list
// cannot be read (a missing permission) or is empty; the dialog still
// accepts a pasted id then.
type ChatList struct {
	Chats []Chat `json:"chats"`
	Hint  string `json:"hint,omitempty"`
}

// Hints the UI words (it translates them).
const (
	HintNoPermission = "permission" // im:chat:readonly missing
	HintNotInGroups  = "empty"      // the bot is in no group yet
	HintNoList       = "unsupported"
)

// maxChats bounds the listing (pages of 100).
const maxChats = 500

// Chats lists the groups the bot is in, for platforms that can list them.
func (s *Service) Chats(ctx context.Context, in Check) (ChatList, error) {
	in = s.fill(in)
	p, ok := platform(in.Platform)
	if !ok {
		return ChatList{}, fmt.Errorf("unknown platform %q", in.Platform)
	}
	if p.ID != "feishu" {
		return ChatList{Chats: []Chat{}, Hint: HintNoList}, nil
	}
	r, ok := p.region(in.Region)
	if !ok {
		return ChatList{}, fmt.Errorf("unknown region %q", in.Region)
	}
	appID, secret := strings.TrimSpace(in.AppID), strings.TrimSpace(in.BotToken)
	if appID == "" || secret == "" {
		return ChatList{}, errors.New("the App ID and App Secret are required")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := feishuChats(ctx, r.Domain, appID, secret)
	if err != nil {
		return ChatList{}, errors.New(strings.ReplaceAll(err.Error(), secret, "***"))
	}
	return out, nil
}

func feishuChats(ctx context.Context, domain, appID, secret string) (ChatList, error) {
	base := strings.TrimRight(domain, "/")
	var t struct {
		Code  int    `json:"code"`
		Msg   string `json:"msg"`
		Token string `json:"tenant_access_token"`
	}
	if _, err := call(ctx, "POST", base+"/open-apis/auth/v3/tenant_access_token/internal", nil,
		map[string]string{"app_id": appID, "app_secret": secret}, &t); err != nil {
		return ChatList{}, err
	}
	if t.Code != 0 || t.Token == "" {
		return ChatList{}, fmt.Errorf("%d: %s", t.Code, nonEmpty(t.Msg, "rejected"))
	}
	auth := map[string]string{"Authorization": "Bearer " + t.Token}
	out := ChatList{Chats: []Chat{}}
	page := ""
	for len(out.Chats) < maxChats {
		q := url.Values{"page_size": {"100"}}
		if page != "" {
			q.Set("page_token", page)
		}
		var res struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				Items []struct {
					ChatID      string `json:"chat_id"`
					Name        string `json:"name"`
					Description string `json:"description"`
					External    bool   `json:"external"`
					Status      string `json:"chat_status"`
				} `json:"items"`
				HasMore   bool   `json:"has_more"`
				PageToken string `json:"page_token"`
			} `json:"data"`
		}
		if _, err := call(ctx, "GET", base+"/open-apis/im/v1/chats?"+q.Encode(), auth, nil, &res); err != nil {
			return ChatList{}, err
		}
		if res.Code != 0 {
			// 99991672: the app has not been granted the API's scope;
			// 99991676: the token lacks it. The person grants
			// im:chat:readonly (and publishes a version), or pastes the id.
			if res.Code == 99991672 || res.Code == 99991676 {
				return ChatList{Chats: []Chat{}, Hint: HintNoPermission}, nil
			}
			return ChatList{}, fmt.Errorf("%d: %s", res.Code, nonEmpty(res.Msg, "groups could not be listed"))
		}
		for _, it := range res.Data.Items {
			if it.ChatID == "" || (it.Status != "" && it.Status != "normal") {
				continue // dissolved groups
			}
			out.Chats = append(out.Chats, Chat{ID: it.ChatID, Name: it.Name, Description: it.Description, External: it.External})
		}
		if !res.Data.HasMore || res.Data.PageToken == "" {
			break
		}
		page = res.Data.PageToken
	}
	if len(out.Chats) == 0 {
		out.Hint = HintNotInGroups
	}
	return out, nil
}
