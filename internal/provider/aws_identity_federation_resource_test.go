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
	"github.com/egdaemon/eg/awswif"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"
)

const awsTestRole = "arn:aws:iam::123456789012:role/egdaemon"

// fakeAWS the eg api's /m/aws/wif routes in memory, with the server's semantics: an empty label defaults to
// the account and creating an existing label overwrites it.
type fakeAWS struct {
	m       sync.Mutex
	conns   map[string]*awswif.AWSWIFConnection
	account string
	srv     *httptest.Server
	// endpoints the authorization endpoints the provider asked a client for.
	endpoints []string
	// oidc the response of the issuer route.
	oidc string
}

func newFakeAWS(t *testing.T, account string) *fakeAWS {
	t.Helper()

	f := &fakeAWS{conns: map[string]*awswif.AWSWIFConnection{}, account: account}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /m/aws/wif/", func(w http.ResponseWriter, r *http.Request) {
		var req awswif.AWSWIFCreateRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		f.m.Lock()
		defer f.m.Unlock()

		conn := &awswif.AWSWIFConnection{
			Label:       req.GetConnection().GetLabel(),
			RoleArn:     req.GetConnection().GetRoleArn(),
			Region:      req.GetConnection().GetRegion(),
			Audience:    req.GetConnection().GetAudience(),
			Description: req.GetConnection().GetDescription(),
		}
		if conn.Label == "" {
			conn.Label = f.account
		}
		// the server defaults the audience.
		if conn.Audience == "" {
			conn.Audience = "sts.amazonaws.com"
		}
		f.conns[conn.Label] = conn

		w.WriteHeader(http.StatusCreated)
		require.NoError(t, json.NewEncoder(w).Encode(&awswif.AWSWIFCreateResponse{Connection: conn}))
	})
	mux.HandleFunc("GET /m/aws/oidc/", func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte(f.oidc))
		require.NoError(t, err)
	})
	mux.HandleFunc("GET /m/aws/wif/{label}", func(w http.ResponseWriter, r *http.Request) {
		f.m.Lock()
		defer f.m.Unlock()

		conn, ok := f.conns[r.PathValue("label")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(&awswif.AWSWIFFindResponse{Connection: conn}))
	})
	mux.HandleFunc("PATCH /m/aws/wif/{label}", func(w http.ResponseWriter, r *http.Request) {
		var req awswif.AWSWIFUpdateRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		f.m.Lock()
		defer f.m.Unlock()

		conn, ok := f.conns[r.PathValue("label")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if req.RoleArn != nil {
			conn.RoleArn = req.GetRoleArn()
		}
		if req.Region != nil {
			conn.Region = req.GetRegion()
		}
		if req.Audience != nil {
			conn.Audience = req.GetAudience()
		}
		if req.Description != nil {
			conn.Description = req.GetDescription()
		}
		require.NoError(t, json.NewEncoder(w).Encode(&awswif.AWSWIFUpdateResponse{Connection: conn}))
	})
	mux.HandleFunc("DELETE /m/aws/wif/{label}", func(w http.ResponseWriter, r *http.Request) {
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
func (t *fakeAWS) client(endpoint string) (*http.Client, error) {
	t.m.Lock()
	t.endpoints = append(t.endpoints, endpoint)
	t.m.Unlock()

	dst, err := url.Parse(t.srv.URL)
	if err != nil {
		return nil, err
	}

	return &http.Client{Transport: hostrewrite{dst: dst}}, nil
}

func (t *fakeAWS) factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"eg": providerserver.NewProtocol6WithError(&egt{version: "test", client: t.client}),
	}
}

func (t *fakeAWS) put(conn *awswif.AWSWIFConnection) {
	t.m.Lock()
	defer t.m.Unlock()
	t.conns[conn.GetLabel()] = conn
}

func (t *fakeAWS) has(label string) bool {
	t.m.Lock()
	defer t.m.Unlock()
	_, ok := t.conns[label]
	return ok
}

func (t *fakeAWS) remove(label string) {
	t.m.Lock()
	defer t.m.Unlock()
	delete(t.conns, label)
}

func awsWifConfig(account string, resourceBody string) string {
	return `
provider "eg" {
  account_id = "` + account + `"
}

resource "eg_aws_identity_federation" "test" {
` + resourceBody + `
}
`
}

func TestAWSIdentityFederationResource(t *testing.T) {
	t.Run("should create with the account as the default label, update, import, and destroy", func(t *testing.T) {
		api := newFakeAWS(t, "account-1")

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: api.factories(),
			Steps: []resource.TestStep{
				{
					Config: awsWifConfig("account-1", `role_arn = "`+awsTestRole+`"
  description = "production"`),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("eg_aws_identity_federation.test", "id", "account-1"),
						resource.TestCheckResourceAttr("eg_aws_identity_federation.test", "label", "account-1"),
						resource.TestCheckResourceAttr("eg_aws_identity_federation.test", "role_arn", awsTestRole),
						resource.TestCheckResourceAttr("eg_aws_identity_federation.test", "audience", "sts.amazonaws.com"),
						resource.TestCheckResourceAttr("eg_aws_identity_federation.test", "region", ""),
						resource.TestCheckResourceAttr("eg_aws_identity_federation.test", "description", "production"),
						func(*terraform.State) error { require.True(t, api.has("account-1")); return nil },
					),
				},
				{
					Config: awsWifConfig("account-1", `role_arn = "`+awsTestRole+`"
  region = "us-east-2"
  description = "changed"`),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("eg_aws_identity_federation.test", "region", "us-east-2"),
						resource.TestCheckResourceAttr("eg_aws_identity_federation.test", "description", "changed"),
					),
				},
				{
					// removing the attributes clears them.
					Config: awsWifConfig("account-1", `role_arn = "`+awsTestRole+`"`),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("eg_aws_identity_federation.test", "region", ""),
						resource.TestCheckResourceAttr("eg_aws_identity_federation.test", "description", ""),
					),
				},
				{
					ResourceName:      "eg_aws_identity_federation.test",
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
		api := newFakeAWS(t, "account-1")

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: api.factories(),
			Steps: []resource.TestStep{
				{
					Config: awsWifConfig("account-1", `label = "prod"
  role_arn = "`+awsTestRole+`"`),
					Check: resource.TestCheckResourceAttr("eg_aws_identity_federation.test", "label", "prod"),
				},
				{
					Config: awsWifConfig("account-1", `label = "staging"
  role_arn = "`+awsTestRole+`"`),
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
		api := newFakeAWS(t, "account-1")
		cfg := awsWifConfig("account-1", `role_arn = "`+awsTestRole+`"`)

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
		api := newFakeAWS(t, "account-1")
		api.put(&awswif.AWSWIFConnection{Label: "account-1", RoleArn: "someone-elses"})

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: api.factories(),
			Steps: []resource.TestStep{
				{
					Config:      awsWifConfig("account-1", `role_arn = "`+awsTestRole+`"`),
					ExpectError: regexp.MustCompile("already\\s+exists"),
				},
			},
		})

		api.m.Lock()
		defer api.m.Unlock()
		require.Equal(t, "someone-elses", api.conns["account-1"].GetRoleArn())
	})

	t.Run("should let the api choose the default label when the account is not configured", func(t *testing.T) {
		api := newFakeAWS(t, "from-the-api")

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: api.factories(),
			Steps: []resource.TestStep{
				{
					Config: `
provider "eg" {}

resource "eg_aws_identity_federation" "test" {
  role_arn = "` + awsTestRole + `"
}
`,
					Check: resource.TestCheckResourceAttr("eg_aws_identity_federation.test", "label", "from-the-api"),
				},
			},
		})
	})
}

func TestAWSOIDCDataSource(t *testing.T) {
	t.Run("should read the issuer and subject of the account", func(t *testing.T) {
		api := newFakeAWS(t, "account-1")
		api.oidc = `{"issuer_url":"https://sso.example.com","subject":"account:account-1"}`

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: api.factories(),
			Steps: []resource.TestStep{
				{
					Config: `
provider "eg" {
  account_id = "account-1"
}

data "eg_aws_oidc" "test" {}
`,
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("data.eg_aws_oidc.test", "issuer_url", "https://sso.example.com"),
						resource.TestCheckResourceAttr("data.eg_aws_oidc.test", "subject", "account:account-1"),
					),
				},
			},
		})
	})
}
