package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/egdaemon/eg/schedules"
	"github.com/hashicorp/terraform-plugin-framework-timetypes/timetypes"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ScheduledWorkloadResourceModel describes the resource data model.
type ScheduledWorkloadResourceModel struct {
	ID          types.String         `tfsdk:"id"`
	Repository  types.String         `tfsdk:"repository"`
	Workload    types.String         `tfsdk:"workload"`
	Schedule    types.String         `tfsdk:"schedule"`
	Description types.String         `tfsdk:"description"`
	Timeout     timetypes.GoDuration `tfsdk:"timeout"`
	CPU         types.Int64          `tfsdk:"cpu"`
	Memory      types.String         `tfsdk:"memory"`
	Vram        types.String         `tfsdk:"vram"`
	Arch        types.String         `tfsdk:"arch"`
	OS          types.String         `tfsdk:"os"`
	Labels      []types.String       `tfsdk:"labels"`
	SecretURIs  []types.String       `tfsdk:"secret_uris"`
	AccountID   types.String         `tfsdk:"account_id"`
	CreatedAt   types.String         `tfsdk:"created_at"`
}

// schedule converts the model into the control plane's representation.
func (t *ScheduledWorkloadResourceModel) schedule() (_ *schedules.Schedule, err error) {
	var (
		ttl     time.Duration
		diags   diag.Diagnostics
		memory  uint64
		vram    uint64
		labels  = make([]string, 0, len(t.Labels))
		secrets = make([]string, 0, len(t.SecretURIs))
	)

	if ttl, diags = t.Timeout.ValueGoDuration(); diags.HasError() {
		return nil, errors.New(diags.Errors()[0].Detail())
	}

	if memory, err = bytesof(t.Memory); err != nil {
		return nil, err
	}

	if vram, err = bytesof(t.Vram); err != nil {
		return nil, err
	}

	for _, l := range t.Labels {
		labels = append(labels, l.ValueString())
	}

	for _, u := range t.SecretURIs {
		secrets = append(secrets, u.ValueString())
	}

	return &schedules.Schedule{
		Id:          t.ID.ValueString(),
		AccountId:   t.AccountID.ValueString(),
		CreatedAt:   t.CreatedAt.ValueString(),
		VcsUri:      t.Repository.ValueString(),
		Entry:       t.Workload.ValueString(),
		Cron:        t.Schedule.ValueString(),
		Description: t.Description.ValueString(),
		Ttl:         uint64(ttl),
		Cores:       uint64(t.CPU.ValueInt64()),
		Memory:      memory,
		Vram:        vram,
		Arch:        t.Arch.ValueString(),
		Os:          t.OS.ValueString(),
		Labels:      labels,
		SecretUris:  secrets,
	}, nil
}

// refresh updates the model from the control plane's representation, preserving the configured
// representation of durations and sizes when they are equivalent.
func (t *ScheduledWorkloadResourceModel) refresh(s *schedules.Schedule) {
	t.ID = types.StringValue(s.Id)
	t.AccountID = types.StringValue(s.AccountId)
	t.CreatedAt = types.StringValue(s.CreatedAt)
	t.Repository = types.StringValue(s.VcsUri)
	t.Workload = types.StringValue(s.Entry)
	t.Schedule = types.StringValue(s.Cron)
	t.Description = types.StringValue(s.Description)
	t.Timeout = timetypes.NewGoDurationValue(time.Duration(s.Ttl))
	t.CPU = types.Int64Value(int64(s.Cores))
	t.Memory = sizeof(t.Memory, s.Memory)
	t.Vram = sizeof(t.Vram, s.Vram)
	t.Arch = types.StringValue(s.Arch)
	t.OS = types.StringValue(s.Os)

	t.Labels = stringlist(t.Labels, s.Labels)
	t.SecretURIs = stringlist(t.SecretURIs, s.SecretUris)
}

var (
	_ resource.Resource                = &ScheduledWorkloadResource{}
	_ resource.ResourceWithConfigure   = &ScheduledWorkloadResource{}
	_ resource.ResourceWithImportState = &ScheduledWorkloadResource{}
)

func NewScheduledWorkloadResource() resource.Resource {
	return &ScheduledWorkloadResource{}
}

// ScheduledWorkloadResource defines the resource implementation for a workload the control plane runs on a schedule.
type ScheduledWorkloadResource struct {
	data *providerData
}

func (r *ScheduledWorkloadResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_repository_scheduled_workload"
}

func (r *ScheduledWorkloadResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "runs a repository's workload on a schedule",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"repository": schema.StringAttribute{
				MarkdownDescription: "canonical vcs uri of the repository, e.g. `git@github.com:egdaemon/eg.git`",
				Required:            true,
			},
			"workload": schema.StringAttribute{
				MarkdownDescription: "workload module to run within the repository, e.g. `tests/stress`",
				Required:            true,
			},
			"schedule": schema.StringAttribute{
				MarkdownDescription: "[fron](https://github.com/egdaemon/fron) schedule to run the workload on, e.g. `1d`, `6h`, `1d @ on weekdays`. pins are ignored, each schedule runs at a stable slot within its frequency.",
				Required:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "human readable description of the scheduled workload",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"timeout": schema.StringAttribute{
				MarkdownDescription: "maximum runtime of the workload as a duration like `30m` or `1h30m`, defaults to `1h`",
				CustomType:          timetypes.GoDurationType{},
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("1h"),
			},
			"cpu": schema.Int64Attribute{
				MarkdownDescription: "number of vCPU to make available",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"memory": schema.StringAttribute{
				MarkdownDescription: "amount of RAM to make available, e.g. `1g`, `512MiB`",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				Validators: []validator.String{
					parses{description: "invalid size", parse: sizeparse},
				},
			},
			"vram": schema.StringAttribute{
				MarkdownDescription: "amount of GPU memory to make available, e.g. `8g`",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				Validators: []validator.String{
					parses{description: "invalid size", parse: sizeparse},
				},
			},
			"arch": schema.StringAttribute{
				MarkdownDescription: "native CPU architecture of the machine, e.g. `amd64`",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"os": schema.StringAttribute{
				MarkdownDescription: "operating system of the machine, e.g. `linux`",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"labels": schema.ListAttribute{
				MarkdownDescription: "labels a runner must have to run the workload",
				ElementType:         types.StringType,
				Optional:            true,
			},
			"secret_uris": schema.ListAttribute{
				MarkdownDescription: "references to the secrets made available to the workload, e.g. `gcpsm://project-id/secret-name`. these are references, the secrets are resolved by the runner",
				ElementType:         types.StringType,
				Optional:            true,
			},
			"account_id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *ScheduledWorkloadResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	var (
		ok bool
	)

	// nil while the provider is validated.
	if req.ProviderData == nil {
		return
	}

	if r.data, ok = req.ProviderData.(*providerData); !ok {
		resp.Diagnostics.AddError("unexpected provider data", fmt.Sprintf("expected *providerData, got %T", req.ProviderData))
	}
}

func (r *ScheduledWorkloadResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var (
		data    ScheduledWorkloadResourceModel
		sched   *schedules.Schedule
		created *schedules.Schedule
		c       *http.Client
		err     error
	)

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if c, err = r.data.compute(); err != nil {
		resp.Diagnostics.AddError("unable to authorize", err.Error())
		return
	}

	if sched, err = data.schedule(); err != nil {
		resp.Diagnostics.AddError("invalid scheduled workload", err.Error())
		return
	}

	if created, err = schedules.Create(ctx, c, sched); err != nil {
		resp.Diagnostics.AddError("unable to create scheduled workload", err.Error())
		return
	}

	data.refresh(created)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ScheduledWorkloadResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var (
		data  ScheduledWorkloadResourceModel
		found *schedules.Schedule
		c     *http.Client
		err   error
	)

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if c, err = r.data.compute(); err != nil {
		resp.Diagnostics.AddError("unable to authorize", err.Error())
		return
	}

	if found, err = schedules.Find(ctx, c, data.Repository.ValueString(), data.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("unable to read scheduled workload", err.Error())
		return
	}

	if found == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	data.refresh(found)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ScheduledWorkloadResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var (
		data    ScheduledWorkloadResourceModel
		sched   *schedules.Schedule
		updated *schedules.Schedule
		c       *http.Client
		err     error
	)

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if c, err = r.data.compute(); err != nil {
		resp.Diagnostics.AddError("unable to authorize", err.Error())
		return
	}

	if sched, err = data.schedule(); err != nil {
		resp.Diagnostics.AddError("invalid scheduled workload", err.Error())
		return
	}

	if updated, err = schedules.Update(ctx, c, sched.Id, sched); err != nil {
		resp.Diagnostics.AddError("unable to update scheduled workload", err.Error())
		return
	}

	data.refresh(updated)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ScheduledWorkloadResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var (
		data ScheduledWorkloadResourceModel
		c    *http.Client
		err  error
	)

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if c, err = r.data.compute(); err != nil {
		resp.Diagnostics.AddError("unable to authorize", err.Error())
		return
	}

	if err = schedules.Remove(ctx, c, data.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("unable to delete scheduled workload", err.Error())
	}
}

func (r *ScheduledWorkloadResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// stringlist the control plane's values as a list attribute. an omitted attribute stays null when the control
// plane reports no values.
func stringlist(prior []types.String, values []string) []types.String {
	if len(values) == 0 && prior == nil {
		return nil
	}

	l := make([]types.String, 0, len(values))
	for _, v := range values {
		l = append(l, types.StringValue(v))
	}

	return l
}

func sizeparse(s string) (err error) {
	_, err = humanize.ParseBytes(s)
	return err
}

// bytesof parses a human readable size, unset sizes are left for the control plane to default.
func bytesof(v types.String) (uint64, error) {
	if v.ValueString() == "" {
		return 0, nil
	}

	return humanize.ParseBytes(v.ValueString())
}

// sizeof preserves the prior representation of a size when it matches the bytes the control plane reports.
func sizeof(prior types.String, n uint64) types.String {
	var (
		v   uint64
		err error
	)

	if v, err = humanize.ParseBytes(prior.ValueString()); err == nil && v == n {
		return prior
	}

	return types.StringValue(humanize.IBytes(n))
}

// parses validates a string attribute by attempting to parse it.
type parses struct {
	description string
	parse       func(string) error
}

func (t parses) Description(context.Context) string {
	return t.description
}

func (t parses) MarkdownDescription(context.Context) string {
	return t.description
}

func (t parses) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	var (
		err error
	)

	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	if err = t.parse(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, t.description, err.Error())
	}
}
