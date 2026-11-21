package filetemplates

import (
	"context"
	"encoding/json"
	"errors"

	"mincode/app/backend/core"
	"mincode/app/backend/storage"
)

func init() {
	for _, action := range []string{"list", "get", "save", "delete"} {
		action := action
		core.RegisterCtx("file_template_custom_"+action, func(ctx context.Context, _, raw string) (string, error) {
			if len(raw) > maxRequestBytes {
				return "", errors.New("file_template_request_limit")
			}
			var fields map[string]json.RawMessage
			if err := strict([]byte(raw), &fields); err != nil || fields == nil {
				return "", errors.New("file_template_invalid_request")
			}
			allowed := map[string]bool{}
			switch action {
			case "get":
				allowed["id"] = true
			case "save":
				allowed["revision"], allowed["template"] = true, true
			case "delete":
				allowed["revision"], allowed["id"] = true, true
			}
			if len(fields) != len(allowed) {
				return "", errors.New("file_template_invalid_request")
			}
			for field, value := range fields {
				if !allowed[field] || string(value) == "null" {
					return "", errors.New("file_template_invalid_request")
				}
			}
			var a struct {
				ID       string   `json:"id,omitempty"`
				Revision uint64   `json:"revision,omitempty"`
				Template Template `json:"template,omitempty"`
			}
			if err := strict([]byte(raw), &a); err != nil {
				return "", err
			}
			if (action == "get" || action == "delete") && (a.ID == "" || len(a.ID) > 128) {
				return "", errors.New("file_template_invalid_request")
			}
			var result any
			if action == "save" || action == "delete" {
				db, err := storage.DB()
				if err != nil {
					return "", err
				}
				s := Store{DB: db}
				if action == "save" {
					result, err = s.Save(ctx, a.Revision, a.Template)
				} else {
					result, err = s.Delete(ctx, a.Revision, a.ID)
				}
				if err != nil {
					return "", err
				}
			} else {
				db, err := storage.ReadDB()
				if err != nil {
					return "", err
				}
				s := Store{DB: db}
				if action == "list" {
					result, err = s.List(ctx)
				} else {
					result, err = s.Get(ctx, a.ID)
				}
				if err != nil {
					return "", err
				}
			}
			return core.EncodeResult(result)
		})
	}
}
