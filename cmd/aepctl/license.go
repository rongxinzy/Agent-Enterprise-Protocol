package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

func licenseCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "license", Short: "Manage enterprise licenses"}
	command.AddCommand(&cobra.Command{Use: "list", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodGet, "/aep/v1/admin/licenses", nil, true)
		return output(value, err)
	})})

	var showID string
	show := &cobra.Command{Use: "show", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodGet, "/aep/v1/admin/licenses/"+segment(showID), nil, true)
		return output(value, err)
	})}
	show.Flags().StringVar(&showID, "license-id", "", "license identifier")
	_ = show.MarkFlagRequired("license-id")

	var file string
	importCommand := &cobra.Command{Use: "import", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		raw, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read license file: %w", err)
		}
		var envelope map[string]any
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return errors.New("license file must contain a JSON envelope")
		}
		value, err := api.request(http.MethodPost, "/aep/v1/admin/licenses/import", map[string]any{"license": envelope}, true)
		return output(value, err)
	})}
	importCommand.Flags().StringVar(&file, "file", "", "path to signed .zylic license envelope")
	_ = importCommand.MarkFlagRequired("file")

	var revokeID string
	revoke := &cobra.Command{Use: "revoke", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodPost, "/aep/v1/admin/licenses/"+segment(revokeID)+"/revoke", nil, true)
		return output(value, err)
	})}
	revoke.Flags().StringVar(&revokeID, "license-id", "", "license identifier")
	_ = revoke.MarkFlagRequired("license-id")

	command.AddCommand(show, importCommand, revoke)
	return command
}
