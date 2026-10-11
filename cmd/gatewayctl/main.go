package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/spf13/cobra"
)

const maxResponseSize = 2 << 20

type apiClient struct {
	baseURL string
	token   string
	http    *http.Client
}

type device struct {
	ID        string  `json:"id"`
	NodeID    string  `json:"node_id"`
	Name      string  `json:"name"`
	CreatedAt string  `json:"created_at"`
	RevokedAt *string `json:"revoked_at"`
}

type node struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	ServerName string `json:"server_name"`
}

type gatewayStatus struct {
	Database        string `json:"database"`
	NodeID          string `json:"node_id"`
	Devices         int    `json:"devices"`
	ActiveDevices   int    `json:"active_devices"`
	DesiredRevision int64  `json:"desired_revision"`
	AppliedRevision int64  `json:"applied_revision"`
	XrayConfigured  bool   `json:"xray_configured"`
}

type browserStatus struct {
	Running bool   `json:"running"`
	URL     string `json:"url"`
}

func main() {
	command := newRootCommand(os.Getenv, &http.Client{Timeout: 70 * time.Second})
	if err := command.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "gatewayctl:", err)
		os.Exit(1)
	}
}

func newRootCommand(getenv func(string) string, client *http.Client) *cobra.Command {
	baseURL := getenv("GATEWAY_API_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	api := &apiClient{http: client}
	root := &cobra.Command{
		Use:           "gatewayctl",
		Short:         "Manage your Private Gateway",
		Long:          "Manage devices, Xray configuration and the remote browser.\nSet GATEWAY_API_TOKEN in your environment; token flags are intentionally not supported.",
		SilenceUsage:  true,
		SilenceErrors: true,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			parsed, err := url.Parse(baseURL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return errors.New("API URL must be an http(s) address without credentials, query parameters or fragments")
			}
			api.baseURL = strings.TrimRight(baseURL, "/") + "/api/v1"
			api.token = strings.TrimSpace(getenv("GATEWAY_API_TOKEN"))
			if api.token == "" {
				return errors.New("GATEWAY_API_TOKEN is required; configure it in your environment")
			}
			return nil
		},
	}
	root.PersistentFlags().StringVar(&baseURL, "url", baseURL, "Gateway base URL (or GATEWAY_API_URL)")
	root.AddCommand(statusCommand(api), deviceCommand(api), nodeCommand(api), configCommand(api), browserCommand(api), auditCommand(api))
	return root
}

func statusCommand(api *apiClient) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show gateway health and configuration revision",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var result gatewayStatus
			if err := api.request(cmd.Context(), http.MethodGet, "/status", nil, &result); err != nil {
				return err
			}
			writer := table(cmd.OutOrStdout())
			fmt.Fprintf(writer, "DATABASE\t%s\nNODE\t%s\nDEVICES\t%d\nACTIVE DEVICES\t%d\nXRAY CONFIGURED\t%t\nAPPLIED REVISION\t%d\nDESIRED REVISION\t%d\n", printable(result.Database), printable(result.NodeID), result.Devices, result.ActiveDevices, result.XrayConfigured, result.AppliedRevision, result.DesiredRevision)
			if result.DesiredRevision != result.AppliedRevision {
				fmt.Fprintln(writer, "CONFIGURATION\tpending; run gatewayctl config validate, then gatewayctl config apply")
			}
			return writer.Flush()
		},
	}
}

func deviceCommand(api *apiClient) *cobra.Command {
	command := &cobra.Command{Use: "device", Short: "Create, export and revoke device access"}
	var nodeID string
	add := &cobra.Command{
		Use:   "add NAME --node ID",
		Short: "Create a device; apply Xray configuration afterward",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" {
				return errors.New("device name must not be empty")
			}
			if strings.TrimSpace(nodeID) == "" {
				return errors.New("--node must not be empty; use gatewayctl node list to find an ID")
			}
			var result device
			if err := api.request(cmd.Context(), http.MethodPost, "/devices", map[string]string{"name": name, "node_id": nodeID}, &result); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), printable(result.ID))
			fmt.Fprintln(cmd.ErrOrStderr(), "Device created. Run gatewayctl config validate and gatewayctl config apply to enable access.")
			return nil
		},
	}
	add.Flags().StringVar(&nodeID, "node", "", "Node ID (see node list)")
	_ = add.MarkFlagRequired("node")
	list := &cobra.Command{
		Use: "list", Short: "List devices without exposing credentials", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var result struct {
				Devices []device `json:"devices"`
			}
			if err := api.request(cmd.Context(), http.MethodGet, "/devices", nil, &result); err != nil {
				return err
			}
			writer := table(cmd.OutOrStdout())
			fmt.Fprintln(writer, "ID\tNAME\tNODE\tACCESS\tCREATED")
			for _, item := range result.Devices {
				access := "active"
				if item.RevokedAt != nil {
					access = "revoked"
				}
				fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", printable(item.ID), printable(item.Name), printable(item.NodeID), access, printable(item.CreatedAt))
			}
			return writer.Flush()
		},
	}
	export := &cobra.Command{
		Use: "export ID", Short: "Print the sensitive VLESS URI for an active device", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var result struct {
				URI string `json:"uri"`
			}
			if err := api.request(cmd.Context(), http.MethodGet, "/devices/"+url.PathEscape(args[0])+"/profile", nil, &result); err != nil {
				return err
			}
			parsed, parseErr := url.Parse(result.URI)
			if parseErr != nil || parsed.Scheme != "vless" || parsed.Host == "" || parsed.User == nil || strings.ContainsAny(result.URI, "\r\n") {
				return errors.New("server returned an invalid profile URI")
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), result.URI)
			return err
		},
	}
	revoke := &cobra.Command{
		Use: "revoke ID", Short: "Revoke a device; apply Xray configuration afterward", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var result device
			if err := api.request(cmd.Context(), http.MethodPost, "/devices/"+url.PathEscape(args[0])+"/revoke", nil, &result); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Revoked", printable(result.ID))
			fmt.Fprintln(cmd.ErrOrStderr(), "Run gatewayctl config validate and gatewayctl config apply to disconnect the device from Xray.")
			return nil
		},
	}
	command.AddCommand(add, list, export, revoke)
	return command
}

func nodeCommand(api *apiClient) *cobra.Command {
	command := &cobra.Command{Use: "node", Short: "Inspect available connection nodes"}
	command.AddCommand(&cobra.Command{
		Use: "list", Short: "List nodes and their public connection address", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var result struct {
				Nodes []node `json:"nodes"`
			}
			if err := api.request(cmd.Context(), http.MethodGet, "/nodes", nil, &result); err != nil {
				return err
			}
			writer := table(cmd.OutOrStdout())
			fmt.Fprintln(writer, "ID\tNAME\tADDRESS\tSERVER NAME")
			for _, item := range result.Nodes {
				fmt.Fprintf(writer, "%s\t%s\t%s:%d\t%s\n", printable(item.ID), printable(item.Name), printable(item.Host), item.Port, printable(item.ServerName))
			}
			return writer.Flush()
		},
	})
	return command
}

func auditCommand(api *apiClient) *cobra.Command {
	return &cobra.Command{
		Use: "audit", Short: "Show gateway action history without credentials", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var result struct {
				Events []struct {
					ID        int64   `json:"id"`
					Action    string  `json:"action"`
					DeviceID  *string `json:"device_id"`
					CreatedAt string  `json:"created_at"`
				} `json:"events"`
			}
			if err := api.request(cmd.Context(), http.MethodGet, "/audit", nil, &result); err != nil {
				return err
			}
			writer := table(cmd.OutOrStdout())
			fmt.Fprintln(writer, "ID\tACTION\tDEVICE\tCREATED")
			for _, event := range result.Events {
				deviceID := "—"
				if event.DeviceID != nil {
					deviceID = printable(*event.DeviceID)
				}
				fmt.Fprintf(writer, "%d\t%s\t%s\t%s\n", event.ID, printable(event.Action), deviceID, printable(event.CreatedAt))
			}
			return writer.Flush()
		},
	}
}

func configCommand(api *apiClient) *cobra.Command {
	command := &cobra.Command{Use: "config", Short: "Validate or apply generated Xray configuration"}
	for _, action := range []string{"validate", "apply"} {
		command.AddCommand(&cobra.Command{
			Use: action, Short: strings.ToUpper(action[:1]) + action[1:] + " Xray configuration", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				var result struct {
					Status string `json:"status"`
				}
				if err := api.request(cmd.Context(), http.MethodPost, "/config/"+action, nil, &result); err != nil {
					return err
				}
				_, err := fmt.Fprintln(cmd.OutOrStdout(), printable(result.Status))
				return err
			},
		})
	}
	return command
}

func browserCommand(api *apiClient) *cobra.Command {
	command := &cobra.Command{Use: "browser", Short: "Manage the remote browser (access its UI through an SSH tunnel)"}
	for _, action := range []string{"status", "start", "stop"} {
		command.AddCommand(&cobra.Command{
			Use: action, Short: strings.ToUpper(action[:1]) + action[1:] + " remote browser", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				method, path := http.MethodPost, "/browser/"+action
				if action == "status" {
					method, path = http.MethodGet, "/browser"
				}
				var result browserStatus
				if err := api.request(cmd.Context(), method, path, nil, &result); err != nil {
					return err
				}
				writer := table(cmd.OutOrStdout())
				fmt.Fprintf(writer, "RUNNING\t%t\n", result.Running)
				if result.URL != "" {
					fmt.Fprintf(writer, "URL\t%s\n", printable(result.URL))
				}
				return writer.Flush()
			},
		})
	}
	return command
}

func (api *apiClient) request(ctx context.Context, method, path string, body any, result any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, api.baseURL+path, reader)
	if err != nil {
		return errors.New("could not create API request")
	}
	req.Header.Set("Authorization", "Bearer "+api.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := api.http.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach gateway API: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return fmt.Errorf("read API response: %w", err)
	}
	if len(data) > maxResponseSize {
		return errors.New("API response exceeds 2 MiB")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return fmt.Errorf("API access denied (HTTP %d); check GATEWAY_API_TOKEN", response.StatusCode)
		}
		var problem struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &problem) == nil && problem.Error != "" {
			return fmt.Errorf("API error (HTTP %d): %s", response.StatusCode, printable(problem.Error))
		}
		return fmt.Errorf("API returned HTTP %d", response.StatusCode)
	}
	if err := json.Unmarshal(data, result); err != nil {
		return errors.New("API returned invalid JSON")
	}
	return nil
}

func table(out io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
}

func printable(value string) string {
	return strings.Map(func(char rune) rune {
		if unicode.IsControl(char) {
			return ' '
		}
		return char
	}, value)
}
