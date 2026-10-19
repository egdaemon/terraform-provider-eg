package provider

import (
	"cmp"
	"context"
	"net/http"
	"os"
	"strconv"

	"github.com/egdaemon/eg/authn"
	"github.com/egdaemon/eg/cmd/cmdopts"
	"github.com/egdaemon/eg/compute"
	"github.com/egdaemon/egt/internal/errorsx"
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

	// schedules replaces the eg schedule api, used by tests.
	schedules scheduleAPI
}

// providerModel is the provider block.
type providerModel struct {
	AccountID  types.String `tfsdk:"account_id"`
	SSHKeyPath types.String `tfsdk:"ssh_key_path"`
	Insecure   types.Bool   `tfsdk:"insecure"`
}

// providerData is what resources receive from the provider.
type providerData struct {
	schedules scheduleAPI
}

// Metadata returns the provider type name.
func (p *egt) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "eg"
	resp.Version = p.version
}

// Schema defines the provider-level schema for configuration data.
func (p *egt) Schema(ctx context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "authenticates to the eg api as the profile the ssh key is registered to, the same way the eg cli does.",
		Attributes: map[string]schema.Attribute{
			"account_id": schema.StringAttribute{
				MarkdownDescription: "account to authorize as, required when the ssh key is associated with more than one profile. defaults to the `EG_ACCOUNT` environment variable.",
				Optional:            true,
			},
			"ssh_key_path": schema.StringAttribute{
				MarkdownDescription: "path to the ssh private key registered with eg, read when a resource first calls the api. defaults to the eg cli's key.",
				Optional:            true,
			},
			"insecure": schema.BoolAttribute{
				MarkdownDescription: "allow unsigned (and therefore insecure) tls certificates. defaults to the `EG_COMPUTE_TLS_INSECURE` environment variable.",
				Optional:            true,
			},
		},
	}
}

// Configure prepares the eg api client for data sources and resources. nothing is read or requested here,
// the ssh key is loaded and the token fetched when a resource first makes a request.
func (p *egt) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var (
		config   providerModel
		insecure bool
	)

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.AccountID.IsUnknown() || config.SSHKeyPath.IsUnknown() || config.Insecure.IsUnknown() {
		resp.Diagnostics.AddError("unknown provider configuration", "provider attributes must be known at plan time")
		return
	}

	if p.schedules != nil {
		resp.ResourceData = &providerData{schedules: p.schedules}
		return
	}

	insecure, _ = strconv.ParseBool(os.Getenv("EG_COMPUTE_TLS_INSECURE"))
	if !config.Insecure.IsNull() {
		insecure = config.Insecure.ValueBool()
	}

	// the signer is nil because lazyauthz loads it, so configurations that only use eg_tar do not require a key.
	c := authn.New(
		ctx,
		cmdopts.TLSConfig{Insecure: insecure}.DefaultClient(),
		nil,
		cmp.Or(config.AccountID.ValueString(), os.Getenv("EG_ACCOUNT")),
		lazyauthz(cmp.Or(config.SSHKeyPath.ValueString(), cmdopts.DefaultSSHKeyPath())),
	)
	resp.ResourceData = &providerData{schedules: httpSchedules{c: c}}
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

// lazyauthz builds authzsources that read the ssh key at path when a token is first requested.
func lazyauthz(path string) authn.TokenAuthz[authzsource] {
	return func(c *http.Client, _ ssh.Signer, endpoint string, account string) authzsource {
		return authzsource{c: c, path: path, endpoint: endpoint, account: account}
	}
}

// authzsource exchanges the ssh key at path for an authorization token from endpoint, see compute.NewAuthzTokenSource.
type authzsource struct {
	c        *http.Client
	path     string
	endpoint string
	account  string
}

func (t authzsource) Token() (_ *oauth2.Token, err error) {
	var (
		encoded []byte
		signer  ssh.Signer
	)

	if encoded, err = os.ReadFile(t.path); err != nil {
		return nil, errorsx.Wrap(err, "unable to read the ssh key, register one with the eg cli first")
	}

	if signer, err = ssh.ParsePrivateKey(encoded); err != nil {
		return nil, errorsx.Wrapf(err, "unable to parse the ssh key %s", t.path)
	}

	return compute.NewAuthzTokenSource(t.c, signer, t.endpoint, t.account).Token()
}
