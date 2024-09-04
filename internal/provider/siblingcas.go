package provider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

func UseSHA256OfAttribute(attrname string) planmodifier.String {
	return useSHA256OfAttribute{attrname: attrname}
}

// useSHA256OfAttribute implements the plan modifier.
type useSHA256OfAttribute struct {
	attrname string
}

// Description returns a human-readable description of the plan modifier.
func (m useSHA256OfAttribute) Description(_ context.Context) string {
	return "Once set, the value of this attribute in state will not change."
}

// MarkdownDescription returns a markdown description of the plan modifier.
func (m useSHA256OfAttribute) MarkdownDescription(_ context.Context) string {
	return "Once set, the value of this attribute in state will not change."
}

// PlanModifyString implements the plan modification logic.
func (m useSHA256OfAttribute) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	var (
		b64 types.String
	)
	p := req.Path.ParentPath().AtName(m.attrname)
	req.Plan.GetAttribute(ctx, p, &b64)

	decoded, err := base64.StdEncoding.DecodeString(b64.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diag.NewAttributeErrorDiagnostic(p, "failed to hash base64", err.Error()))
		return
	}

	digest := sha256.Sum256(decoded)
	encoded := hex.EncodeToString(digest[:])
	tflog.Debug(ctx, fmt.Sprintf("sha256 plan: %s %s %s %s -> %s", p.String(), b64.ValueString(), decoded, req.StateValue.ValueString(), encoded))
	resp.PlanValue = basetypes.NewStringValue(encoded)
}
