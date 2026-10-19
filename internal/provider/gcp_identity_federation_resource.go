package provider

import (
	"context"
	"fmt"

	"github.com/egdaemon/eg/gcpwif"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &GCPIdentityFederationResource{}
	_ resource.ResourceWithConfigure   = &GCPIdentityFederationResource{}
	_ resource.ResourceWithImportState = &GCPIdentityFederationResource{}
)

// GCPIdentityFederationResourceModel describes the resource data model.
type GCPIdentityFederationResourceModel struct {
	ID                        types.String `tfsdk:"id"`
	Label                     types.String `tfsdk:"label"`
	Audience                  types.String `tfsdk:"audience"`
	ImpersonateServiceAccount types.String `tfsdk:"impersonate_service_account"`
	Description               types.String `tfsdk:"description"`
}

func NewGCPIdentityFederationResource() resource.Resource {
	return &GCPIdentityFederationResource{}
}

// GCPIdentityFederationResource a gcp workload identity federation connection, it lets eg runners
// present egdaemon's identity to the gcp project the audience belongs to.
type GCPIdentityFederationResource struct {
	data *providerData
}

func (r *GCPIdentityFederationResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gcp_identity_federation"
}

func (r *GCPIdentityFederationResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "registers a gcp workload identity federation provider with eg so workloads can access the gcp project it belongs to. managing connections requires the user management permission.",
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
			"audience": schema.StringAttribute{
				MarkdownDescription: "the full resource name of the workload identity pool provider: `//iam.googleapis.com/projects/<number>/locations/global/workloadIdentityPools/<pool>/providers/<provider>`.",
				Required:            true,
			},
			"impersonate_service_account": schema.StringAttribute{
				MarkdownDescription: "email of the service account to impersonate after the token exchange. when empty workloads act as the federated pool principal.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(""),
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

func (r *GCPIdentityFederationResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *GCPIdentityFederationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan GCPIdentityFederationResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// the label defaults to the account id. when the account is unknown here the api applies the default,
	// but then an existing connection can't be detected before it is overwritten.
	c, err := r.data.client()
	if err != nil {
		resp.Diagnostics.AddError("unable to authorize", err.Error())
		return
	}

	label := plan.Label.ValueString()
	if label == "" {
		label = r.data.account
	}

	if label != "" {
		if _, err := gcpwif.Find(ctx, c, label); err == nil {
			resp.Diagnostics.AddError(
				"connection already exists",
				fmt.Sprintf("a connection labeled %q already exists, import it with `terraform import` or choose another label", label),
			)
			return
		} else if !gcpwif.NotFound(err) {
			resp.Diagnostics.AddError("unable to check for an existing connection", err.Error())
			return
		}
	}

	conn, err := gcpwif.Create(ctx, c, &gcpwif.GCPWIFConnection{
		Label:                     label,
		Audience:                  plan.Audience.ValueString(),
		ImpersonateServiceAccount: plan.ImpersonateServiceAccount.ValueString(),
		Description:               plan.Description.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("unable to create the connection", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, modelFromConnection(conn))...)
}

func (r *GCPIdentityFederationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state GCPIdentityFederationResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	c, err := r.data.client()
	if err != nil {
		resp.Diagnostics.AddError("unable to authorize", err.Error())
		return
	}

	conn, err := gcpwif.Find(ctx, c, state.Label.ValueString())
	if gcpwif.NotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	} else if err != nil {
		resp.Diagnostics.AddError("unable to read the connection", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, modelFromConnection(conn))...)
}

func (r *GCPIdentityFederationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state GCPIdentityFederationResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	c, err := r.data.client()
	if err != nil {
		resp.Diagnostics.AddError("unable to authorize", err.Error())
		return
	}

	// every field is sent, an empty string clears it.
	audience, impersonate, description := plan.Audience.ValueString(), plan.ImpersonateServiceAccount.ValueString(), plan.Description.ValueString()
	conn, err := gcpwif.Update(ctx, c, state.Label.ValueString(), &gcpwif.GCPWIFUpdateRequest{
		Audience:                  &audience,
		ImpersonateServiceAccount: &impersonate,
		Description:               &description,
	})
	if err != nil {
		resp.Diagnostics.AddError("unable to update the connection", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, modelFromConnection(conn))...)
}

func (r *GCPIdentityFederationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state GCPIdentityFederationResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	c, err := r.data.client()
	if err != nil {
		resp.Diagnostics.AddError("unable to authorize", err.Error())
		return
	}

	if err = gcpwif.Remove(ctx, c, state.Label.ValueString()); err != nil && !gcpwif.NotFound(err) {
		resp.Diagnostics.AddError("unable to remove the connection", err.Error())
	}
}

func (r *GCPIdentityFederationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// import by label, the following read fills in the remaining attributes.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("label"), req.ID)...)
}

func modelFromConnection(conn *gcpwif.GCPWIFConnection) *GCPIdentityFederationResourceModel {
	return &GCPIdentityFederationResourceModel{
		ID:                        types.StringValue(conn.GetLabel()),
		Label:                     types.StringValue(conn.GetLabel()),
		Audience:                  types.StringValue(conn.GetAudience()),
		ImpersonateServiceAccount: types.StringValue(conn.GetImpersonateServiceAccount()),
		Description:               types.StringValue(conn.GetDescription()),
	}
}
