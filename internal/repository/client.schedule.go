package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/egdaemon/egt/internal/errorsx"
)

func NewScheduleClient(c *http.Client, host string) *ScheduleClient {
	return &ScheduleClient{
		c:    c,
		host: host,
	}
}

// ScheduleClient manages a repository's scheduled workloads via the control plane's /r/schedule api.
type ScheduleClient struct {
	c    *http.Client
	host string
}

func (t ScheduleClient) Search(ctx context.Context, req *ScheduleSearchRequest) (_ *ScheduleSearchResponse, err error) {
	var (
		resp    ScheduleSearchResponse
		httpreq *http.Request
		query   = url.Values{
			"query":   {req.Query},
			"vcs_uri": {req.VcsUri},
			"offset":  {strconv.FormatUint(req.Offset, 10)},
			"limit":   {strconv.FormatUint(req.Limit, 10)},
		}
	)

	if httpreq, err = http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/r/schedule/?%s", t.host, query.Encode()), nil); err != nil {
		return nil, err
	}

	if err = t.do(httpreq, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

// Find pages through the schedules of vcsuri until it locates id, the control plane has no lookup by id.
// an empty vcsuri searches every schedule of the account. returns nil when the schedule does not exist.
func (t ScheduleClient) Find(ctx context.Context, vcsuri string, id string) (_ *Schedule, err error) {
	var (
		resp *ScheduleSearchResponse
		req  = &ScheduleSearchRequest{VcsUri: vcsuri, Limit: 100}
	)

	for {
		if resp, err = t.Search(ctx, req); err != nil {
			return nil, err
		}

		for _, s := range resp.Items {
			if s.Id == id {
				return s, nil
			}
		}

		// the control plane resets the offset once it returns a partial page.
		if resp.Next.Offset == 0 {
			return nil, nil
		}

		req = resp.Next
	}
}

func (t ScheduleClient) Create(ctx context.Context, req *ScheduleCreateRequest) (_ *ScheduleCreateResponse, err error) {
	var (
		resp ScheduleCreateResponse
	)

	if err = t.send(ctx, http.MethodPost, fmt.Sprintf("%s/r/schedule/", t.host), req, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

// Update replaces the schedule, fields omitted from req are reset to their defaults.
func (t ScheduleClient) Update(ctx context.Context, id string, req *ScheduleUpdateRequest) (_ *ScheduleUpdateResponse, err error) {
	var (
		resp ScheduleUpdateResponse
	)

	if err = t.send(ctx, http.MethodPatch, fmt.Sprintf("%s/r/schedule/%s", t.host, url.PathEscape(id)), req, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

func (t ScheduleClient) Destroy(ctx context.Context, id string) (_ *ScheduleDestroyResponse, err error) {
	var (
		resp ScheduleDestroyResponse
	)

	if err = t.send(ctx, http.MethodDelete, fmt.Sprintf("%s/r/schedule/%s", t.host, url.PathEscape(id)), &ScheduleDestroyRequest{}, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

func (t ScheduleClient) send(ctx context.Context, method string, uri string, req any, resp any) (err error) {
	var (
		encoded []byte
		httpreq *http.Request
	)

	if encoded, err = json.Marshal(req); err != nil {
		return err
	}

	if httpreq, err = http.NewRequestWithContext(ctx, method, uri, bytes.NewReader(encoded)); err != nil {
		return err
	}
	httpreq.Header.Set("Content-Type", "application/json")

	return t.do(httpreq, resp)
}

func (t ScheduleClient) do(httpreq *http.Request, resp any) (err error) {
	var (
		httpresp *http.Response
	)

	if httpresp, err = t.c.Do(httpreq); err != nil {
		return err
	}
	defer func() { errorsx.Log(httpresp.Body.Close()) }()

	if httpresp.StatusCode < 200 || httpresp.StatusCode > 299 {
		return errorsx.Errorf("%s %s: %s", httpreq.Method, httpreq.URL.Path, httpresp.Status)
	}

	return json.NewDecoder(httpresp.Body).Decode(resp)
}
