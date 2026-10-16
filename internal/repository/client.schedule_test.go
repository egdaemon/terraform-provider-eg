package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScheduleClientSearch(t *testing.T) {
	t.Run("encodes the request as query parameters", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodGet, r.Method)
			require.Equal(t, "/r/schedule/", r.URL.Path)
			require.Equal(t, "git@github.com:egdaemon/eg.git", r.URL.Query().Get("vcs_uri"))
			require.Equal(t, "nightly", r.URL.Query().Get("query"))
			require.Equal(t, "2", r.URL.Query().Get("offset"))
			require.Equal(t, "50", r.URL.Query().Get("limit"))
			_, _ = io.WriteString(w, `{"next":{"offset":"3","limit":"50"},"items":[{"id":"s1","chron":"1d","entrypoint":"tests/stress","cores":"8"}]}`)
		}))
		defer srv.Close()

		resp, err := NewScheduleClient(srv.Client(), srv.URL).Search(context.Background(), &ScheduleSearchRequest{
			Query:  "nightly",
			VcsUri: "git@github.com:egdaemon/eg.git",
			Offset: 2,
			Limit:  50,
		})
		require.NoError(t, err)
		require.Equal(t, uint64(3), resp.Next.Offset)
		require.Len(t, resp.Items, 1)
		require.Equal(t, "s1", resp.Items[0].Id)
		require.Equal(t, "1d", resp.Items[0].Cron)
		require.Equal(t, "tests/stress", resp.Items[0].Entry)
		require.Equal(t, uint64(8), resp.Items[0].Cores)
	})

	t.Run("discards unknown fields", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"next":{},"items":[{"id":"s1","cid":"00000000-0000-0000-0000-000000000000"}]}`)
		}))
		defer srv.Close()

		resp, err := NewScheduleClient(srv.Client(), srv.URL).Search(context.Background(), &ScheduleSearchRequest{})
		require.NoError(t, err)
		require.Equal(t, "s1", resp.Items[0].Id)
	})

	t.Run("returns an error on a non 2xx status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer srv.Close()

		_, err := NewScheduleClient(srv.Client(), srv.URL).Search(context.Background(), &ScheduleSearchRequest{})
		require.ErrorContains(t, err, "400 Bad Request")
	})
}

func TestScheduleClientFind(t *testing.T) {
	// pages mimics the control plane's pagination: a full page advances the offset, a partial page resets it.
	pages := func(t *testing.T, limit int, ids ...string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var (
				offset int
				resp   = ScheduleSearchResponse{Next: &ScheduleSearchRequest{Limit: uint64(limit)}}
			)

			_, err := fmt.Sscan(r.URL.Query().Get("offset"), &offset)
			require.NoError(t, err)

			for _, id := range ids[min(offset*limit, len(ids)):min((offset+1)*limit, len(ids))] {
				resp.Items = append(resp.Items, &Schedule{Id: id, VcsUri: r.URL.Query().Get("vcs_uri")})
			}

			resp.Next.Offset = uint64(offset) + 1
			if len(resp.Items) < limit {
				resp.Next.Offset = 0
			}

			require.NoError(t, json.NewEncoder(w).Encode(&resp))
		}))
	}

	t.Run("locates a schedule on the first page", func(t *testing.T) {
		srv := pages(t, 100, "s1", "s2")
		defer srv.Close()

		found, err := NewScheduleClient(srv.Client(), srv.URL).Find(context.Background(), "git@github.com:egdaemon/eg.git", "s2")
		require.NoError(t, err)
		require.Equal(t, "s2", found.Id)
		require.Equal(t, "git@github.com:egdaemon/eg.git", found.VcsUri)
	})

	t.Run("locates a schedule on a later page", func(t *testing.T) {
		srv := pages(t, 2, "s1", "s2", "s3", "s4", "s5")
		defer srv.Close()

		found, err := NewScheduleClient(srv.Client(), srv.URL).Find(context.Background(), "", "s5")
		require.NoError(t, err)
		require.Equal(t, "s5", found.Id)
	})

	t.Run("returns nil when the schedule does not exist", func(t *testing.T) {
		srv := pages(t, 2, "s1", "s2", "s3", "s4")
		defer srv.Close()

		found, err := NewScheduleClient(srv.Client(), srv.URL).Find(context.Background(), "", "missing")
		require.NoError(t, err)
		require.Nil(t, found)
	})

	t.Run("returns nil when there are no schedules", func(t *testing.T) {
		srv := pages(t, 100)
		defer srv.Close()

		found, err := NewScheduleClient(srv.Client(), srv.URL).Find(context.Background(), "", "missing")
		require.NoError(t, err)
		require.Nil(t, found)
	})

	t.Run("propagates search errors", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		_, err := NewScheduleClient(srv.Client(), srv.URL).Find(context.Background(), "", "s1")
		require.ErrorContains(t, err, "500 Internal Server Error")
	})
}

func TestScheduleClientCreate(t *testing.T) {
	t.Run("posts the schedule using the control plane's json names", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var (
				body map[string]map[string]any
			)

			require.Equal(t, http.MethodPost, r.Method)
			require.Equal(t, "/r/schedule/", r.URL.Path)
			require.Equal(t, "application/json", r.Header.Get("Content-Type"))
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "1d", body["schedule"]["chron"])
			require.Equal(t, "tests/stress", body["schedule"]["entrypoint"])
			require.Equal(t, "3600000", body["schedule"]["ttl"])
			_, _ = io.WriteString(w, `{"schedule":{"id":"s1","chron":"1d","entrypoint":"tests/stress","nextrun_at":"2026-10-17T00:00:00Z"}}`)
		}))
		defer srv.Close()

		resp, err := NewScheduleClient(srv.Client(), srv.URL).Create(context.Background(), &ScheduleCreateRequest{
			Schedule: &Schedule{Cron: "1d", Entry: "tests/stress", Ttl: 3600000},
		})
		require.NoError(t, err)
		require.Equal(t, "s1", resp.Schedule.Id)
		require.Equal(t, "2026-10-17T00:00:00Z", resp.Schedule.NextrunAt)
	})

	t.Run("returns an error on a non 2xx status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer srv.Close()

		_, err := NewScheduleClient(srv.Client(), srv.URL).Create(context.Background(), &ScheduleCreateRequest{Schedule: &Schedule{Cron: "garbage"}})
		require.ErrorContains(t, err, "POST /r/schedule/: 400 Bad Request")
	})
}

func TestScheduleClientUpdate(t *testing.T) {
	t.Run("patches the schedule by id", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var (
				req ScheduleUpdateRequest
			)

			require.Equal(t, http.MethodPatch, r.Method)
			require.Equal(t, "/r/schedule/s1", r.URL.Path)
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			require.Equal(t, "6h", req.Schedule.Cron)
			require.Equal(t, "2026-10-16T00:00:00Z", req.Schedule.LastrunAt)
			require.NoError(t, json.NewEncoder(w).Encode(&ScheduleUpdateResponse{Schedule: req.Schedule}))
		}))
		defer srv.Close()

		resp, err := NewScheduleClient(srv.Client(), srv.URL).Update(context.Background(), "s1", &ScheduleUpdateRequest{
			Schedule: &Schedule{Id: "s1", Cron: "6h", LastrunAt: "2026-10-16T00:00:00Z"},
		})
		require.NoError(t, err)
		require.Equal(t, "6h", resp.Schedule.Cron)
	})

	t.Run("escapes the id", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/r/schedule/a%2Fb", r.URL.EscapedPath())
			_, _ = io.WriteString(w, `{"schedule":{}}`)
		}))
		defer srv.Close()

		_, err := NewScheduleClient(srv.Client(), srv.URL).Update(context.Background(), "a/b", &ScheduleUpdateRequest{Schedule: &Schedule{}})
		require.NoError(t, err)
	})

	t.Run("returns an error on a non 2xx status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		_, err := NewScheduleClient(srv.Client(), srv.URL).Update(context.Background(), "s1", &ScheduleUpdateRequest{Schedule: &Schedule{}})
		require.ErrorContains(t, err, "403 Forbidden")
	})
}

func TestScheduleClientDestroy(t *testing.T) {
	t.Run("deletes the schedule by id", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodDelete, r.Method)
			require.Equal(t, "/r/schedule/s1", r.URL.Path)
			_, _ = io.WriteString(w, `{"schedule":{"id":"s1"}}`)
		}))
		defer srv.Close()

		resp, err := NewScheduleClient(srv.Client(), srv.URL).Destroy(context.Background(), "s1")
		require.NoError(t, err)
		require.Equal(t, "s1", resp.Schedule.Id)
	})

	t.Run("returns an error on a non 2xx status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		_, err := NewScheduleClient(srv.Client(), srv.URL).Destroy(context.Background(), "s1")
		require.ErrorContains(t, err, "DELETE /r/schedule/s1: 500 Internal Server Error")
	})
}
