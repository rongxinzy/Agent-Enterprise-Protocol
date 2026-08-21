package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func credentialCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "credential", Short: "Manage encrypted credentials and assignments"}
	command.AddCommand(&cobra.Command{Use: "list", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodGet, "/aep/v1/admin/credentials", nil, true)
		return output(value, err)
	})})
	command.AddCommand(&cobra.Command{Use: "assignments", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodGet, "/aep/v1/admin/credential-assignments", nil, true)
		return output(value, err)
	})})

	var name, service, credentialType, deliveryMode, rawValue, valueFile string
	var enabled bool
	create := &cobra.Command{Use: "create", RunE: authenticated(opts, func(api *client, command *cobra.Command, _ []string) error {
		secret, err := credentialValue(command, rawValue, valueFile)
		if err != nil {
			return err
		}
		value, err := api.request(http.MethodPost, "/aep/v1/admin/credentials", map[string]any{
			"name": name, "service": service, "type": credentialType,
			"deliveryMode": deliveryMode, "value": secret, "enabled": enabled,
		}, true)
		return output(value, err)
	})}
	create.Flags().StringVar(&name, "name", "", "credential display name")
	create.Flags().StringVar(&service, "service", "", "service identifier")
	create.Flags().StringVar(&credentialType, "type", "api_key", "credential type")
	create.Flags().StringVar(&deliveryMode, "delivery-mode", "server_only", "server_only or agent")
	create.Flags().StringVar(&rawValue, "value", "", "secret value (prefer AEPCTL_CREDENTIAL_VALUE or --value-file)")
	create.Flags().StringVar(&valueFile, "value-file", "", "path containing the secret value")
	create.Flags().BoolVar(&enabled, "enabled", true, "make this credential available")
	_ = create.MarkFlagRequired("name")
	_ = create.MarkFlagRequired("service")

	var showID string
	show := &cobra.Command{Use: "show", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodGet, "/aep/v1/admin/credentials/"+segment(showID), nil, true)
		return output(value, err)
	})}
	show.Flags().StringVar(&showID, "credential-id", "", "credential identifier")
	_ = show.MarkFlagRequired("credential-id")

	var updateID, updateName, updateService, updateDeliveryMode string
	var updateEnabled bool
	update := &cobra.Command{Use: "update", RunE: authenticated(opts, func(api *client, command *cobra.Command, _ []string) error {
		body := map[string]any{}
		fields := map[string]struct {
			jsonName string
			value    any
		}{
			"name":          {"name", updateName},
			"service":       {"service", updateService},
			"delivery-mode": {"deliveryMode", updateDeliveryMode},
			"enabled":       {"enabled", updateEnabled},
		}
		for flag, field := range fields {
			if command.Flags().Changed(flag) {
				body[field.jsonName] = field.value
			}
		}
		if len(body) == 0 {
			return errors.New("at least one credential field must be changed")
		}
		value, err := api.request(http.MethodPatch, "/aep/v1/admin/credentials/"+segment(updateID), body, true)
		return output(value, err)
	})}
	update.Flags().StringVar(&updateID, "credential-id", "", "credential identifier")
	update.Flags().StringVar(&updateName, "name", "", "credential display name")
	update.Flags().StringVar(&updateService, "service", "", "service identifier")
	update.Flags().StringVar(&updateDeliveryMode, "delivery-mode", "", "server_only or agent")
	update.Flags().BoolVar(&updateEnabled, "enabled", true, "set credential availability")
	_ = update.MarkFlagRequired("credential-id")

	var rotateID, rotateValue, rotateValueFile string
	rotate := &cobra.Command{Use: "rotate", RunE: authenticated(opts, func(api *client, command *cobra.Command, _ []string) error {
		secret, err := credentialValue(command, rotateValue, rotateValueFile)
		if err != nil {
			return err
		}
		value, err := api.request(http.MethodPost, "/aep/v1/admin/credentials/"+segment(rotateID)+"/rotate", map[string]any{"value": secret}, true)
		return output(value, err)
	})}
	rotate.Flags().StringVar(&rotateID, "credential-id", "", "credential identifier")
	rotate.Flags().StringVar(&rotateValue, "value", "", "new secret value (prefer AEPCTL_CREDENTIAL_VALUE or --value-file)")
	rotate.Flags().StringVar(&rotateValueFile, "value-file", "", "path containing the new secret value")
	_ = rotate.MarkFlagRequired("credential-id")

	var deleteID string
	remove := &cobra.Command{Use: "delete", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		_, err := api.request(http.MethodDelete, "/aep/v1/admin/credentials/"+segment(deleteID), nil, true)
		if err == nil {
			fmt.Println("credential deleted")
		}
		return err
	})}
	remove.Flags().StringVar(&deleteID, "credential-id", "", "credential identifier")
	_ = remove.MarkFlagRequired("credential-id")

	var assignCredentialID, subjectType, subjectID string
	assign := &cobra.Command{Use: "assign", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodPost, "/aep/v1/admin/credential-assignments", map[string]any{
			"credentialId": assignCredentialID, "subject": map[string]string{"type": subjectType, "id": subjectID},
		}, true)
		return output(value, err)
	})}
	assign.Flags().StringVar(&assignCredentialID, "credential-id", "", "credential identifier")
	assign.Flags().StringVar(&subjectType, "subject-type", "user", "enterprise, organization, user, or agent")
	assign.Flags().StringVar(&subjectID, "subject-id", "", "subject identifier")
	_ = assign.MarkFlagRequired("credential-id")
	_ = assign.MarkFlagRequired("subject-id")

	var assignmentID string
	revoke := &cobra.Command{Use: "revoke", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		_, err := api.request(http.MethodDelete, "/aep/v1/admin/credential-assignments/"+segment(assignmentID), nil, true)
		if err == nil {
			fmt.Println("assignment removed")
		}
		return err
	})}
	revoke.Flags().StringVar(&assignmentID, "assignment-id", "", "assignment identifier")
	_ = revoke.MarkFlagRequired("assignment-id")

	command.AddCommand(create, show, update, rotate, remove, assign, revoke)
	return command
}

func credentialValue(command *cobra.Command, flagValue, path string) (string, error) {
	environmentValue := os.Getenv("AEPCTL_CREDENTIAL_VALUE")
	sources := 0
	if command.Flags().Changed("value") {
		sources++
	}
	if command.Flags().Changed("value-file") {
		sources++
	}
	if environmentValue != "" {
		sources++
	}
	if sources != 1 {
		return "", errors.New("provide exactly one of --value, --value-file, or AEPCTL_CREDENTIAL_VALUE")
	}
	if command.Flags().Changed("value") {
		return flagValue, nil
	}
	if command.Flags().Changed("value-file") {
		content, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return strings.TrimSuffix(strings.TrimSuffix(string(content), "\n"), "\r"), nil
	}
	return environmentValue, nil
}
