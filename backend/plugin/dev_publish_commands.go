package plugin

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"mincode/app/backend/core"
)

func decodePublishCommand(raw string, value any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		return errors.New("invalid publish command")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid publish command")
	}
	return nil
}
func pluginHostDevelopmentPublishListHandler(_, raw string) (string, error) {
	var args struct{}
	if err := decodePublishCommand(raw, &args); err != nil {
		return "", err
	}
	return core.EncodeResult(host().devBridge.Publishes())
}
func publishCommand(raw string, action func(*pluginDevBridge, string) error) (string, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := decodePublishCommand(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.ID) == "" {
		return "", errors.New("publish id is required")
	}
	if err := action(host().devBridge, args.ID); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}
func pluginHostDevelopmentPublishConfirmHandler(_, raw string) (string, error) {
	return publishCommand(raw, (*pluginDevBridge).ConfirmPublish)
}
func pluginHostDevelopmentPublishRejectHandler(_, raw string) (string, error) {
	return publishCommand(raw, (*pluginDevBridge).RejectPublish)
}
func pluginHostDevelopmentPublishCancelHandler(_, raw string) (string, error) {
	return publishCommand(raw, (*pluginDevBridge).CancelPublish)
}
func pluginHostDevelopmentPublishRefreshHandler(_, raw string) (string, error) {
	return publishCommand(raw, (*pluginDevBridge).RefreshPublish)
}
