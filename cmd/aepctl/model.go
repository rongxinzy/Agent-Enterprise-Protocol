package main

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func modelCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "model", Short: "Manage models and assignments"}
	command.AddCommand(&cobra.Command{Use: "list", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodGet, "/aep/v1/admin/models", nil, true)
		return output(value, err)
	})})
	command.AddCommand(&cobra.Command{Use: "assignments", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodGet, "/aep/v1/admin/model-assignments", nil, true)
		return output(value, err)
	})})

	var modelID, displayName, sourceType, endpoint, upstreamModel, localModelRef, credentialID string
	var capabilities []string
	var contextWindow int32
	var defaultModel, enabled bool
	create := &cobra.Command{Use: "create", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		body := map[string]any{
			"id": modelID, "displayName": displayName, "sourceType": sourceType,
			"protocol": "openai-compatible", "capabilities": capabilities,
			"isDefault": defaultModel, "enabled": enabled,
		}
		if endpoint != "" {
			body["endpoint"] = endpoint
		}
		if upstreamModel != "" {
			body["upstreamModel"] = upstreamModel
		}
		if localModelRef != "" {
			body["localModelRef"] = localModelRef
		}
		if credentialID != "" {
			body["credentialId"] = credentialID
		}
		if contextWindow > 0 {
			body["contextWindow"] = contextWindow
		}
		value, err := api.request(http.MethodPost, "/aep/v1/admin/models", body, true)
		return output(value, err)
	})}
	create.Flags().StringVar(&modelID, "model-id", "", "model identifier")
	create.Flags().StringVar(&displayName, "display-name", "", "display name")
	create.Flags().StringVar(&sourceType, "source-type", "gateway", "gateway, enterprise_open_source, or local")
	create.Flags().StringVar(&endpoint, "endpoint", "", "upstream endpoint or gateway route")
	create.Flags().StringVar(&upstreamModel, "upstream-model", "", "upstream model identifier")
	create.Flags().StringVar(&localModelRef, "local-model-ref", "", "local client model reference")
	create.Flags().StringVar(&credentialID, "credential-id", "", "optional credential identifier")
	create.Flags().StringSliceVar(&capabilities, "capability", []string{"text"}, "model capability (repeatable)")
	create.Flags().Int32Var(&contextWindow, "context-window", 0, "context window size")
	create.Flags().BoolVar(&defaultModel, "default", false, "make this the enterprise default model")
	create.Flags().BoolVar(&enabled, "enabled", true, "make this model available for authorization")
	_ = create.MarkFlagRequired("model-id")
	_ = create.MarkFlagRequired("display-name")

	var showID string
	show := &cobra.Command{Use: "show", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodGet, "/aep/v1/admin/models/"+segment(showID), nil, true)
		return output(value, err)
	})}
	show.Flags().StringVar(&showID, "model-id", "", "model identifier")
	_ = show.MarkFlagRequired("model-id")

	var updateID, updateDisplayName, updateEndpoint, updateUpstreamModel, updateLocalModelRef, updateCredentialID string
	var updateCapabilities []string
	var updateContextWindow int32
	var updateDefault, updateEnabled bool
	update := &cobra.Command{Use: "update", RunE: authenticated(opts, func(api *client, command *cobra.Command, _ []string) error {
		body := map[string]any{}
		fields := map[string]struct {
			jsonName string
			value    any
		}{
			"display-name":    {"displayName", updateDisplayName},
			"endpoint":        {"endpoint", updateEndpoint},
			"upstream-model":  {"upstreamModel", updateUpstreamModel},
			"local-model-ref": {"localModelRef", updateLocalModelRef},
			"capability":      {"capabilities", updateCapabilities},
			"context-window":  {"contextWindow", updateContextWindow},
			"default":         {"isDefault", updateDefault},
			"enabled":         {"enabled", updateEnabled},
		}
		for flag, field := range fields {
			if command.Flags().Changed(flag) {
				body[field.jsonName] = field.value
			}
		}
		if command.Flags().Changed("credential-id") {
			if updateCredentialID == "" {
				body["credentialId"] = nil
			} else {
				body["credentialId"] = updateCredentialID
			}
		}
		if len(body) == 0 {
			return errors.New("at least one model field must be changed")
		}
		value, err := api.request(http.MethodPatch, "/aep/v1/admin/models/"+segment(updateID), body, true)
		return output(value, err)
	})}
	update.Flags().StringVar(&updateID, "model-id", "", "model identifier")
	update.Flags().StringVar(&updateDisplayName, "display-name", "", "display name")
	update.Flags().StringVar(&updateEndpoint, "endpoint", "", "upstream endpoint or gateway route")
	update.Flags().StringVar(&updateUpstreamModel, "upstream-model", "", "upstream model identifier")
	update.Flags().StringVar(&updateLocalModelRef, "local-model-ref", "", "local client model reference")
	update.Flags().StringVar(&updateCredentialID, "credential-id", "", "credential identifier; empty clears it")
	update.Flags().StringSliceVar(&updateCapabilities, "capability", nil, "replacement model capabilities")
	update.Flags().Int32Var(&updateContextWindow, "context-window", 0, "context window size")
	update.Flags().BoolVar(&updateDefault, "default", false, "set default model status")
	update.Flags().BoolVar(&updateEnabled, "enabled", true, "set availability")
	_ = update.MarkFlagRequired("model-id")

	var deleteID string
	remove := &cobra.Command{Use: "delete", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		_, err := api.request(http.MethodDelete, "/aep/v1/admin/models/"+segment(deleteID), nil, true)
		if err == nil {
			fmt.Println("model deleted")
		}
		return err
	})}
	remove.Flags().StringVar(&deleteID, "model-id", "", "model identifier")
	_ = remove.MarkFlagRequired("model-id")

	var assignModelID, subjectType, subjectID string
	assign := &cobra.Command{Use: "assign", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodPost, "/aep/v1/admin/model-assignments", map[string]any{
			"modelId": assignModelID, "subject": map[string]string{"type": subjectType, "id": subjectID},
		}, true)
		return output(value, err)
	})}
	assign.Flags().StringVar(&assignModelID, "model-id", "", "model identifier")
	assign.Flags().StringVar(&subjectType, "subject-type", "user", "enterprise, organization, user, or agent")
	assign.Flags().StringVar(&subjectID, "subject-id", "", "subject identifier")
	_ = assign.MarkFlagRequired("model-id")
	_ = assign.MarkFlagRequired("subject-id")

	var assignmentID string
	revoke := &cobra.Command{Use: "revoke", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		_, err := api.request(http.MethodDelete, "/aep/v1/admin/model-assignments/"+segment(assignmentID), nil, true)
		if err == nil {
			fmt.Println("assignment removed")
		}
		return err
	})}
	revoke.Flags().StringVar(&assignmentID, "assignment-id", "", "assignment identifier")
	_ = revoke.MarkFlagRequired("assignment-id")

	command.AddCommand(create, show, update, remove, assign, revoke)
	return command
}
