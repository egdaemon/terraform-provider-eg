package provider

import (
	"context"
	"fmt"

	"github.com/egdaemon/eg/awswif"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &AWSIdentityFederationResource{}
	_ resource.ResourceWithConfigure   = &AWSIdentityFederationResource{}
	_ resource.ResourceWithImportState = &AWSIdentityFederationResource{}
)

// AWSIdentityFederationResourceModel describes the resource data model.
type AWSIdentityFederationResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Label       types.String `tfsdk:"label"`
	RoleARN     types.String `tfsdk:"role_arn"`
	Region      types.String `tfsdk:"region"`
	Audience    types.String `tfsdk:"audience"`
	Description types.String `tfsdk:"description"`
}

func NewAWSIdentityFederationResource() resource.Resource {
	return &AWSIdentityFederationResource{}
}

// AWSIdentityFederationResource an aws web identity federation connection, it lets eg runners
// present egdaemon's identity to the aws account the role belongs to.
type AWSIdentityFederationResource struct {
	data *providerData
}

func (r *AWSIdentityFederationResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_aws_identity_federation"
}

func (r *AWSIdentityFederationResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "registers an aws iam role with eg so workloads can assume it through web identity federation. managing connections requires the user management permission.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "the label of the connection.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"label": schema.StringAttribute{
				MarkdownDescription: "identifies the connection within the account, defaults to the id of the account. changing it replaces the connection.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"role_arn": schema.StringAttribute{
				MarkdownDescription: "the arn of the iam role workloads assume: `arn:aws:iam::<account>:role/<name>`.",
				Required:            true,
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "the default region of the workloads.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(""),
			},
			"audience": schema.StringAttribute{
				MarkdownDescription: "the audience of the identity tokens, it must match the client id of the iam identity provider.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("sts.amazonaws.com"),
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "free form description of the connection.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(""),
			},
		},
	}
}

func (r *AWSIdentityFederationResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *AWSIdentityFederationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan AWSIdentityFederationResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// the label defaults to the account id. when the account is unknown here the api applies the default,
	// but then an existing connection can't be detected before it is overwritten.
	c, err := r.data.meta()
	if err != nil {
		resp.Diagnostics.AddError("unable to authorize", err.Error())
		return
	}

	label := plan.Label.ValueString()
	if label == "" {
		label = r.data.account
	}

	if label != "" {
		if _, err := awswif.Find(ctx, c, label); err == nil {
			resp.Diagnostics.AddError(
				"connection already exists",
				fmt.Sprintf("a connection labeled %q already exists, import it with `terraform import` or choose another label", label),
			)
			return
		} else if !awswif.NotFound(err) {
			resp.Diagnostics.AddError("unable to check for an existing connection", err.Error())
			return
		}
	}

	conn, err := awswif.Create(ctx, c, &awswif.AWSWIFConnection{
		Label:       label,
		RoleArn:     plan.RoleARN.ValueString(),
		Region:      plan.Region.ValueString(),
		Audience:    plan.Audience.ValueString(),
		Description: plan.Description.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("unable to create the connection", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, awsModelFromConnection(conn))...)
}

func (r *AWSIdentityFederationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state AWSIdentityFederationResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	c, err := r.data.meta()
	if err != nil {
		resp.Diagnostics.AddError("unable to authorize", err.Error())
		return
	}

	conn, err := awswif.Find(ctx, c, state.Label.ValueString())
	if awswif.NotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	} else if err != nil {
		resp.Diagnostics.AddError("unable to read the connection", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, awsModelFromConnection(conn))...)
}

func (r *AWSIdentityFederationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state AWSIdentityFederationResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	c, err := r.data.meta()
	if err != nil {
		resp.Diagnostics.AddError("unable to authorize", err.Error())
		return
	}

	// every field is sent, an empty string clears it.
	rolearn, region, audience, description := plan.RoleARN.ValueString(), plan.Region.ValueString(), plan.Audience.ValueString(), plan.Description.ValueString()
	conn, err := awswif.Update(ctx, c, state.Label.ValueString(), &awswif.AWSWIFUpdateRequest{
		RoleArn:     &rolearn,
		Region:      &region,
		Audience:    &audience,
		Description: &description,
	})
	if err != nil {
		resp.Diagnostics.AddError("unable to update the connection", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, awsModelFromConnection(conn))...)
}

func (r *AWSIdentityFederationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state AWSIdentityFederationResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	c, err := r.data.meta()
	if err != nil {
		resp.Diagnostics.AddError("unable to authorize", err.Error())
		return
	}

	if err = awswif.Remove(ctx, c, state.Label.ValueString()); err != nil && !awswif.NotFound(err) {
		resp.Diagnostics.AddError("unable to remove the connection", err.Error())
	}
}

func (r *AWSIdentityFederationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// import by label, the following read fills in the remaining attributes.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("label"), req.ID)...)
}

func awsModelFromConnection(conn *awswif.AWSWIFConnection) *AWSIdentityFederationResourceModel {
	return &AWSIdentityFederationResourceModel{
		ID:          types.StringValue(conn.GetLabel()),
		Label:       types.StringValue(conn.GetLabel()),
		RoleARN:     types.StringValue(conn.GetRoleArn()),
		Region:      types.StringValue(conn.GetRegion()),
		Audience:    types.StringValue(conn.GetAudience()),
		Description: types.StringValue(conn.GetDescription()),
	}
}
