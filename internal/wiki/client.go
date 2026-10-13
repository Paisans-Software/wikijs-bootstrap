// Package wiki talks to one Wiki.js 2.5 instance: its setup endpoint and its
// GraphQL API, as the bootstrap needs them and no further.
package wiki

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

var ErrLogin = errors.New("login refused")

type Client struct {
	BaseURL string
	HTTP    *http.Client
	Log     func(format string, args ...any)
}

type Finalize struct{ AdminEmail, AdminPassword, SiteURL string }

type Strategy struct {
	Key, StrategyKey, DisplayName string
	Order                         int
	Enabled, SelfRegistration     bool
	Config                        map[string]any
}

type Site struct {
	Host, Title, Description, LogoURL string
	AutoLogin, HideLocal, TrustProxy  bool
	UploadMaxFileSize                 int
}

type GroupSummary struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type PageRule struct {
	ID      string   `json:"id"`
	Deny    bool     `json:"deny"`
	Match   string   `json:"match"`
	Roles   []string `json:"roles"`
	Path    string   `json:"path"`
	Locales []string `json:"locales"`
}

type Group struct {
	ID              int        `json:"id"`
	Name            string     `json:"name"`
	RedirectOnLogin string     `json:"redirectOnLogin"`
	Permissions     []string   `json:"permissions"`
	PageRules       []PageRule `json:"pageRules"`
}

const (
	qLogin       = `mutation($u:String!,$p:String!){authentication{login(username:$u,password:$p,strategy:"local"){jwt responseResult{succeeded message}}}}`
	qStrategies  = `mutation($s:[AuthenticationStrategyInput]!){authentication{updateStrategies(strategies:$s){responseResult{succeeded message}}}}`
	qSite        = `mutation($host:String,$title:String,$description:String,$logoUrl:String,$autoLogin:Boolean,$hideLocal:Boolean,$trustProxy:Boolean,$upload:Int){site{updateConfig(host:$host,title:$title,description:$description,logoUrl:$logoUrl,authAutoLogin:$autoLogin,authHideLocal:$hideLocal,securityTrustProxy:$trustProxy,uploadMaxFileSize:$upload){responseResult{succeeded message}}}}`
	qGroups      = `{groups{list{id name}}}`
	qGroup       = `query($id:Int!){groups{single(id:$id){id name redirectOnLogin permissions pageRules{id deny match roles path locales}}}}`
	qCreateGroup = `mutation($n:String!){groups{create(name:$n){responseResult{succeeded message} group{id}}}}`
	qUpdateGroup = `mutation($id:Int!,$n:String!,$r:String!,$perm:[String]!,$rules:[PageRuleInput]!){groups{update(id:$id,name:$n,redirectOnLogin:$r,permissions:$perm,pageRules:$rules){responseResult{succeeded message}}}}`
)

type result struct {
	Succeeded bool   `json:"succeeded"`
	Message   string `json:"message"`
}

func (c *Client) Healthy(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/healthz", nil)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET /healthz: %s", resp.Status)
	}
	return nil
}

// SetupMode is whether the instance serves its setup wizard. In setup mode
// Wiki.js answers GET * and POST /finalize and 404s everything else
// (server/setup.js:68,76,382 at v2.5.316), so a POST to /graphql tells them
// apart without changing anything.
func (c *Client) SetupMode(ctx context.Context) (bool, error) {
	status, _, err := c.post(ctx, "/graphql", "", map[string]any{"query": "{__typename}"})
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusNotFound:
		return true, nil
	case http.StatusOK:
		return false, nil
	}
	return false, fmt.Errorf("POST /graphql answered %d", status)
}

// Finalize runs Wiki.js's setup. It deletes every user and group before
// seeding (server/setup.js:204-223), so the caller holds the advisory lock
// and has seen an empty settings table.
func (c *Client) Finalize(ctx context.Context, in Finalize) error {
	status, body, err := c.post(ctx, "/finalize", "", map[string]any{"adminEmail": in.AdminEmail, "adminPassword": in.AdminPassword, "siteUrl": in.SiteURL, "telemetry": false})
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("answered %d", status)
	}
	if err == nil {
		var r struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &r) != nil || !r.OK {
			err = fmt.Errorf("refused: %s", r.Error)
		}
	}
	c.logWrite("finalize", in.SiteURL, err)
	return err
}

func (c *Client) Login(ctx context.Context, email, password string) (string, error) {
	var out struct {
		Authentication struct {
			Login struct {
				JWT            *string `json:"jwt"`
				ResponseResult result  `json:"responseResult"`
			} `json:"login"`
		} `json:"authentication"`
	}
	if err := c.gql(ctx, "", qLogin, map[string]any{"u": email, "p": password}, &out); err != nil {
		return "", err
	}
	l := out.Authentication.Login
	if !l.ResponseResult.Succeeded || l.JWT == nil {
		return "", fmt.Errorf("%w for %s: %s", ErrLogin, email, l.ResponseResult.Message)
	}
	return *l.JWT, nil
}

func (c *Client) UpdateStrategies(ctx context.Context, token string, s []Strategy) error {
	in := make([]map[string]any, 0, len(s))
	for _, st := range s {
		cfg := make([]map[string]string, 0, len(st.Config))
		for k, v := range st.Config {
			wrapped, _ := json.Marshal(map[string]any{"v": v})
			cfg = append(cfg, map[string]string{"key": k, "value": string(wrapped)})
		}
		in = append(in, map[string]any{
			"key": st.Key, "strategyKey": st.StrategyKey, "displayName": st.DisplayName,
			"order": st.Order, "isEnabled": st.Enabled, "selfRegistration": st.SelfRegistration,
			"domainWhitelist": []string{}, "autoEnrollGroups": []int{}, "config": cfg,
		})
	}
	var out struct {
		Authentication struct {
			UpdateStrategies struct {
				ResponseResult result `json:"responseResult"`
			} `json:"updateStrategies"`
		} `json:"authentication"`
	}
	err := c.gql(ctx, token, qStrategies, map[string]any{"s": in}, &out)
	err = check(err, out.Authentication.UpdateStrategies.ResponseResult)
	c.logWrite("authentication.updateStrategies", fmt.Sprintf("%d strategies", len(s)), err)
	return err
}

func (c *Client) UpdateSite(ctx context.Context, token string, s Site) error {
	var out struct {
		Site struct {
			UpdateConfig struct {
				ResponseResult result `json:"responseResult"`
			} `json:"updateConfig"`
		} `json:"site"`
	}
	err := c.gql(ctx, token, qSite, map[string]any{
		"host": s.Host, "title": s.Title, "description": s.Description, "logoUrl": s.LogoURL,
		"autoLogin": s.AutoLogin, "hideLocal": s.HideLocal, "trustProxy": s.TrustProxy, "upload": s.UploadMaxFileSize,
	}, &out)
	err = check(err, out.Site.UpdateConfig.ResponseResult)
	c.logWrite("site.updateConfig", s.Host, err)
	return err
}

func (c *Client) Groups(ctx context.Context, token string) ([]GroupSummary, error) {
	var out struct {
		Groups struct {
			List []GroupSummary `json:"list"`
		} `json:"groups"`
	}
	err := c.gql(ctx, token, qGroups, nil, &out)
	return out.Groups.List, err
}

func (c *Client) Group(ctx context.Context, token string, id int) (Group, error) {
	var out struct {
		Groups struct {
			Single Group `json:"single"`
		} `json:"groups"`
	}
	err := c.gql(ctx, token, qGroup, map[string]any{"id": id}, &out)
	return out.Groups.Single, err
}

func (c *Client) CreateGroup(ctx context.Context, token, name string) (int, error) {
	var out struct {
		Groups struct {
			Create struct {
				ResponseResult result `json:"responseResult"`
				Group          struct {
					ID int `json:"id"`
				} `json:"group"`
			} `json:"create"`
		} `json:"groups"`
	}
	err := c.gql(ctx, token, qCreateGroup, map[string]any{"n": name}, &out)
	err = check(err, out.Groups.Create.ResponseResult)
	c.logWrite("groups.create", name, err)
	return out.Groups.Create.Group.ID, err
}

func (c *Client) UpdateGroup(ctx context.Context, token string, g Group) error {
	rules := g.PageRules
	if rules == nil {
		rules = []PageRule{}
	}
	perms := g.Permissions
	if perms == nil {
		perms = []string{}
	}
	var out struct {
		Groups struct {
			Update struct {
				ResponseResult result `json:"responseResult"`
			} `json:"update"`
		} `json:"groups"`
	}
	err := c.gql(ctx, token, qUpdateGroup, map[string]any{"id": g.ID, "n": g.Name, "r": g.RedirectOnLogin, "perm": perms, "rules": rules}, &out)
	err = check(err, out.Groups.Update.ResponseResult)
	c.logWrite("groups.update", fmt.Sprintf("%d %s", g.ID, g.Name), err)
	return err
}

func check(err error, r result) error {
	if err != nil {
		return err
	}
	if !r.Succeeded {
		return fmt.Errorf("refused: %s", r.Message)
	}
	return nil
}

func (c *Client) logWrite(what, target string, err error) {
	if c.Log == nil {
		return
	}
	if err != nil {
		c.Log("write %s %s: %v", what, target, err)
		return
	}
	c.Log("write %s %s: ok", what, target)
}

func (c *Client) gql(ctx context.Context, token, query string, vars map[string]any, out any) error {
	status, body, err := c.post(ctx, "/graphql", token, map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("POST /graphql answered %d", status)
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("POST /graphql: %w", err)
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("graphql: %s", env.Errors[0].Message)
	}
	return json.Unmarshal(env.Data, out)
}

func (c *Client) post(ctx context.Context, path, token string, payload any) (int, []byte, error) {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}
