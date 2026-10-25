package provider

import (
	"context"
	"fmt"

	"github.com/egdaemon/eg/awswif"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = &AWSOIDCDataSource{}
	_ datasource.DataSourceWithConfigure = &AWSOIDCDataSource{}
)

// AWSOIDCDataSourceModel describes the data source data model.
type AWSOIDCDataSourceModel struct {
	ID        types.String `tfsdk:"id"`
	IssuerURL types.String `tfsdk:"issuer_url"`
	Subject   types.String `tfsdk:"subject"`
}

func NewAWSOIDCDataSource() datasource.DataSource {
	return &AWSOIDCDataSource{}
}

// AWSOIDCDataSource the issuer and subject an aws account trusts to accept the workloads of the account.
type AWSOIDCDataSource struct {
	data *providerData
}

func (r *AWSOIDCDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_aws_oidc"
}

func (r *AWSOIDCDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "the identity provider and subject an aws account must trust to let the workloads of the eg account assume its roles. requires the user management permission.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "the subject.",
				Computed:            true,
			},
			"issuer_url": schema.StringAttribute{
				MarkdownDescription: "the url of the issuer, the url of the `aws_iam_openid_connect_provider`.",
				Computed:            true,
			},
			"subject": schema.StringAttribute{
				MarkdownDescription: "the subject of the identity tokens of the account, pin it in the trust policy of the role with the `sub` condition of the issuer.",
				Computed:            true,
			},
		},
	}
}

func (r *AWSOIDCDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	// nil while the provider is validated.
	if req.ProviderData == nil {
		return
	}

	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("unexpected provider data", fmt.Sprintf("expected *providerData, got %T", req.ProviderData))
		return
	}

	r.data = data
}

func (r *AWSOIDCDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	c, err := r.data.meta()
	if err != nil {
		resp.Diagnostics.AddError("unable to authorize", err.Error())
		return
	}

	issuer, err := awswif.Issuer(ctx, c)
	if err != nil {
		resp.Diagnostics.AddError("unable to read the issuer", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &AWSOIDCDataSourceModel{
		ID:        types.StringValue(issuer.GetSubject()),
		IssuerURL: types.StringValue(issuer.GetIssuerUrl()),
		Subject:   types.StringValue(issuer.GetSubject()),
	})...)
}
