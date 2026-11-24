package acp

import (
	"context"
	"fmt"
	"maps"
	"sync"

	"mincode/app/backend/core"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"
)

const (
	elicitationSessionMetaKey  = "mincode/sessionId"
	elicitationToolCallMetaKey = "mincode/toolCallId"
)

type elicitationOwner struct {
	RequestID string `json:"requestId"`
	ServerID  string `json:"serverId"`
	SessionID string `json:"sessionId"`
}

type elicitationRequestPayload struct {
	elicitationOwner
	ToolCallID      string                           `json:"toolCallId,omitempty"`
	Message         string                           `json:"message"`
	RequestedSchema acpsdk.UnstableElicitationSchema `json:"requestedSchema"`
	Meta            map[string]any                   `json:"meta,omitempty"`
}

type pendingElicitation struct {
	owner    elicitationOwner
	ctx      context.Context
	response chan acpsdk.UnstableCreateElicitationResponse
}

var elicitations = struct {
	sync.Mutex
	pending map[string]pendingElicitation
}{pending: make(map[string]pendingElicitation)}

func (c *bridgeClient) UnstableCreateElicitation(ctx context.Context, request acpsdk.UnstableCreateElicitationRequest) (acpsdk.UnstableCreateElicitationResponse, error) {
	form := request.Form
	if form == nil {
		return acpsdk.NewUnstableCreateElicitationResponseDecline(), nil
	}
	if ctx.Err() != nil {
		return acpsdk.NewUnstableCreateElicitationResponseCancel(), nil
	}

	sessionID, _ := form.Meta[elicitationSessionMetaKey].(string)
	toolCallID, _ := form.Meta[elicitationToolCallMetaKey].(string)
	meta := maps.Clone(form.Meta)
	delete(meta, elicitationSessionMetaKey)
	delete(meta, elicitationToolCallMetaKey)

	owner := elicitationOwner{RequestID: uuid.NewString(), ServerID: c.serverID, SessionID: sessionID}
	response := make(chan acpsdk.UnstableCreateElicitationResponse, 1)
	elicitations.Lock()
	elicitations.pending[owner.RequestID] = pendingElicitation{owner: owner, ctx: ctx, response: response}
	elicitations.Unlock()

	// Like permission requests, elicitation events use the reliable event channel.
	core.Emit("acp:elicitation-request", elicitationRequestPayload{
		elicitationOwner: owner,
		ToolCallID:       toolCallID,
		Message:          form.Message,
		RequestedSchema:  form.RequestedSchema,
		Meta:             meta,
	})

	select {
	case result := <-response:
		return result, nil
	case <-ctx.Done():
		elicitations.Lock()
		_, pending := elicitations.pending[owner.RequestID]
		delete(elicitations.pending, owner.RequestID)
		elicitations.Unlock()
		if !pending {
			// The user response won the race with cancellation.
			return <-response, nil
		}
		core.Emit("acp:elicitation-closed", struct {
			elicitationOwner
			Reason string `json:"reason"`
		}{elicitationOwner: owner, Reason: "withdrawn"})
		return acpsdk.NewUnstableCreateElicitationResponseCancel(), nil
	}
}

func (c *bridgeClient) UnstableCompleteElicitation(_ context.Context, _ acpsdk.UnstableCompleteElicitationNotification) error {
	// Completion refers to a URL elicitation ID. Form requests finish with their response.
	return nil
}

func resolveElicitation(requestID, serverID, sessionID, action string, content map[string]any) error {
	var response acpsdk.UnstableCreateElicitationResponse
	switch action {
	case "accept":
		response = acpsdk.NewUnstableCreateElicitationResponseAccept()
		response.Accept.Content = content
	case "decline":
		response = acpsdk.NewUnstableCreateElicitationResponseDecline()
	case "cancel":
		response = acpsdk.NewUnstableCreateElicitationResponseCancel()
	default:
		return fmt.Errorf("invalid elicitation action %q", action)
	}

	elicitations.Lock()
	defer elicitations.Unlock()
	request, ok := elicitations.pending[requestID]
	if !ok || request.ctx.Err() != nil {
		return fmt.Errorf("elicitation request %q is not pending", requestID)
	}
	if request.owner.ServerID != serverID || request.owner.SessionID != sessionID {
		return fmt.Errorf("elicitation request %q owner mismatch", requestID)
	}
	delete(elicitations.pending, requestID)
	request.response <- response
	return nil
}
