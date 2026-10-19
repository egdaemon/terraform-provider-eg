package provider

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/stretchr/testify/require"
)

// configureProvider runs the provider's Configure with account_id set to account-1 and the ssh key path, empty for unset.
func configureProvider(t *testing.T, p *egt, sshkeypath string) *providerData {
	t.Helper()

	var schemaResp provider.SchemaResponse
	p.Schema(t.Context(), provider.SchemaRequest{}, &schemaResp)

	var key tftypes.Value = tftypes.NewValue(tftypes.String, nil)
	if sshkeypath != "" {
		key = tftypes.NewValue(tftypes.String, sshkeypath)
	}

	raw := tftypes.NewValue(
		tftypes.Object{AttributeTypes: map[string]tftypes.Type{
			"account_id":   tftypes.String,
			"ssh_key_path": tftypes.String,
			"insecure":     tftypes.Bool,
		}},
		map[string]tftypes.Value{
			"account_id":   tftypes.NewValue(tftypes.String, "account-1"),
			"ssh_key_path": key,
			"insecure":     tftypes.NewValue(tftypes.Bool, nil),
		},
	)

	var resp provider.ConfigureResponse
	p.Configure(t.Context(), provider.ConfigureRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: raw}}, &resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)

	data, ok := resp.ResourceData.(*providerData)
	require.True(t, ok)
	return data
}

func TestEgtConfigure(t *testing.T) {
	// resources that do not talk to the eg api never need the ssh key.
	t.Run("should not require an ssh key for eg_tar", func(t *testing.T) {
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
				"eg": providerserver.NewProtocol6WithError(New("test")()),
			},
			Steps: []resource.TestStep{
				{
					Config: `
provider "eg" {
  ssh_key_path = "` + filepath.Join(t.TempDir(), "missing") + `"
}

resource "eg_tar" "test" {
  source {
    base64   = base64encode("hello")
    location = "hello.txt"
  }
}
`,
					Check: resource.TestCheckResourceAttrSet("eg_tar.test", "archiveb64"),
				},
			},
		})
	})

	t.Run("should report a missing ssh key when a resource first needs the api", func(t *testing.T) {
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
				"eg": providerserver.NewProtocol6WithError(New("test")()),
			},
			Steps: []resource.TestStep{
				{
					Config: `
provider "eg" {
  ssh_key_path = "/nonexistent/id_ed25519"
}

resource "eg_gcp_identity_federation" "test" {
  audience = "` + wifTestAudience + `"
}
`,
					ExpectError: regexp.MustCompile("unable\\s+to\\s+read\\s+the\\s+ssh\\s+key"),
				},
			},
		})
	})
}

func TestProviderClient(t *testing.T) {
	t.Run("should build the client once, and only when asked", func(t *testing.T) {
		var calls atomic.Int32
		data := configureProvider(t, &egt{version: "test", client: func() (*http.Client, error) {
			calls.Add(1)
			return &http.Client{}, nil
		}}, "")

		require.Zero(t, calls.Load(), "configure must not build the client")
		require.Equal(t, "account-1", data.account)

		first, err := data.client()
		require.NoError(t, err)
		second, err := data.client()
		require.NoError(t, err)
		require.Same(t, first, second)
		require.EqualValues(t, 1, calls.Load())
	})

	t.Run("should remember a failure rather than retrying", func(t *testing.T) {
		var calls atomic.Int32
		data := configureProvider(t, &egt{version: "test", client: func() (*http.Client, error) {
			calls.Add(1)
			return nil, errors.New("no key")
		}}, "")

		_, err := data.client()
		require.ErrorContains(t, err, "no key")
		_, err = data.client()
		require.ErrorContains(t, err, "no key")
		require.EqualValues(t, 1, calls.Load())
	})

	t.Run("should report a missing ssh key", func(t *testing.T) {
		data := configureProvider(t, New("test")().(*egt), filepath.Join(t.TempDir(), "eg"))

		_, err := data.client()
		require.ErrorContains(t, err, "unable to read the ssh key")
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("should report a malformed ssh key", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "eg")
		require.NoError(t, os.WriteFile(p, []byte("not a key"), 0600))

		data := configureProvider(t, New("test")().(*egt), p)

		_, err := data.client()
		require.ErrorContains(t, err, "unable to parse the ssh key")
	})
}
