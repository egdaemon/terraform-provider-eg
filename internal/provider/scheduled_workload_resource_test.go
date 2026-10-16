package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/egdaemon/egt/internal/repository"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"
)

func TestScheduledWorkloadResource(t *testing.T) {
	// controlplane is an in memory implementation of the /r/schedule api.
	type controlplane struct {
		*httptest.Server
		m         sync.Mutex
		schedules map[string]*repository.Schedule
		updated   *repository.Schedule
	}

	fake := func(t *testing.T) *controlplane {
		var (
			mux = http.NewServeMux()
			cp  = &controlplane{schedules: map[string]*repository.Schedule{}}
		)

		mux.HandleFunc("GET /r/schedule/{$}", func(w http.ResponseWriter, r *http.Request) {
			var (
				resp = repository.ScheduleSearchResponse{Next: &repository.ScheduleSearchRequest{Limit: 100}}
			)

			cp.m.Lock()
			defer cp.m.Unlock()

			for _, s := range cp.schedules {
				if v := r.URL.Query().Get("vcs_uri"); v == "" || v == s.VcsUri {
					resp.Items = append(resp.Items, s)
				}
			}

			require.NoError(t, json.NewEncoder(w).Encode(&resp))
		})

		mux.HandleFunc("POST /r/schedule/{$}", func(w http.ResponseWriter, r *http.Request) {
			var (
				req repository.ScheduleCreateRequest
			)

			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

			cp.m.Lock()
			defer cp.m.Unlock()

			req.Schedule.Id = fmt.Sprintf("s%d", len(cp.schedules)+1)
			req.Schedule.AccountId = "a1"
			req.Schedule.CreatedAt = "2026-10-15T00:00:00Z"
			req.Schedule.UpdatedAt = "2026-10-15T00:00:00Z"
			req.Schedule.LastrunAt = "2026-10-16T00:00:00Z"
			req.Schedule.NextrunAt = "2026-10-17T00:00:00Z"
			req.Schedule.Arch = "amd64"
			req.Schedule.Os = "linux"
			cp.schedules[req.Schedule.Id] = req.Schedule

			require.NoError(t, json.NewEncoder(w).Encode(&repository.ScheduleCreateResponse{Schedule: req.Schedule}))
		})

		mux.HandleFunc("PATCH /r/schedule/{id}", func(w http.ResponseWriter, r *http.Request) {
			var (
				req repository.ScheduleUpdateRequest
			)

			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

			cp.m.Lock()
			defer cp.m.Unlock()

			cp.updated = req.Schedule
			req.Schedule.Id = r.PathValue("id")
			req.Schedule.UpdatedAt = "2026-10-16T12:00:00Z"
			req.Schedule.NextrunAt = "2026-10-16T18:00:00Z"
			cp.schedules[req.Schedule.Id] = req.Schedule

			require.NoError(t, json.NewEncoder(w).Encode(&repository.ScheduleUpdateResponse{Schedule: req.Schedule}))
		})

		mux.HandleFunc("DELETE /r/schedule/{id}", func(w http.ResponseWriter, r *http.Request) {
			cp.m.Lock()
			defer cp.m.Unlock()

			s := cp.schedules[r.PathValue("id")]
			delete(cp.schedules, r.PathValue("id"))

			require.NoError(t, json.NewEncoder(w).Encode(&repository.ScheduleDestroyResponse{Schedule: s}))
		})

		cp.Server = httptest.NewServer(mux)
		t.Cleanup(cp.Close)
		return cp
	}

	factories := func(cp *controlplane) map[string]func() (tfprotov6.ProviderServer, error) {
		return map[string]func() (tfprotov6.ProviderServer, error){
			"eg": providerserver.NewProtocol6WithError(&configured{client: repository.NewScheduleClient(cp.Client(), cp.URL)}),
		}
	}

	config := func(cron string, extra string) string {
		return fmt.Sprintf(`
resource "eg_repository_scheduled_workload" "default" {
  repository = "git@github.com:egdaemon/eg.git"
  entry      = "tests/stress"
  cron       = %q
  memory     = "1g"
  cpu        = 8
  %s
}
`, cron, extra)
	}

	t.Run("manages the lifecycle of a scheduled workload", func(t *testing.T) {
		cp := fake(t)

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: factories(cp),
			CheckDestroy: func(*terraform.State) error {
				require.Empty(t, cp.schedules)
				return nil
			},
			Steps: []resource.TestStep{
				{
					Config: config("1d", `labels = ["gpu"]`),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.default", "id", "s1"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.default", "account_id", "a1"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.default", "cron", "1d"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.default", "timeout", "1h"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.default", "memory", "1g"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.default", "cpu", "8"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.default", "arch", "amd64"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.default", "labels.#", "1"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.default", "labels.0", "gpu"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.default", "nextrun_at", "2026-10-17T00:00:00Z"),
						func(*terraform.State) error {
							require.Equal(t, uint64(time.Hour.Milliseconds()), cp.schedules["s1"].Ttl)
							require.Equal(t, uint64(1000000000), cp.schedules["s1"].Memory)
							return nil
						},
					),
				},
				{
					Config: config("6h", `timeout = "2h"`),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.default", "id", "s1"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.default", "cron", "6h"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.default", "timeout", "2h"),
						resource.TestCheckNoResourceAttr("eg_repository_scheduled_workload.default", "labels"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.default", "nextrun_at", "2026-10-16T18:00:00Z"),
						func(*terraform.State) error {
							// the control plane computes the next run from lastrun_at, an update must send it back.
							require.Equal(t, "2026-10-16T00:00:00Z", cp.updated.LastrunAt)
							require.Equal(t, "a1", cp.updated.AccountId)
							require.Equal(t, "amd64", cp.updated.Arch)
							require.Equal(t, uint64(2*time.Hour.Milliseconds()), cp.updated.Ttl)
							return nil
						},
					),
				},
				{
					ResourceName:      "eg_repository_scheduled_workload.default",
					ImportState:       true,
					ImportStateId:     "s1",
					ImportStateVerify: true,
					// the control plane reports durations and sizes in milliseconds and bytes, an import has no configured representation to preserve.
					ImportStateVerifyIgnore: []string{"timeout", "memory"},
				},
				{
					PreConfig: func() {
						cp.m.Lock()
						defer cp.m.Unlock()
						delete(cp.schedules, "s1")
					},
					Config:             config("6h", `timeout = "2h"`),
					PlanOnly:           true,
					ExpectNonEmptyPlan: true,
				},
			},
		})
	})

	t.Run("rejects a classic cron expression at plan time", func(t *testing.T) {
		cp := fake(t)

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: factories(cp),
			Steps: []resource.TestStep{
				{
					Config:      config("0 3 * * *", ""),
					ExpectError: regexp.MustCompile("invalid fron schedule"),
				},
			},
		})
		require.Empty(t, cp.schedules)
	})

	t.Run("rejects an invalid size at plan time", func(t *testing.T) {
		cp := fake(t)

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: factories(cp),
			Steps: []resource.TestStep{
				{
					Config:      config("1d", `vram = "lots"`),
					ExpectError: regexp.MustCompile("invalid size"),
				},
			},
		})
	})

	t.Run("rejects an invalid timeout at plan time", func(t *testing.T) {
		cp := fake(t)

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: factories(cp),
			Steps: []resource.TestStep{
				{
					Config:      config("1d", `timeout = "forever"`),
					ExpectError: regexp.MustCompile("invalid duration"),
				},
			},
		})
	})
}

func TestScheduledWorkloadResourceModelSchedule(t *testing.T) {
	t.Run("converts durations and sizes", func(t *testing.T) {
		s, err := (&ScheduledWorkloadResourceModel{
			Timeout: types.StringValue("90m"),
			Memory:  types.StringValue("2GiB"),
			Vram:    types.StringValue("8g"),
			CPU:     types.Int64Value(4),
			Labels:  []types.String{types.StringValue("gpu"), types.StringValue("fast")},
		}).schedule()
		require.NoError(t, err)
		require.Equal(t, uint64(90*time.Minute.Milliseconds()), s.Ttl)
		require.Equal(t, uint64(2<<30), s.Memory)
		require.Equal(t, uint64(8000000000), s.Vram)
		require.Equal(t, uint64(4), s.Cores)
		require.Equal(t, []string{"gpu", "fast"}, s.Labels)
	})

	t.Run("leaves unknown sizes for the control plane to default", func(t *testing.T) {
		s, err := (&ScheduledWorkloadResourceModel{
			Timeout: types.StringValue("1h"),
			Memory:  types.StringUnknown(),
			Vram:    types.StringNull(),
			CPU:     types.Int64Unknown(),
		}).schedule()
		require.NoError(t, err)
		require.Zero(t, s.Memory)
		require.Zero(t, s.Vram)
		require.Zero(t, s.Cores)
	})

	t.Run("carries the identity and lastrun_at of an existing schedule", func(t *testing.T) {
		s, err := (&ScheduledWorkloadResourceModel{
			ID:        types.StringValue("s1"),
			AccountID: types.StringValue("a1"),
			CreatedAt: types.StringValue("2026-10-15T00:00:00Z"),
			LastrunAt: types.StringValue("2026-10-16T00:00:00Z"),
			Timeout:   types.StringValue("1h"),
		}).schedule()
		require.NoError(t, err)
		require.Equal(t, "s1", s.Id)
		require.Equal(t, "a1", s.AccountId)
		require.Equal(t, "2026-10-15T00:00:00Z", s.CreatedAt)
		require.Equal(t, "2026-10-16T00:00:00Z", s.LastrunAt)
	})

	t.Run("rejects an invalid timeout", func(t *testing.T) {
		_, err := (&ScheduledWorkloadResourceModel{Timeout: types.StringValue("forever")}).schedule()
		require.Error(t, err)
	})

	t.Run("rejects an invalid size", func(t *testing.T) {
		_, err := (&ScheduledWorkloadResourceModel{Timeout: types.StringValue("1h"), Memory: types.StringValue("lots")}).schedule()
		require.Error(t, err)
	})
}

func TestScheduledWorkloadResourceModelRefresh(t *testing.T) {
	t.Run("preserves equivalent configured representations", func(t *testing.T) {
		m := ScheduledWorkloadResourceModel{
			Timeout: types.StringValue("60m"),
			Memory:  types.StringValue("1g"),
		}
		m.refresh(&repository.Schedule{Id: "s1", Ttl: uint64(time.Hour.Milliseconds()), Memory: 1000000000})
		require.Equal(t, "s1", m.ID.ValueString())
		require.Equal(t, "60m", m.Timeout.ValueString())
		require.Equal(t, "1g", m.Memory.ValueString())
	})

	t.Run("replaces representations that drifted", func(t *testing.T) {
		m := ScheduledWorkloadResourceModel{
			Timeout: types.StringValue("1h"),
			Memory:  types.StringValue("1g"),
		}
		m.refresh(&repository.Schedule{Ttl: uint64(2 * time.Hour.Milliseconds()), Memory: 2 << 30})
		require.Equal(t, "2h0m0s", m.Timeout.ValueString())
		require.Equal(t, "2.0 GiB", m.Memory.ValueString())
	})

	t.Run("keeps omitted labels null", func(t *testing.T) {
		m := ScheduledWorkloadResourceModel{}
		m.refresh(&repository.Schedule{})
		require.Nil(t, m.Labels)
	})

	t.Run("keeps configured empty labels empty", func(t *testing.T) {
		m := ScheduledWorkloadResourceModel{Labels: []types.String{}}
		m.refresh(&repository.Schedule{})
		require.NotNil(t, m.Labels)
		require.Empty(t, m.Labels)
	})

	t.Run("reports labels", func(t *testing.T) {
		m := ScheduledWorkloadResourceModel{}
		m.refresh(&repository.Schedule{Labels: []string{"gpu"}})
		require.Equal(t, []types.String{types.StringValue("gpu")}, m.Labels)
	})
}

func TestCronparse(t *testing.T) {
	t.Run("accepts fron schedules", func(t *testing.T) {
		require.NoError(t, cronparse("1d"))
		require.NoError(t, cronparse("6h"))
		require.NoError(t, cronparse("1d @ on weekdays"))
		require.NoError(t, cronparse("15m @ during 09:00-17:00 in America/New_York"))
	})

	t.Run("rejects classic cron expressions", func(t *testing.T) {
		require.Error(t, cronparse("0 3 * * *"))
	})

	t.Run("rejects empty and malformed schedules", func(t *testing.T) {
		require.Error(t, cronparse(""))
		require.Error(t, cronparse("garbage"))
	})
}

func TestBytesof(t *testing.T) {
	t.Run("parses human readable sizes", func(t *testing.T) {
		n, err := bytesof(types.StringValue("512MiB"))
		require.NoError(t, err)
		require.Equal(t, uint64(512<<20), n)
	})

	t.Run("treats null and unknown as zero", func(t *testing.T) {
		n, err := bytesof(types.StringNull())
		require.NoError(t, err)
		require.Zero(t, n)

		n, err = bytesof(types.StringUnknown())
		require.NoError(t, err)
		require.Zero(t, n)
	})

	t.Run("rejects malformed sizes", func(t *testing.T) {
		_, err := bytesof(types.StringValue("lots"))
		require.Error(t, err)
	})
}

func TestSizeof(t *testing.T) {
	t.Run("preserves an equivalent prior value", func(t *testing.T) {
		require.Equal(t, "1g", sizeof(types.StringValue("1g"), 1000000000).ValueString())
	})

	t.Run("formats the reported size when the prior value differs", func(t *testing.T) {
		require.Equal(t, "1.0 GiB", sizeof(types.StringValue("1g"), 1<<30).ValueString())
	})

	t.Run("formats the reported size when there is no prior value", func(t *testing.T) {
		require.Equal(t, "1.0 GiB", sizeof(types.StringNull(), 1<<30).ValueString())
		require.Equal(t, "0 B", sizeof(types.StringUnknown(), 0).ValueString())
	})
}

func TestDurationof(t *testing.T) {
	t.Run("preserves an equivalent prior value", func(t *testing.T) {
		require.Equal(t, "90m", durationof(types.StringValue("90m"), 90*time.Minute).ValueString())
	})

	t.Run("formats the reported duration when the prior value differs", func(t *testing.T) {
		require.Equal(t, "2h0m0s", durationof(types.StringValue("1h"), 2*time.Hour).ValueString())
	})

	t.Run("formats the reported duration when there is no prior value", func(t *testing.T) {
		require.Equal(t, "1h0m0s", durationof(types.StringNull(), time.Hour).ValueString())
	})
}

func TestParses(t *testing.T) {
	validate := func(v types.String) *validator.StringResponse {
		resp := &validator.StringResponse{}
		parses{description: "invalid duration", parse: durationparse}.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("timeout"), ConfigValue: v}, resp)
		return resp
	}

	t.Run("accepts a value that parses", func(t *testing.T) {
		require.False(t, validate(types.StringValue("1h")).Diagnostics.HasError())
	})

	t.Run("rejects a value that does not parse", func(t *testing.T) {
		resp := validate(types.StringValue("forever"))
		require.True(t, resp.Diagnostics.HasError())
		require.Equal(t, "invalid duration", resp.Diagnostics[0].Summary())
	})

	t.Run("skips null and unknown values", func(t *testing.T) {
		require.False(t, validate(types.StringNull()).Diagnostics.HasError())
		require.False(t, validate(types.StringUnknown()).Diagnostics.HasError())
	})
}

// configured is the provider with its resources pointed at a fake control plane.
type configured struct {
	egt
	client *repository.ScheduleClient
}

func (t *configured) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	resp.ResourceData = t.client
}
