package provider

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/egdaemon/eg/schedules"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// fakeSchedules the eg schedule api in memory.
type fakeSchedules struct {
	m         sync.Mutex
	schedules map[string]*schedules.Schedule
	updated   *schedules.Schedule
}

func newFakeSchedules() *fakeSchedules {
	return &fakeSchedules{schedules: map[string]*schedules.Schedule{}}
}

func (t *fakeSchedules) Create(ctx context.Context, s *schedules.Schedule) (*schedules.Schedule, error) {
	t.m.Lock()
	defer t.m.Unlock()

	// like the api, the id, account, timestamps, and machine defaults are assigned on create.
	stored := proto.CloneOf(s)
	stored.Id = fmt.Sprintf("s%d", len(t.schedules)+1)
	stored.AccountId = "a1"
	stored.CreatedAt = "2026-10-15T00:00:00Z"
	stored.UpdatedAt = "2026-10-15T00:00:00Z"
	stored.LastrunAt = "2026-10-16T00:00:00Z"
	stored.NextrunAt = "2026-10-17T00:00:00Z"
	stored.Arch = "amd64"
	stored.Os = "linux"
	t.schedules[stored.Id] = stored
	return proto.CloneOf(stored), nil
}

func (t *fakeSchedules) Find(ctx context.Context, vcsuri string, id string) (*schedules.Schedule, error) {
	t.m.Lock()
	defer t.m.Unlock()

	s, ok := t.schedules[id]
	if !ok || (vcsuri != "" && vcsuri != s.GetVcsUri()) {
		return nil, nil
	}
	return proto.CloneOf(s), nil
}

func (t *fakeSchedules) Update(ctx context.Context, id string, s *schedules.Schedule) (*schedules.Schedule, error) {
	t.m.Lock()
	defer t.m.Unlock()

	// like the api, the schedule is replaced and its next run recomputed.
	t.updated = proto.CloneOf(s)
	stored := proto.CloneOf(s)
	stored.Id = id
	stored.UpdatedAt = "2026-10-16T12:00:00Z"
	stored.NextrunAt = "2026-10-16T18:00:00Z"
	t.schedules[id] = stored
	return proto.CloneOf(stored), nil
}

func (t *fakeSchedules) Remove(ctx context.Context, id string) error {
	t.m.Lock()
	defer t.m.Unlock()

	delete(t.schedules, id)
	return nil
}

func (t *fakeSchedules) get(id string) *schedules.Schedule {
	t.m.Lock()
	defer t.m.Unlock()
	return t.schedules[id]
}

func (t *fakeSchedules) remove(id string) {
	t.m.Lock()
	defer t.m.Unlock()
	delete(t.schedules, id)
}

func TestScheduledWorkloadResource(t *testing.T) {
	factories := func(api scheduleAPI) map[string]func() (tfprotov6.ProviderServer, error) {
		return map[string]func() (tfprotov6.ProviderServer, error){
			"eg": providerserver.NewProtocol6WithError(&egt{version: "test", schedules: api}),
		}
	}

	config := func(cron string, extra string) string {
		return fmt.Sprintf(`
resource "eg_repository_scheduled_workload" "test" {
  repository = "git@github.com:egdaemon/eg.git"
  workload   = "tests/stress"
  cron       = %q
  memory     = "1g"
  cpu        = 8
  %s
}
`, cron, extra)
	}

	t.Run("should create, update, import, and destroy", func(t *testing.T) {
		api := newFakeSchedules()

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: factories(api),
			CheckDestroy: func(*terraform.State) error {
				require.Nil(t, api.get("s1"))
				return nil
			},
			Steps: []resource.TestStep{
				{
					Config: config("1d", `labels = ["gpu"]`),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "id", "s1"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "account_id", "a1"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "workload", "tests/stress"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "cron", "1d"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "timeout", "1h"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "memory", "1g"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "cpu", "8"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "arch", "amd64"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "labels.#", "1"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "labels.0", "gpu"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "nextrun_at", "2026-10-17T00:00:00Z"),
						func(*terraform.State) error {
							require.Equal(t, "tests/stress", api.get("s1").GetEntry())
							require.Equal(t, uint64(time.Hour.Milliseconds()), api.get("s1").GetTtl())
							require.Equal(t, uint64(1000000000), api.get("s1").GetMemory())
							return nil
						},
					),
				},
				{
					Config: config("6h", `timeout = "2h"`),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "id", "s1"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "cron", "6h"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "timeout", "2h"),
						resource.TestCheckNoResourceAttr("eg_repository_scheduled_workload.test", "labels"),
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "nextrun_at", "2026-10-16T18:00:00Z"),
						func(*terraform.State) error {
							// the api computes the next run from lastrun_at and resets omitted fields, an update must send the full record.
							require.Equal(t, "2026-10-16T00:00:00Z", api.updated.GetLastrunAt())
							require.Equal(t, "a1", api.updated.GetAccountId())
							require.Equal(t, "amd64", api.updated.GetArch())
							require.Equal(t, uint64(2*time.Hour.Milliseconds()), api.updated.GetTtl())
							return nil
						},
					),
				},
				{
					ResourceName:      "eg_repository_scheduled_workload.test",
					ImportState:       true,
					ImportStateId:     "s1",
					ImportStateVerify: true,
					// the api reports durations and sizes in milliseconds and bytes, an import has no configured representation to preserve.
					ImportStateVerifyIgnore: []string{"timeout", "memory"},
				},
			},
		})
	})

	t.Run("should recreate a schedule removed out of band", func(t *testing.T) {
		api := newFakeSchedules()
		cfg := config("1d", "")

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: factories(api),
			Steps: []resource.TestStep{
				{Config: cfg},
				{
					PreConfig: func() { api.remove("s1") },
					Config:    cfg,
					Check: func(*terraform.State) error {
						require.NotNil(t, api.get("s1"))
						return nil
					},
				},
			},
		})
	})

	t.Run("should reject a classic cron expression at plan time", func(t *testing.T) {
		api := newFakeSchedules()

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: factories(api),
			Steps: []resource.TestStep{
				{
					Config:      config("0 3 * * *", ""),
					ExpectError: regexp.MustCompile("invalid fron schedule"),
				},
			},
		})
		require.Empty(t, api.schedules)
	})

	t.Run("should reject an invalid size at plan time", func(t *testing.T) {
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: factories(newFakeSchedules()),
			Steps: []resource.TestStep{
				{
					Config:      config("1d", `vram = "lots"`),
					ExpectError: regexp.MustCompile("invalid size"),
				},
			},
		})
	})

	t.Run("should reject an invalid timeout at plan time", func(t *testing.T) {
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: factories(newFakeSchedules()),
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
	t.Run("should convert durations and sizes", func(t *testing.T) {
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

	t.Run("should leave unknown sizes for the control plane to default", func(t *testing.T) {
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

	t.Run("should carry the identity and lastrun_at of an existing schedule", func(t *testing.T) {
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

	t.Run("should reject an invalid timeout", func(t *testing.T) {
		_, err := (&ScheduledWorkloadResourceModel{Timeout: types.StringValue("forever")}).schedule()
		require.Error(t, err)
	})

	t.Run("should reject an invalid size", func(t *testing.T) {
		_, err := (&ScheduledWorkloadResourceModel{Timeout: types.StringValue("1h"), Memory: types.StringValue("lots")}).schedule()
		require.Error(t, err)
	})
}

func TestScheduledWorkloadResourceModelRefresh(t *testing.T) {
	t.Run("should preserve equivalent configured representations", func(t *testing.T) {
		m := ScheduledWorkloadResourceModel{
			Timeout: types.StringValue("60m"),
			Memory:  types.StringValue("1g"),
		}
		m.refresh(&schedules.Schedule{Id: "s1", Ttl: uint64(time.Hour.Milliseconds()), Memory: 1000000000})
		require.Equal(t, "s1", m.ID.ValueString())
		require.Equal(t, "60m", m.Timeout.ValueString())
		require.Equal(t, "1g", m.Memory.ValueString())
	})

	t.Run("should replace representations that drifted", func(t *testing.T) {
		m := ScheduledWorkloadResourceModel{
			Timeout: types.StringValue("1h"),
			Memory:  types.StringValue("1g"),
		}
		m.refresh(&schedules.Schedule{Ttl: uint64(2 * time.Hour.Milliseconds()), Memory: 2 << 30})
		require.Equal(t, "2h0m0s", m.Timeout.ValueString())
		require.Equal(t, "2.0 GiB", m.Memory.ValueString())
	})

	t.Run("should keep omitted labels null", func(t *testing.T) {
		m := ScheduledWorkloadResourceModel{}
		m.refresh(&schedules.Schedule{})
		require.Nil(t, m.Labels)
	})

	t.Run("should keep configured empty labels empty", func(t *testing.T) {
		m := ScheduledWorkloadResourceModel{Labels: []types.String{}}
		m.refresh(&schedules.Schedule{})
		require.NotNil(t, m.Labels)
		require.Empty(t, m.Labels)
	})

	t.Run("should report labels", func(t *testing.T) {
		m := ScheduledWorkloadResourceModel{}
		m.refresh(&schedules.Schedule{Labels: []string{"gpu"}})
		require.Equal(t, []types.String{types.StringValue("gpu")}, m.Labels)
	})
}

func TestCronparse(t *testing.T) {
	t.Run("should accept fron schedules", func(t *testing.T) {
		require.NoError(t, cronparse("1d"))
		require.NoError(t, cronparse("6h"))
		require.NoError(t, cronparse("1d @ on weekdays"))
		require.NoError(t, cronparse("15m @ during 09:00-17:00 in America/New_York"))
	})

	t.Run("should reject classic cron expressions", func(t *testing.T) {
		require.Error(t, cronparse("0 3 * * *"))
	})

	t.Run("should reject empty and malformed schedules", func(t *testing.T) {
		require.Error(t, cronparse(""))
		require.Error(t, cronparse("garbage"))
	})
}

func TestBytesof(t *testing.T) {
	t.Run("should parse human readable sizes", func(t *testing.T) {
		n, err := bytesof(types.StringValue("512MiB"))
		require.NoError(t, err)
		require.Equal(t, uint64(512<<20), n)
	})

	t.Run("should treat null and unknown as zero", func(t *testing.T) {
		n, err := bytesof(types.StringNull())
		require.NoError(t, err)
		require.Zero(t, n)

		n, err = bytesof(types.StringUnknown())
		require.NoError(t, err)
		require.Zero(t, n)
	})

	t.Run("should reject malformed sizes", func(t *testing.T) {
		_, err := bytesof(types.StringValue("lots"))
		require.Error(t, err)
	})
}

func TestSizeof(t *testing.T) {
	t.Run("should preserve an equivalent prior value", func(t *testing.T) {
		require.Equal(t, "1g", sizeof(types.StringValue("1g"), 1000000000).ValueString())
	})

	t.Run("should format the reported size when the prior value differs", func(t *testing.T) {
		require.Equal(t, "1.0 GiB", sizeof(types.StringValue("1g"), 1<<30).ValueString())
	})

	t.Run("should format the reported size when there is no prior value", func(t *testing.T) {
		require.Equal(t, "1.0 GiB", sizeof(types.StringNull(), 1<<30).ValueString())
		require.Equal(t, "0 B", sizeof(types.StringUnknown(), 0).ValueString())
	})
}

func TestDurationof(t *testing.T) {
	t.Run("should preserve an equivalent prior value", func(t *testing.T) {
		require.Equal(t, "90m", durationof(types.StringValue("90m"), 90*time.Minute).ValueString())
	})

	t.Run("should format the reported duration when the prior value differs", func(t *testing.T) {
		require.Equal(t, "2h0m0s", durationof(types.StringValue("1h"), 2*time.Hour).ValueString())
	})

	t.Run("should format the reported duration when there is no prior value", func(t *testing.T) {
		require.Equal(t, "1h0m0s", durationof(types.StringNull(), time.Hour).ValueString())
	})
}

func TestParses(t *testing.T) {
	validate := func(v types.String) *validator.StringResponse {
		resp := &validator.StringResponse{}
		parses{description: "invalid duration", parse: durationparse}.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("timeout"), ConfigValue: v}, resp)
		return resp
	}

	t.Run("should accept a value that parses", func(t *testing.T) {
		require.False(t, validate(types.StringValue("1h")).Diagnostics.HasError())
	})

	t.Run("should reject a value that does not parse", func(t *testing.T) {
		resp := validate(types.StringValue("forever"))
		require.True(t, resp.Diagnostics.HasError())
		require.Equal(t, "invalid duration", resp.Diagnostics[0].Summary())
	})

	t.Run("should skip null and unknown values", func(t *testing.T) {
		require.False(t, validate(types.StringNull()).Diagnostics.HasError())
		require.False(t, validate(types.StringUnknown()).Diagnostics.HasError())
	})
}
