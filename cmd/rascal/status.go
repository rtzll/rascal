package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/rtzll/rascal/internal/api"
	"github.com/spf13/cobra"
)

func (a *app) newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show system readiness",
		Example: strings.TrimSpace(`
  rascal status
  rascal status --output json
`),
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			resp, err := a.client.do(http.MethodGet, "/v1/status", nil)
			if err != nil {
				if a.output == "table" {
					tw := tabwriter.NewWriter(os.Stdout, 0, 4, 4, ' ', 0)
					if _, werr := fmt.Fprintf(tw, "daemon\tunreachable\t%s\n", a.cfg.ServerURL); werr != nil {
						return fmt.Errorf("write status: %w", werr)
					}
					if _, werr := fmt.Fprintln(tw, "ready\tunknown"); werr != nil {
						return fmt.Errorf("write status: %w", werr)
					}
					if _, werr := fmt.Fprintln(tw, "agents\tunknown"); werr != nil {
						return fmt.Errorf("write status: %w", werr)
					}
					tw.Flush() //nolint:errcheck
				}
				return &cliError{Code: exitServer, Message: "daemon unreachable", Cause: err}
			}
			defer closeWithLog("close status response body", resp.Body)
			if resp.StatusCode >= 300 {
				return decodeServerError(resp)
			}
			var out api.SystemStatusResponse
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				return &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
			}
			if err := emit(a, out, func() error {
				tw := tabwriter.NewWriter(os.Stdout, 0, 4, 4, ' ', 0)
				if _, err := fmt.Fprintf(tw, "daemon\tok\t%s\n", a.cfg.ServerURL); err != nil {
					return fmt.Errorf("write status: %w", err)
				}
				if out.Ready {
					if _, err := fmt.Fprintln(tw, "ready\tyes"); err != nil {
						return fmt.Errorf("write status: %w", err)
					}
				} else {
					if _, err := fmt.Fprintln(tw, "ready\tno\tdraining"); err != nil {
						return fmt.Errorf("write status: %w", err)
					}
				}
				switch out.ActiveCredentials {
				case 0:
					if _, err := fmt.Fprintln(tw, "agents\tnone\tno credentials configured"); err != nil {
						return fmt.Errorf("write status: %w", err)
					}
				case 1:
					if _, err := fmt.Fprintln(tw, "agents\t1 active credential"); err != nil {
						return fmt.Errorf("write status: %w", err)
					}
				default:
					if _, err := fmt.Fprintf(tw, "agents\t%d active credentials\n", out.ActiveCredentials); err != nil {
						return fmt.Errorf("write status: %w", err)
					}
				}
				return tw.Flush()
			}); err != nil {
				return err
			}
			if !out.Ready {
				return &cliError{Code: exitRuntime, Message: "daemon not ready"}
			}
			return nil
		},
	}
}
