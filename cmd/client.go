package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	cli "github.com/forkbombeu/credimi-runner/pkg/gen/http/cli/runner_server"
	"github.com/spf13/cobra"
	goahttp "goa.design/goa/v3/http"
)

var (
	clientURL     string
	clientHost    string
	clientPort    int
	clientSecure  bool
	clientTimeout int
	clientVerbose bool
)

var parseEndpoint = cli.ParseEndpoint

var clientCmd = &cobra.Command{
	Use:   "client [SERVICE] [ENDPOINT] [flags]",
	Short: "Call the Runner Server API (Goa generated client)",
	Long: `Calls the Runner Server API using the Goa generated CLI parser.

Examples:
  credimi-runner client --url http://127.0.0.1:8050 runner process-list
  credimi-runner client --host 127.0.0.1 --port 8050 runner fetch-apk-and-action --body '{"instance_url":"...","version_identifier":"v1"}'
`,
	Args: cobra.MinimumNArgs(2), // SERVICE + ENDPOINT at least
	RunE: func(cmd *cobra.Command, args []string) error {
		baseURL, err := resolveClientBaseURL()
		if err != nil {
			return err
		}

		u, err := url.Parse(baseURL)
		if err != nil {
			return fmt.Errorf("invalid --url %q: %w", baseURL, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("invalid scheme %q (expected http or https)", u.Scheme)
		}
		if u.Host == "" {
			return fmt.Errorf("invalid --url %q: missing host", baseURL)
		}

		// Goa HTTP doer (+ optional debug)
		var doer goahttp.Doer = &http.Client{Timeout: time.Duration(clientTimeout) * time.Second}
		if clientVerbose {
			doer = goahttp.NewDebugDoer(doer)
		}

		origArgs := os.Args
		defer func() { os.Args = origArgs }()
		os.Args = append([]string{origArgs[0]}, args...)

		endpoint, payload, err := parseEndpoint(
			u.Scheme,
			u.Host,
			doer,
			goahttp.RequestEncoder,
			goahttp.ResponseDecoder,
			clientVerbose, // restore/verbose-ish flag used by generated client
		)
		if err != nil {
			return err
		}

		resp, err := endpoint(context.Background(), payload)
		if err != nil {
			return err
		}

		if resp == nil {
			return nil
		}
		b, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal response: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(b))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(clientCmd)

	clientCmd.Flags().StringVar(&clientURL, "url", "", "Service base URL (e.g. http://127.0.0.1:8050). Overrides --host/--port.")
	clientCmd.Flags().StringVar(&clientHost, "host", "127.0.0.1", "Service host (used when --url is not set)")
	clientCmd.Flags().IntVar(&clientPort, "port", 8050, "Service port (used when --url is not set)")
	clientCmd.Flags().BoolVar(&clientSecure, "secure", false, "Use https when --url is not set")
	clientCmd.Flags().IntVar(&clientTimeout, "timeout", 30, "Timeout in seconds")
	clientCmd.Flags().BoolVarP(&clientVerbose, "verbose", "v", false, "Print request and response details")
}

func resolveClientBaseURL() (string, error) {
	if clientURL != "" {
		return clientURL, nil
	}
	if clientHost == "" {
		return "", fmt.Errorf("--host cannot be empty")
	}
	if clientPort <= 0 || clientPort > 65535 {
		return "", fmt.Errorf("--port must be in 1..65535")
	}

	scheme := "http"
	if clientSecure {
		scheme = "https"
	}

	// If host already includes a port, keep it.
	if _, _, err := net.SplitHostPort(clientHost); err == nil {
		return scheme + "://" + clientHost, nil
	}

	return scheme + "://" + clientHost + ":" + strconv.Itoa(clientPort), nil
}
