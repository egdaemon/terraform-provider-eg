package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/stretchr/testify/require"
)

func TestEgtConfigure(t *testing.T) {
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
}

func TestAuthzsourceToken(t *testing.T) {
	t.Run("should report a missing ssh key", func(t *testing.T) {
		_, err := authzsource{path: filepath.Join(t.TempDir(), "eg")}.Token()
		require.ErrorContains(t, err, "unable to read the ssh key")
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("should report a malformed ssh key", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "eg")
		require.NoError(t, os.WriteFile(p, []byte("not a key"), 0600))

		_, err := authzsource{path: p}.Token()
		require.ErrorContains(t, err, "unable to parse the ssh key")
	})
}
