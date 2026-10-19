package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sync"
	"testing"

	"github.com/egdaemon/eg/authn"
	"github.com/egdaemon/eg/gcpwif"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"
)

const wifTestAudience = "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/egdaemon/providers/egd-workloads"

// hostrewrite sends requests to dst, keeping the path and query.
type hostrewrite struct {
	dst *url.URL
}

func (t hostrewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	dup := *req.URL
	dup.Scheme, dup.Host = t.dst.Scheme, t.dst.Host
	req = req.Clone(req.Context())
	req.URL, req.Host = &dup, t.dst.Host
	return http.DefaultTransport.RoundTrip(req)
}

// fakeEG the eg api's /m/gcp/wif routes in memory, with the server's semantics: an empty label defaults to
// the account and creating an existing label overwrites it.
type fakeEG struct {
	m       sync.Mutex
	conns   map[string]*gcpwif.GCPWIFConnection
	account string
	srv     *httptest.Server
	// endpoints the authorization endpoints the provider asked a client for.
	endpoints []string
}

func newFakeEG(t *testing.T, account string) *fakeEG {
	t.Helper()

	f := &fakeEG{conns: map[string]*gcpwif.GCPWIFConnection{}, account: account}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /m/gcp/wif/", func(w http.ResponseWriter, r *http.Request) {
		var req gcpwif.GCPWIFCreateRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		f.m.Lock()
		defer f.m.Unlock()

		conn := &gcpwif.GCPWIFConnection{
			Label:                     req.GetConnection().GetLabel(),
			Audience:                  req.GetConnection().GetAudience(),
			ImpersonateServiceAccount: req.GetConnection().GetImpersonateServiceAccount(),
			Description:               req.GetConnection().GetDescription(),
		}
		if conn.Label == "" {
			conn.Label = f.account
		}
		f.conns[conn.Label] = conn

		w.WriteHeader(http.StatusCreated)
		require.NoError(t, json.NewEncoder(w).Encode(&gcpwif.GCPWIFCreateResponse{Connection: conn}))
	})
	mux.HandleFunc("GET /m/gcp/wif/{label}", func(w http.ResponseWriter, r *http.Request) {
		f.m.Lock()
		defer f.m.Unlock()

		conn, ok := f.conns[r.PathValue("label")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(&gcpwif.GCPWIFFindResponse{Connection: conn}))
	})
	mux.HandleFunc("PATCH /m/gcp/wif/{label}", func(w http.ResponseWriter, r *http.Request) {
		var req gcpwif.GCPWIFUpdateRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		f.m.Lock()
		defer f.m.Unlock()

		conn, ok := f.conns[r.PathValue("label")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if req.Audience != nil {
			conn.Audience = req.GetAudience()
		}
		if req.ImpersonateServiceAccount != nil {
			conn.ImpersonateServiceAccount = req.GetImpersonateServiceAccount()
		}
		if req.Description != nil {
			conn.Description = req.GetDescription()
		}
		require.NoError(t, json.NewEncoder(w).Encode(&gcpwif.GCPWIFUpdateResponse{Connection: conn}))
	})
	mux.HandleFunc("DELETE /m/gcp/wif/{label}", func(w http.ResponseWriter, r *http.Request) {
		f.m.Lock()
		defer f.m.Unlock()

		if _, ok := f.conns[r.PathValue("label")]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(f.conns, r.PathValue("label"))
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// client the http client the provider is given in place of one built from the ssh key.
func (t *fakeEG) client(endpoint string) (*http.Client, error) {
	t.m.Lock()
	t.endpoints = append(t.endpoints, endpoint)
	t.m.Unlock()

	dst, err := url.Parse(t.srv.URL)
	if err != nil {
		return nil, err
	}

	return &http.Client{Transport: hostrewrite{dst: dst}}, nil
}

func (t *fakeEG) factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"eg": providerserver.NewProtocol6WithError(&egt{version: "test", client: t.client}),
	}
}

func (t *fakeEG) put(conn *gcpwif.GCPWIFConnection) {
	t.m.Lock()
	defer t.m.Unlock()
	t.conns[conn.GetLabel()] = conn
}

func (t *fakeEG) has(label string) bool {
	t.m.Lock()
	defer t.m.Unlock()
	_, ok := t.conns[label]
	return ok
}

func (t *fakeEG) remove(label string) {
	t.m.Lock()
	defer t.m.Unlock()
	delete(t.conns, label)
}

func wifConfig(account string, resourceBody string) string {
	return `
provider "eg" {
  account_id = "` + account + `"
}

resource "eg_gcp_identity_federation" "test" {
` + resourceBody + `
}
`
}

func TestGCPIdentityFederationResource(t *testing.T) {
	t.Run("should create with the account as the default label, update, import, and destroy", func(t *testing.T) {
		api := newFakeEG(t, "account-1")

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: api.factories(),
			Steps: []resource.TestStep{
				{
					Config: wifConfig("account-1", `audience = "`+wifTestAudience+`"
  description = "production"`),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("eg_gcp_identity_federation.test", "id", "account-1"),
						resource.TestCheckResourceAttr("eg_gcp_identity_federation.test", "label", "account-1"),
						resource.TestCheckResourceAttr("eg_gcp_identity_federation.test", "audience", wifTestAudience),
						resource.TestCheckResourceAttr("eg_gcp_identity_federation.test", "impersonate_service_account", ""),
						resource.TestCheckResourceAttr("eg_gcp_identity_federation.test", "description", "production"),
						func(*terraform.State) error { require.True(t, api.has("account-1")); return nil },
					),
				},
				{
					Config: wifConfig("account-1", `audience = "`+wifTestAudience+`"
  impersonate_service_account = "reader@example.iam.gserviceaccount.com"
  description = "changed"`),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("eg_gcp_identity_federation.test", "impersonate_service_account", "reader@example.iam.gserviceaccount.com"),
						resource.TestCheckResourceAttr("eg_gcp_identity_federation.test", "description", "changed"),
					),
				},
				{
					// removing the attributes clears them.
					Config: wifConfig("account-1", `audience = "`+wifTestAudience+`"`),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("eg_gcp_identity_federation.test", "impersonate_service_account", ""),
						resource.TestCheckResourceAttr("eg_gcp_identity_federation.test", "description", ""),
					),
				},
				{
					ResourceName:      "eg_gcp_identity_federation.test",
					ImportState:       true,
					ImportStateId:     "account-1",
					ImportStateVerify: true,
				},
			},
		})

		require.False(t, api.has("account-1"), "destroy should remove the connection")

		// the account management routes check the meta authorization token.
		api.m.Lock()
		defer api.m.Unlock()
		require.NotEmpty(t, api.endpoints)
		for _, e := range api.endpoints {
			require.Equal(t, authn.EndpointMetaAuthz(), e)
		}
	})

	t.Run("should use an explicit label and replace the connection when it changes", func(t *testing.T) {
		api := newFakeEG(t, "account-1")

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: api.factories(),
			Steps: []resource.TestStep{
				{
					Config: wifConfig("account-1", `label = "prod"
  audience = "`+wifTestAudience+`"`),
					Check: resource.TestCheckResourceAttr("eg_gcp_identity_federation.test", "label", "prod"),
				},
				{
					Config: wifConfig("account-1", `label = "staging"
  audience = "`+wifTestAudience+`"`),
					Check: func(*terraform.State) error {
						require.False(t, api.has("prod"))
						require.True(t, api.has("staging"))
						return nil
					},
				},
			},
		})
	})

	t.Run("should recreate a connection removed out of band", func(t *testing.T) {
		api := newFakeEG(t, "account-1")
		cfg := wifConfig("account-1", `audience = "`+wifTestAudience+`"`)

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: api.factories(),
			Steps: []resource.TestStep{
				{Config: cfg},
				{
					PreConfig: func() { api.remove("account-1") },
					Config:    cfg,
					Check: func(*terraform.State) error {
						require.True(t, api.has("account-1"))
						return nil
					},
				},
			},
		})
	})

	t.Run("should not overwrite an existing connection", func(t *testing.T) {
		api := newFakeEG(t, "account-1")
		api.put(&gcpwif.GCPWIFConnection{Label: "account-1", Audience: "someone-elses"})

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: api.factories(),
			Steps: []resource.TestStep{
				{
					Config:      wifConfig("account-1", `audience = "`+wifTestAudience+`"`),
					ExpectError: regexp.MustCompile("already\\s+exists"),
				},
			},
		})

		api.m.Lock()
		defer api.m.Unlock()
		require.Equal(t, "someone-elses", api.conns["account-1"].GetAudience())
	})

	t.Run("should let the api choose the default label when the account is not configured", func(t *testing.T) {
		api := newFakeEG(t, "from-the-api")

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: api.factories(),
			Steps: []resource.TestStep{
				{
					Config: `
provider "eg" {}

resource "eg_gcp_identity_federation" "test" {
  audience = "` + wifTestAudience + `"
}
`,
					Check: resource.TestCheckResourceAttr("eg_gcp_identity_federation.test", "label", "from-the-api"),
				},
			},
		})
	})
}
