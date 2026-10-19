package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/egdaemon/eg/schedules"
	"github.com/hashicorp/terraform-plugin-framework-timetypes/timetypes"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// fakeSchedules the eg schedule api's /r/schedule routes in memory.
type fakeSchedules struct {
	m         sync.Mutex
	schedules map[string]*schedules.Schedule
	updated   *schedules.Schedule
	srv       *httptest.Server
}

func newFakeSchedules(t *testing.T) *fakeSchedules {
	t.Helper()

	f := &fakeSchedules{schedules: map[string]*schedules.Schedule{}}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /r/schedule/", func(w http.ResponseWriter, r *http.Request) {
		var req schedules.ScheduleCreateRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.NoError(t, json.NewEncoder(w).Encode(&schedules.ScheduleCreateResponse{Schedule: f.create(req.GetSchedule())}))
	})
	mux.HandleFunc("GET /r/schedule/", func(w http.ResponseWriter, r *http.Request) {
		// a single page, the response's next offset of zero ends the paging.
		require.NoError(t, json.NewEncoder(w).Encode(&schedules.ScheduleSearchResponse{
			Next:  &schedules.ScheduleSearchRequest{},
			Items: f.search(r.URL.Query().Get("vcs_uri")),
		}))
	})
	mux.HandleFunc("PATCH /r/schedule/{id}", func(w http.ResponseWriter, r *http.Request) {
		var req schedules.ScheduleUpdateRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.NoError(t, json.NewEncoder(w).Encode(&schedules.ScheduleUpdateResponse{Schedule: f.update(r.PathValue("id"), req.GetSchedule())}))
	})
	mux.HandleFunc("DELETE /r/schedule/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.remove(r.PathValue("id"))
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// client the http client the provider is given in place of one built from the ssh key.
func (t *fakeSchedules) client() (*http.Client, error) {
	dst, err := url.Parse(t.srv.URL)
	if err != nil {
		return nil, err
	}

	return &http.Client{Transport: hostrewrite{dst: dst}}, nil
}

func (t *fakeSchedules) create(s *schedules.Schedule) *schedules.Schedule {
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
	return proto.CloneOf(stored)
}

func (t *fakeSchedules) search(vcsuri string) (found []*schedules.Schedule) {
	t.m.Lock()
	defer t.m.Unlock()

	for _, s := range t.schedules {
		if vcsuri == "" || vcsuri == s.GetVcsUri() {
			found = append(found, proto.CloneOf(s))
		}
	}
	return found
}

func (t *fakeSchedules) update(id string, s *schedules.Schedule) *schedules.Schedule {
	t.m.Lock()
	defer t.m.Unlock()

	// like the api, the schedule is replaced and its next run recomputed.
	t.updated = proto.CloneOf(s)
	stored := proto.CloneOf(s)
	stored.Id = id
	stored.UpdatedAt = "2026-10-16T12:00:00Z"
	stored.NextrunAt = "2026-10-16T18:00:00Z"
	t.schedules[id] = stored
	return proto.CloneOf(stored)
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
	factories := func(api *fakeSchedules) map[string]func() (tfprotov6.ProviderServer, error) {
		return map[string]func() (tfprotov6.ProviderServer, error){
			"eg": providerserver.NewProtocol6WithError(&egt{version: "test", client: api.client}),
		}
	}

	config := func(schedule string, extra string) string {
		return fmt.Sprintf(`
resource "eg_repository_scheduled_workload" "test" {
  repository = "git@github.com:egdaemon/eg.git"
  workload   = "tests/stress"
  schedule   = %q
  memory     = "1g"
  cpu        = 8
  %s
}
`, schedule, extra)
	}

	t.Run("should create, update, import, and destroy", func(t *testing.T) {
		api := newFakeSchedules(t)

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
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "schedule", "1d"),
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
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "schedule", "6h"),
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
		api := newFakeSchedules(t)
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

	t.Run("should reject an invalid size at plan time", func(t *testing.T) {
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: factories(newFakeSchedules(t)),
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
			ProtoV6ProviderFactories: factories(newFakeSchedules(t)),
			Steps: []resource.TestStep{
				{
					Config:      config("1d", `timeout = "forever"`),
					ExpectError: regexp.MustCompile("Invalid\\s+Time\\s+Duration\\s+String\\s+Value"),
				},
			},
		})
	})
}

func TestScheduledWorkloadResourceTimeout(t *testing.T) {
	config := func(timeout string) string {
		return fmt.Sprintf(`
resource "eg_repository_scheduled_workload" "test" {
  repository = "git@github.com:egdaemon/eg.git"
  workload   = "tests/stress"
  schedule   = "1d"
  timeout    = %q
}
`, timeout)
	}
	factories := func(api *fakeSchedules) map[string]func() (tfprotov6.ProviderServer, error) {
		return map[string]func() (tfprotov6.ProviderServer, error){
			"eg": providerserver.NewProtocol6WithError(&egt{version: "test", client: api.client}),
		}
	}

	// the control plane reports milliseconds, which are formatted as 1h30m0s. the type's semantic equality keeps the
	// configured spelling in state, so there is no perpetual diff, the framework checks for an empty plan after every apply.
	t.Run("should keep the configured spelling of an equivalent timeout", func(t *testing.T) {
		api := newFakeSchedules(t)

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: factories(api),
			Steps: []resource.TestStep{
				{
					Config: config("90m"),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "timeout", "90m"),
						func(*terraform.State) error {
							require.Equal(t, uint64(90*time.Minute.Milliseconds()), api.get("s1").GetTtl())
							return nil
						},
					),
				},
				{
					Config: config("2h"),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "timeout", "2h"),
						func(*terraform.State) error {
							require.Equal(t, uint64(2*time.Hour.Milliseconds()), api.get("s1").GetTtl())
							return nil
						},
					),
				},
			},
		})
	})
}

func TestScheduledWorkloadResourceSecretURIs(t *testing.T) {
	config := func(extra string) string {
		return fmt.Sprintf(`
resource "eg_repository_scheduled_workload" "test" {
  repository = "git@github.com:egdaemon/eg.git"
  workload   = "tests/stress"
  schedule   = "1d"
  %s
}
`, extra)
	}

	api := newFakeSchedules(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"eg": providerserver.NewProtocol6WithError(&egt{version: "test", client: api.client}),
		},
		Steps: []resource.TestStep{
			{
				Config: config(`secret_uris = ["gcpsm://p/one", "gcpsm://p/two"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "secret_uris.#", "2"),
					resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "secret_uris.0", "gcpsm://p/one"),
					resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "secret_uris.1", "gcpsm://p/two"),
					func(*terraform.State) error {
						require.Equal(t, []string{"gcpsm://p/one", "gcpsm://p/two"}, api.get("s1").GetSecretUris())
						return nil
					},
				),
			},
			{
				Config: config(`secret_uris = ["gcpsm://p/three"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("eg_repository_scheduled_workload.test", "secret_uris.#", "1"),
					func(*terraform.State) error {
						require.Equal(t, []string{"gcpsm://p/three"}, api.get("s1").GetSecretUris())
						return nil
					},
				),
			},
			{
				// removing the attribute removes the secrets.
				Config: config(``),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("eg_repository_scheduled_workload.test", "secret_uris.#"),
					func(*terraform.State) error {
						require.Empty(t, api.get("s1").GetSecretUris())
						return nil
					},
				),
			},
		},
	})
}

func TestScheduledWorkloadResourceModelSchedule(t *testing.T) {
	t.Run("should convert durations and sizes", func(t *testing.T) {
		s, err := (&ScheduledWorkloadResourceModel{
			Timeout: timetypes.NewGoDurationValueFromStringMust("90m"),
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

	t.Run("should carry the secret uris", func(t *testing.T) {
		s, err := (&ScheduledWorkloadResourceModel{
			Timeout:    timetypes.NewGoDurationValueFromStringMust("1h"),
			SecretURIs: []types.String{types.StringValue("gcpsm://p/one"), types.StringValue("gcpsm://p/two")},
		}).schedule()
		require.NoError(t, err)
		require.Equal(t, []string{"gcpsm://p/one", "gcpsm://p/two"}, s.SecretUris)
	})

	t.Run("should leave unknown sizes for the control plane to default", func(t *testing.T) {
		s, err := (&ScheduledWorkloadResourceModel{
			Timeout: timetypes.NewGoDurationValueFromStringMust("1h"),
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
			Timeout:   timetypes.NewGoDurationValueFromStringMust("1h"),
		}).schedule()
		require.NoError(t, err)
		require.Equal(t, "s1", s.Id)
		require.Equal(t, "a1", s.AccountId)
		require.Equal(t, "2026-10-15T00:00:00Z", s.CreatedAt)
		require.Equal(t, "2026-10-16T00:00:00Z", s.LastrunAt)
	})

	t.Run("should reject an invalid timeout", func(t *testing.T) {
		_, err := (&ScheduledWorkloadResourceModel{Timeout: timetypes.GoDuration{StringValue: basetypes.NewStringValue("forever")}}).schedule()
		require.Error(t, err)
	})

	t.Run("should reject an invalid size", func(t *testing.T) {
		_, err := (&ScheduledWorkloadResourceModel{Timeout: timetypes.NewGoDurationValueFromStringMust("1h"), Memory: types.StringValue("lots")}).schedule()
		require.Error(t, err)
	})
}

func TestScheduledWorkloadResourceModelRefresh(t *testing.T) {
	// the timeout's configured representation is kept by the framework through the type's semantic equality.
	t.Run("should preserve equivalent configured representations", func(t *testing.T) {
		m := ScheduledWorkloadResourceModel{
			Memory: types.StringValue("1g"),
		}
		m.refresh(&schedules.Schedule{Id: "s1", Ttl: uint64(time.Hour.Milliseconds()), Memory: 1000000000})
		require.Equal(t, "s1", m.ID.ValueString())
		require.Equal(t, "1h0m0s", m.Timeout.ValueString())
		require.Equal(t, "1g", m.Memory.ValueString())
	})

	t.Run("should replace representations that drifted", func(t *testing.T) {
		m := ScheduledWorkloadResourceModel{
			Memory: types.StringValue("1g"),
		}
		m.refresh(&schedules.Schedule{Ttl: uint64(2 * time.Hour.Milliseconds()), Memory: 2 << 30})
		require.Equal(t, "2h0m0s", m.Timeout.ValueString())
		require.Equal(t, "2.0 GiB", m.Memory.ValueString())
	})

	t.Run("should report secret uris", func(t *testing.T) {
		m := ScheduledWorkloadResourceModel{}
		m.refresh(&schedules.Schedule{SecretUris: []string{"gcpsm://p/one"}})
		require.Equal(t, []types.String{types.StringValue("gcpsm://p/one")}, m.SecretURIs)
	})

	t.Run("should keep omitted secret uris null", func(t *testing.T) {
		m := ScheduledWorkloadResourceModel{}
		m.refresh(&schedules.Schedule{})
		require.Nil(t, m.SecretURIs)
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

func TestParses(t *testing.T) {
	validate := func(v types.String) *validator.StringResponse {
		resp := &validator.StringResponse{}
		parses{description: "invalid size", parse: sizeparse}.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("size"), ConfigValue: v}, resp)
		return resp
	}

	t.Run("should accept a value that parses", func(t *testing.T) {
		require.False(t, validate(types.StringValue("1g")).Diagnostics.HasError())
	})

	t.Run("should reject a value that does not parse", func(t *testing.T) {
		resp := validate(types.StringValue("garbage"))
		require.True(t, resp.Diagnostics.HasError())
		require.Equal(t, "invalid size", resp.Diagnostics[0].Summary())
	})

	t.Run("should skip null and unknown values", func(t *testing.T) {
		require.False(t, validate(types.StringNull()).Diagnostics.HasError())
		require.False(t, validate(types.StringUnknown()).Diagnostics.HasError())
	})
}
