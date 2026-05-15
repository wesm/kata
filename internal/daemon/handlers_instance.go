package daemon

import (
	"context"
	"fmt"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/version"
)

// registerInstanceHandlers installs /api/v1/instance — the local kata
// installation's stable identifier alongside the daemon's build version and
// the database's schema_version. The instance UID is set by db.Open at first
// init and never changes; this endpoint surfaces it for future federation
// spoke discovery and lets the spoke negotiate wire/schema compatibility
// without a follow-up /health round trip.
func registerInstanceHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "instance",
		Method:      "GET",
		Path:        "/api/v1/instance",
	}, func(ctx context.Context, _ *struct{}) (*api.InstanceResponse, error) {
		uid := cfg.DB.InstanceUID()
		if uid == "" {
			return nil, api.NewError(503, "instance_uid_unset",
				"meta.instance_uid not yet set", "", nil)
		}
		var schemaValue string
		if err := cfg.DB.QueryRowContext(ctx,
			`SELECT value FROM meta WHERE key='schema_version'`,
		).Scan(&schemaValue); err != nil {
			return nil, api.NewError(500, "schema_version_unavailable",
				err.Error(), "", nil)
		}
		sv, err := strconv.ParseInt(schemaValue, 10, 64)
		if err != nil {
			return nil, api.NewError(500, "schema_version_parse",
				fmt.Sprintf("parse schema_version %q: %v", schemaValue, err),
				"", nil)
		}
		out := &api.InstanceResponse{}
		out.Body.InstanceUID = uid
		out.Body.Version = version.Version
		out.Body.SchemaVersion = sv
		return out, nil
	})
}
