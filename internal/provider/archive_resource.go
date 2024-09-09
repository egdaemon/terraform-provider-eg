package provider

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"os"
	"time"

	"github.com/egdaemon/egt/internal/errorsx"
	"github.com/egdaemon/egt/internal/iox"
	"github.com/egdaemon/egt/internal/tarx"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// reference resource: https://github.com/hashicorp/terraform-provider-local/blob/main/internal/provider/resource_local_file.go
type SourceModel struct {
	Base64   types.String `tfsdk:"base64"`
	Location types.String `tfsdk:"location"`
	Perm     types.Int32  `tfsdk:"perm"`
	Digest   types.String `tfsdk:"digest"`
}

// ExampleResourceModel describes the resource data model.
type ArchiveResourceModel struct {
	Digest     types.String   `tfsdk:"digest"`
	Sources    []*SourceModel `tfsdk:"source"`
	Timestamp  types.Int64    `tfsdk:"timestamp"`
	ArchiveB64 types.String   `tfsdk:"archiveb64"`
}

func NewTarResource() resource.Resource {
	return &ArchiveResource{}
}

// ArchiveResource defines the resource implementation for a tar archive
type ArchiveResource struct{}

func (r *ArchiveResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tar"
}

func (r *ArchiveResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "creates a tar archive",
		Blocks: map[string]schema.Block{
			"source": schema.ListNestedBlock{
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"base64": schema.StringAttribute{
							Required: true,
						},
						"location": schema.StringAttribute{
							MarkdownDescription: "location to place the file within the archive",
							Required:            true,
						},
						"perm": schema.Int32Attribute{
							MarkdownDescription: "permission bits within the archive for the file, defaults to read/write for the user only",
							Optional:            true,
							// Default:             int32default.StaticInt32(0600),
						},
						"digest": schema.StringAttribute{
							MarkdownDescription: "archive digest used to determine if content has changed",
							Computed:            true,
							Optional:            false,
							Required:            false,
							PlanModifiers: []planmodifier.String{
								UseSHA256OfAttribute("base64"),
							},
						},
					},
				},
			},
		},
		Attributes: map[string]schema.Attribute{
			"timestamp": schema.Int64Attribute{
				MarkdownDescription: "timestamp files should be given within the archive, this value is set on creation and when the digest of the source content changes.",
				Computed:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"digest": schema.StringAttribute{
				MarkdownDescription: "archive digest used to determine if content has changed",
				Computed:            true,
			},
			"archiveb64": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "base64 encoded contents of the archive",
			},
		},
	}
}

func (r *ArchiveResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// Prevent panic if the provider has not been configured.
	if req.ProviderData == nil {
		return
	}
}

func (r *ArchiveResource) generate(ctx context.Context, ts time.Time, dst *os.File, data *ArchiveResourceModel) error {
	var (
		digest = sha256.New()
	)

	b64 := base64.NewEncoder(base64.StdEncoding, dst)
	gw := gzip.NewWriter(b64)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	for _, v := range data.Sources {
		localdigest := sha256.New()
		bstr := v.Base64.ValueString()

		decoded, err := base64.StdEncoding.DecodeString(bstr)
		if err != nil {
			return err
		}
		mode := int32(0600)
		if !v.Perm.IsNull() {
			mode = v.Perm.ValueInt32()
		}

		err = tarx.WriteFileToArchive(tw, tarx.NewHeader(v.Location.ValueString(), ts, int64(len(decoded)), int64(mode)), io.TeeReader(bytes.NewReader(decoded), io.MultiWriter(digest, localdigest)))
		if err != nil {
			return err
		}

		v.Digest = basetypes.NewStringValue(hex.EncodeToString(localdigest.Sum(nil)))
	}

	data.Digest = basetypes.NewStringValue(hex.EncodeToString(digest.Sum(nil)))
	if err := errorsx.Compact(tw.Close(), gw.Close(), b64.Close()); err != nil {
		return err
	}

	encodedstr, err := iox.String(dst)
	if err != nil {
		return err
	}
	// tflog.Info(ctx, fmt.Sprintf("debug encoded archive %s", encodedstr))
	data.ArchiveB64 = basetypes.NewStringValue(encodedstr)

	return nil
}

func (r *ArchiveResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data ArchiveResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	ts := time.Now()
	data.Timestamp = basetypes.NewInt64Value(ts.UnixMilli())

	dst, err := os.CreateTemp("", "egt.archive.*")
	if err != nil {
		panic(err)
	}
	defer dst.Close()

	if err = r.generate(ctx, ts, dst, &data); err != nil {
		panic(err)
	}

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ArchiveResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var (
		data ArchiveResourceModel
	)
	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	ts := time.UnixMilli(data.Timestamp.ValueInt64())
	dst, err := os.CreateTemp("", "egt.archive.*")
	if err != nil {
		panic(err)
	}
	defer dst.Close()

	if err = r.generate(ctx, ts, dst, &data); err != nil {
		panic(err)
	}

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ArchiveResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var (
		data ArchiveResourceModel
	)

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	ts := time.UnixMilli(data.Timestamp.ValueInt64())
	dst, err := os.CreateTemp("", "egt.archive.*")
	if err != nil {
		panic(err)
	}
	defer dst.Close()

	if err = r.generate(ctx, ts, dst, &data); err != nil {
		panic(err)
	}
	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ArchiveResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data ArchiveResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}
}

func (r *ArchiveResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
