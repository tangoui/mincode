package plugin

import (
	"encoding/json"
	"fmt"

	"mincode/app/plugins/api"
	"mincode/app/plugins/api/externalopen"
)

const externalOpenPoint = "externalOpenHandlers"

type externalHandler struct {
	ID             string            `json:"id"`
	Type           string            `json:"type"`
	View           string            `json:"view"`
	Title          string            `json:"title,omitempty"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
}

func decodeExternalHandler(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var e externalHandler
	if err := decodeStrictJSON(raw, &e, true); err != nil {
		return nil, "", err
	}
	if !uriHandlerIDPattern.MatchString(e.ID) {
		return nil, e.ID, fmt.Errorf("invalid external handler id")
	}
	if _, ok := externalopen.Lookup(e.Type); !ok {
		return nil, e.ID, fmt.Errorf("unsupported external type")
	}
	if _, ok := ctx.views[e.View]; !ok {
		return nil, e.ID, fmt.Errorf("handler view must belong to this plugin")
	}
	var err error
	e.Title, err = validateContributionLine(e.Title, 64, false)
	if err != nil {
		return nil, e.ID, err
	}
	e.TitleLocalized, err = validateContributionLocalized(e.TitleLocalized, 64, 16)
	if err != nil {
		return nil, e.ID, err
	}
	return e, e.ID, nil
}
func init() {
	registerContributionPoint(&contributionPoint{Key: externalOpenPoint, Decode: decodeExternalHandler, EntryNoun: "external open handlers", IssuePrefix: "invalid externalOpenHandlers contribution", DuplicateMessage: func(id string) string { return "duplicate external handler " + id }, ExpandsPermission: func(a, b *resolvedContribution) bool { return b != nil && (a == nil || a.Fingerprint != b.Fingerprint) }})
	registerHostMethod("host.associations.list", nil, api.CapabilitySystemAssociations, hostAssociationsList)
	registerHostMethod("host.associations.request", nil, api.CapabilitySystemAssociations, hostAssociationsRequest)
	registerHostMethod("host.associations.read", nil, api.CapabilitySystemAssociations, hostAssociationsRead)
}
