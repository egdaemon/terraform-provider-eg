package provider

import (
	"cmp"
	"context"
	"net/http"
	"os"
	"path/filepath"

	"github.com/egdaemon/eg"
	"github.com/egdaemon/eg/authn"
	"github.com/egdaemon/eg/compute"
	"github.com/egdaemon/egt/internal/errorsx"
	"github.com/egdaemon/egt/internal/repository"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"golang.org/x/crypto/ssh"
	"golang.org/x/oauth2"
)

// New is a helper function to simplify provider server and testing implementation.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &egt{
			version: version,
		}
	}
}

// egt is the provider implementation.
type egt struct {
	// version is set to the provider version on release, "dev" when the
	// provider is built and ran locally, and "test" when running acceptance
	// testing.
	version string
}

type egtModel struct {
	AccountID  types.String `tfsdk:"account_id"`
	SSHKeyPath types.String `tfsdk:"ssh_key_path"`
}

// Metadata returns the provider type name.
func (p *egt) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "eg"
	resp.Version = p.version
}

// Schema defines the provider-level schema for configuration data.
func (p *egt) Schema(ctx context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"account_id": schema.StringAttribute{
				MarkdownDescription: "account to authenticate as, disambiguates when the ssh key is associated with multiple profiles. defaults to the `EG_ACCOUNT` environment variable.",
				Optional:            true,
			},
			"ssh_key_path": schema.StringAttribute{
				MarkdownDescription: "path to the ssh key registered with eg, defaults to the key the eg cli uses: `$CONFIGURATION_DIRECTORY/eg` or `~/.ssh/eg`.",
				Optional:            true,
			},
		},
	}
}

// Configure prepares the eg api client for data sources and resources.
func (p *egt) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var (
		data egtModel
		home string
		err  error
	)

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if home, err = os.UserHomeDir(); err != nil {
		resp.Diagnostics.AddError("unable to determine home directory", err.Error())
		return
	}

	src := authzsource{
		account: cmp.Or(data.AccountID.ValueString(), os.Getenv("EG_ACCOUNT")),
		path:    cmp.Or(data.SSHKeyPath.ValueString(), filepath.Join(cmp.Or(os.Getenv("CONFIGURATION_DIRECTORY"), filepath.Join(home, ".ssh")), "eg")),
	}

	resp.ResourceData = repository.NewScheduleClient(oauth2.NewClient(context.WithValue(ctx, oauth2.HTTPClient, http.DefaultClient), src), eg.EnvAPIHostDefault())
}

// DataSources defines the data sources implemented in the provider.
func (p *egt) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}

// Resources defines the resources implemented in the provider.
func (p *egt) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewTarResource,
		NewScheduledWorkloadResource,
	}
}

// authzsource loads the ssh key when a token is first requested so configurations
// that only use eg_tar do not require one.
type authzsource struct {
	account string
	path    string
}

func (t authzsource) Token() (_ *oauth2.Token, err error) {
	var (
		encoded []byte
		signer  ssh.Signer
	)

	if encoded, err = os.ReadFile(t.path); err != nil {
		return nil, errorsx.Wrap(err, "unable to read ssh key, register one with `eg register`")
	}

	if signer, err = ssh.ParsePrivateKey(encoded); err != nil {
		return nil, errorsx.Wrapf(err, "unable to parse ssh key %s", t.path)
	}

	return compute.NewAuthzTokenSource(http.DefaultClient, signer, authn.EndpointCompute(), t.account).Token()
}
