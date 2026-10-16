package main

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var publicationKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func publicationCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "publication", Short: "Queue, inspect and recover immutable gateway publications"}
	var key, revision string
	var limits bool
	queue := &cobra.Command{Use: "queue", Args: cobra.NoArgs, PreRunE: func(_ *cobra.Command, _ []string) error {
		return validatePublicationKey(key)
	}, RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		body := map[string]any{}
		if revision != "" {
			body["revision"] = revision
		}
		if limits {
			body["includeGatewayLimits"] = true
		}
		value, err := api.requestWithKey(http.MethodPost, "/aep/v1/admin/data-plane/publications", body, true, key)
		return output(value, err)
	})}
	queue.Flags().StringVar(&key, "key", "", "non-secret idempotency key; preserve unchanged for retries")
	queue.Flags().StringVar(&revision, "revision", "", "optional publication revision")
	queue.Flags().BoolVar(&limits, "include-gateway-limits", false, "explicitly capture draft gateway limits with routes")
	_ = queue.MarkFlagRequired("key")
	command.AddCommand(queue)
	for _, mode := range []string{"get", "replay"} {
		var operation, replayKey string
		child := &cobra.Command{Use: mode, Args: cobra.NoArgs, PreRunE: func(_ *cobra.Command, _ []string) error {
			if _, err := uuid.Parse(operation); err != nil {
				return errors.New("operation-id must be a UUID")
			}
			if mode == "replay" {
				return validatePublicationKey(replayKey)
			}
			return nil
		}, RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
			id, _ := uuid.Parse(operation)
			path := "/aep/v1/admin/data-plane/publications/" + id.String()
			if mode == "replay" {
				value, err := api.requestWithKey(http.MethodPost, path+"/replay", map[string]any{}, true, replayKey)
				return output(value, err)
			}
			value, err := api.request(http.MethodGet, path, nil, true)
			return output(value, err)
		})}
		child.Flags().StringVar(&operation, "operation-id", "", "source or queried operation UUID")
		_ = child.MarkFlagRequired("operation-id")
		if mode == "replay" {
			child.Flags().StringVar(&replayKey, "key", "", "new non-secret recovery key; reuse for retries")
			_ = child.MarkFlagRequired("key")
		}
		command.AddCommand(child)
	}
	return command
}

func validatePublicationKey(key string) error {
	if !publicationKey.MatchString(key) {
		return errors.New("key must contain 1-128 ASCII letters, digits or ._:-")
	}
	return nil
}
