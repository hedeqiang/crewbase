package audit

import (
	"context"
	"strconv"
	"time"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// APIPlugin exposes the audit export on the gateway (desktop devices only).
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/audit-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Remote methods: audit export (hash chain) and verification.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/audit",
			Validate: func(r *gateway.Request) error {
				q := r.HTTP.URL.Query()
				for _, k := range []string{"from", "to"} {
					if v := q.Get(k); v != "" {
						if _, err := parseTime(v); err != nil {
							return gateway.BadRequest("%s: want RFC 3339 or YYYY-MM-DD", k)
						}
					}
				}
				if v := q.Get("limit"); v != "" {
					if n, err := strconv.Atoi(v); err != nil || n < 0 {
						return gateway.BadRequest("limit must be a non-negative integer")
					}
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				q := r.HTTP.URL.Query()
				from, _ := parseTime(q.Get("from"))
				to, _ := parseTime(q.Get("to"))
				limit, _ := strconv.Atoi(q.Get("limit"))
				recs, err := s.log.Query(from, to, q.Get("type"), limit)
				if err != nil {
					return nil, err
				}
				if recs == nil {
					recs = []Record{}
				}
				seq, head := s.log.Head()
				// Records carry prev/hash: a consumer re-verifies the slice
				// with ComputeHash and links it to the head.
				return map[string]any{"records": recs, "headSeq": seq, "head": head,
					"hash": "sha256(prev || canonical JSON of the record without hash)"}, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/audit/verify",
			Handle: func(context.Context, *gateway.Request) (any, error) {
				return s.Verify()
			}})
		return nil
	},
})

func parseTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", v)
}
