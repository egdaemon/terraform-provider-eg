package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/egdaemon/eg/authn"
	"github.com/egdaemon/eg/cmd/cmdopts"
	"github.com/egdaemon/eg/compute"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"golang.org/x/crypto/ssh"
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

	// client replaces building the eg api client from the ssh key, used by tests. it receives the
	// authorization endpoint the client is for.
	client func(endpoint string) (*http.Client, error)
}

// providerModel is the provider block.
type providerModel struct {
	AccountID  types.String `tfsdk:"account_id"`
	SSHKeyPath types.String `tfsdk:"ssh_key_path"`
	Insecure   types.Bool   `tfsdk:"insecure"`
}

// providerData is what resources receive from the provider.
type providerData struct {
	// account the account id configured on the provider, may be empty.
	account string
	// meta the http client authorized for the account management routes (/m/), e.g. eg_gcp_identity_federation.
	// the ssh key is not read, nor the client built, until the first call, and then only once.
	meta func() (*http.Client, error)
	// compute the http client authorized for the repository and compute routes (/r/, /c/), e.g.
	// eg_repository_scheduled_workload. the routes check the compute authorization token, the meta token is
	// rejected with a 401.
	compute func() (*http.Client, error)
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
				MarkdownDescription: "account to authorize as, required when the ssh key is associated with more than one profile. defaults to the `EG_ACCOUNT` environment variable. also the default label of `eg_gcp_identity_federation` connections.",
				Optional:            true,
			},
			"ssh_key_path": schema.StringAttribute{
				MarkdownDescription: "path to the ssh private key registered with eg, the key must already exist. it is read when a resource first needs the eg api, not when the provider is configured. defaults to the eg cli's key.",
				Optional:            true,
			},
			"insecure": schema.BoolAttribute{
				MarkdownDescription: "allow unsigned (and therefore insecure) tls certificates. defaults to the `EG_COMPUTE_TLS_INSECURE` environment variable.",
				Optional:            true,
			},
		},
	}
}

// Configure prepares the eg api client for data sources and resources. nothing is read or requested here:
// the ssh key is loaded when a resource first needs the client, and the token when it first makes a request.
func (p *egt) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.AccountID.IsUnknown() || config.SSHKeyPath.IsUnknown() || config.Insecure.IsUnknown() {
		resp.Diagnostics.AddError("unknown provider configuration", "provider attributes must be known at plan time")
		return
	}

	account := config.AccountID.ValueString()
	if account == "" {
		account = os.Getenv("EG_ACCOUNT")
	}

	if p.client != nil {
		resp.ResourceData = &providerData{
			account: account,
			meta:    sync.OnceValues(func() (*http.Client, error) { return p.client(authn.EndpointMetaAuthz()) }),
			compute: sync.OnceValues(func() (*http.Client, error) { return p.client(authn.EndpointCompute()) }),
		}
		resp.DataSourceData = resp.ResourceData
		return
	}

	// the request context ends with the request, the client outlives it.
	ctx = context.WithoutCancel(ctx)

	// the key is read once and shared by the clients.
	signer := sync.OnceValues(func() (ssh.Signer, error) {
		keypath := config.SSHKeyPath.ValueString()
		if keypath == "" {
			keypath = cmdopts.DefaultSSHKeyPath()
		}

		encoded, err := os.ReadFile(keypath)
		if err != nil {
			return nil, fmt.Errorf("unable to read the ssh key, register one with the eg cli first: %w", err)
		}

		signer, err := ssh.ParsePrivateKey(encoded)
		if err != nil {
			return nil, fmt.Errorf("unable to parse the ssh key %s: %w", keypath, err)
		}

		return signer, nil
	})

	insecure, _ := strconv.ParseBool(os.Getenv("EG_COMPUTE_TLS_INSECURE"))
	if !config.Insecure.IsNull() {
		insecure = config.Insecure.ValueBool()
	}

	client := func(endpoint string) func() (*http.Client, error) {
		return sync.OnceValues(func() (*http.Client, error) {
			s, err := signer()
			if err != nil {
				return nil, err
			}

			return authn.New(ctx, cmdopts.TLSConfig{Insecure: insecure}.DefaultClient(), s, account, compute.NewAuthzTokenSource, authn.OptionEndpoint(endpoint)), nil
		})
	}

	resp.ResourceData = &providerData{
		account: account,
		meta:    client(authn.EndpointMetaAuthz()),
		compute: client(authn.EndpointCompute()),
	}
	resp.DataSourceData = resp.ResourceData
}

// DataSources defines the data sources implemented in the provider.
func (p *egt) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewAWSOIDCDataSource,
	}
}

// Resources defines the resources implemented in the provider.
func (p *egt) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewTarResource,
		NewGCPIdentityFederationResource,
		NewAWSIdentityFederationResource,
		NewScheduledWorkloadResource,
	}
}
