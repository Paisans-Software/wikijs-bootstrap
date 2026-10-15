package wiki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fake answers GraphQL by matching a word in the query, and records each.
type fake struct {
	setup   bool
	queries []string
	answer  map[string]string
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.URL.Path == "/healthz":
		w.WriteHeader(200)
	case f.setup && r.URL.Path == "/finalize":
		f.queries = append(f.queries, "finalize "+string(body))
		io.WriteString(w, `{"ok":true}`)
	case f.setup:
		http.NotFound(w, r)
	case r.URL.Path == "/graphql":
		var q struct{ Query string }
		json.Unmarshal(body, &q)
		f.queries = append(f.queries, string(body)) // whole request: variables carry the values
		for word, a := range f.answer {
			if strings.Contains(q.Query, word) {
				io.WriteString(w, a)
				return
			}
		}
		io.WriteString(w, `{"data":{}}`)
	}
}

func client(f *fake) (*Client, *[]string) {
	var logged []string
	s := httptest.NewServer(f)
	return &Client{BaseURL: s.URL, HTTP: s.Client(), Log: func(format string, a ...any) {
		logged = append(logged, format)
	}}, &logged
}

func TestSetupModeIsA404OnGraphQL(t *testing.T) {
	c, _ := client(&fake{setup: true})
	if on, err := c.SetupMode(context.Background()); err != nil || !on {
		t.Errorf("setup mode = %v, %v", on, err)
	}
	c, _ = client(&fake{})
	if on, err := c.SetupMode(context.Background()); err != nil || on {
		t.Errorf("normal mode = %v, %v", on, err)
	}
}

func TestLoginReturnsTheJWTOrErrLogin(t *testing.T) {
	f := &fake{answer: map[string]string{"login": `{"data":{"authentication":{"login":{"jwt":"tok","responseResult":{"succeeded":true}}}}}`}}
	c, _ := client(f)
	if tok, err := c.Login(context.Background(), "a@example.org", "pw"); err != nil || tok != "tok" {
		t.Errorf("login = %q, %v", tok, err)
	}
	f.answer["login"] = `{"data":{"authentication":{"login":{"jwt":null,"responseResult":{"succeeded":false,"message":"Invalid email / username or password."}}}}}`
	if _, err := c.Login(context.Background(), "a@example.org", "pw"); !errors.Is(err, ErrLogin) {
		t.Errorf("err = %v", err)
	}
}

func TestStrategyConfigIsWrappedForWikiJS(t *testing.T) {
	f := &fake{answer: map[string]string{"updateStrategies": `{"data":{"authentication":{"updateStrategies":{"responseResult":{"succeeded":true}}}}}`}}
	c, logged := client(f)
	err := c.UpdateStrategies(context.Background(), "tok", []Strategy{{Key: "pocketid", StrategyKey: "oauth2", DisplayName: "Pocket ID", Config: map[string]any{"mapGroups": true}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.queries[0], `{\"v\":true}`) {
		t.Errorf("config not wrapped: %s", f.queries[0])
	}
	if len(*logged) != 1 {
		t.Errorf("logged %d lines for one write", len(*logged))
	}
}

func TestAFailedMutationIsAnError(t *testing.T) {
	f := &fake{answer: map[string]string{"updateConfig": `{"data":{"site":{"updateConfig":{"responseResult":{"succeeded":false,"message":"nope"}}}}}`}}
	c, _ := client(f)
	if err := c.UpdateSite(context.Background(), "tok", Site{Host: "https://wiki.example.org"}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("err = %v", err)
	}
}

func TestNoPasswordInLogsOrErrors(t *testing.T) {
	const pw = "s3cret-pw-value"
	// Setup-mode fake answers /finalize ok; a non-setup one 404s it.
	var lines []string
	f := &fake{answer: map[string]string{"login": `{"data":{"authentication":{"login":{"jwt":null,"responseResult":{"succeeded":false,"message":"bad"}}}}}`}}
	s := httptest.NewServer(f)
	c := &Client{BaseURL: s.URL, HTTP: s.Client(), Log: func(format string, a ...any) {
		lines = append(lines, fmt.Sprintf(format, a...))
	}}
	_, err := c.Login(context.Background(), "a@example.org", pw)
	ferr := c.Finalize(context.Background(), Finalize{AdminEmail: "a@example.org", AdminPassword: pw, SiteURL: "https://w.example.org"})
	all := strings.Join(lines, "\n")
	for _, e := range []error{err, ferr} {
		if e != nil {
			all += "\n" + e.Error()
		}
	}
	if ferr == nil || len(lines) != 1 {
		t.Fatalf("finalize err = %v, lines = %v", ferr, lines)
	}
	if strings.Contains(all, pw) {
		t.Errorf("password leaked: %s", all)
	}
}

func TestActiveStrategiesUnwrapsEachConfigValue(t *testing.T) {
	f := &fake{answer: map[string]string{"activeStrategies": `{"data":{"authentication":{"activeStrategies":[
		{"key":"pocketid","strategy":{"key":"oauth2"},"displayName":"Pocket ID","order":0,"isEnabled":true,"selfRegistration":true,
		 "config":[{"key":"clientId","value":"{\"type\":\"String\",\"title\":\"Client ID\",\"order\":1,\"value\":\"cid\"}"},
		           {"key":"mapGroups","value":"{\"type\":\"Boolean\",\"default\":false,\"value\":true}"}]},
		{"key":"local","strategy":{"key":"local"},"displayName":"Local","order":1,"isEnabled":true,"selfRegistration":false,"config":[]}]}}}`}}
	c, logged := client(f)
	got, err := c.ActiveStrategies(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != "pocketid" || got[0].StrategyKey != "oauth2" || !got[0].Enabled || !got[0].SelfRegistration || got[1].Order != 1 {
		t.Fatalf("%+v", got)
	}
	if got[0].Config["clientId"] != "cid" || got[0].Config["mapGroups"] != true || len(got[1].Config) != 0 {
		t.Errorf("config %+v %+v", got[0].Config, got[1].Config)
	}
	if strings.Contains(f.queries[0], "mutation") || len(*logged) != 0 {
		t.Errorf("a read was a mutation or logged as a write: %s %v", f.queries[0], *logged)
	}
}

func TestSiteConfigReadsWhatUpdateSiteWrites(t *testing.T) {
	f := &fake{answer: map[string]string{"config": `{"data":{"site":{"config":{"host":"https://wiki.example.org","title":"T","description":null,"logoUrl":"","authAutoLogin":true,"authHideLocal":true,"securityTrustProxy":false,"uploadMaxFileSize":5242880}}}}`}}
	c, _ := client(f)
	got, err := c.SiteConfig(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	want := Site{Host: "https://wiki.example.org", Title: "T", AutoLogin: true, HideLocal: true, UploadMaxFileSize: 5242880}
	if got != want {
		t.Errorf("%+v", got)
	}
}
