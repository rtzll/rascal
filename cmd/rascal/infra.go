package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/spf13/cobra"
)

type hcloudProvisionConfig struct {
	Token         string
	ServerName    string
	ServerType    string
	Location      string
	Image         string
	SSHKeyName    string
	SSHPublicPath string
	FirewallName  string
	ApplyFirewall bool
}

type hcloudProvisionResult struct {
	ServerID      int64  `json:"server_id"`
	ServerName    string `json:"server_name"`
	Host          string `json:"host"`
	Location      string `json:"location"`
	ServerType    string `json:"server_type"`
	Architecture  string `json:"architecture"`
	Image         string `json:"image"`
	SSHKeyName    string `json:"ssh_key_name"`
	FirewallName  string `json:"firewall_name,omitempty"`
	FirewallID    int64  `json:"firewall_id,omitempty"`
	RootPassword  string `json:"root_password,omitempty"`
	ProvisionedAt string `json:"provisioned_at"`
}

func (a *app) newInfraCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "infra",
		Short: "Infrastructure operations (provisioning/deploy)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(a.newInfraProvisionHetznerCmd())
	cmd.AddCommand(a.newInfraDeployExistingCmd())
	return cmd
}

func (a *app) newInfraProvisionHetznerCmd() *cobra.Command {
	var (
		token         string
		name          string
		serverType    string
		location      string
		image         string
		sshKeyName    string
		sshPublicPath string
		firewallName  string
		applyFirewall bool
		timeout       time.Duration
	)
	cmd := &cobra.Command{
		Use:   "provision-hetzner",
		Short: "Provision a Hetzner Cloud server",
		RunE: func(_ *cobra.Command, _ []string) error {
			token = firstNonEmpty(strings.TrimSpace(token), strings.TrimSpace(os.Getenv("HCLOUD_TOKEN")))
			if token == "" {
				return &cliError{Code: exitInput, Message: "missing Hetzner token", Hint: "set --token or HCLOUD_TOKEN"}
			}
			name = firstNonEmpty(strings.TrimSpace(name), fmt.Sprintf("rascal-%d", time.Now().UTC().Unix()))
			serverType = firstNonEmpty(strings.TrimSpace(serverType), "cx23")
			location = firstNonEmpty(strings.TrimSpace(location), "fsn1")
			image = firstNonEmpty(strings.TrimSpace(image), "ubuntu-24.04")
			sshKeyName = firstNonEmpty(strings.TrimSpace(sshKeyName), "rascal")
			sshPublicPath = firstNonEmpty(strings.TrimSpace(sshPublicPath), "~/.ssh/id_ed25519.pub")
			firewallName = firstNonEmpty(strings.TrimSpace(firewallName), "rascal-fw")
			if timeout <= 0 {
				timeout = 8 * time.Minute
			}

			publicPath, err := expandPath(sshPublicPath)
			if err != nil {
				return &cliError{Code: exitInput, Message: "invalid ssh public key path", Cause: err}
			}

			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			out, err := provisionHetznerServer(ctx, hcloudProvisionConfig{
				Token:         token,
				ServerName:    name,
				ServerType:    serverType,
				Location:      location,
				Image:         image,
				SSHKeyName:    sshKeyName,
				SSHPublicPath: publicPath,
				FirewallName:  firewallName,
				ApplyFirewall: applyFirewall,
			})
			if err != nil {
				return &cliError{Code: exitRuntime, Message: "hetzner provisioning failed", Cause: err}
			}

			return a.emit(map[string]any{"server": out}, func() error {
				a.println("provisioned %s (%d)", out.ServerName, out.ServerID)
				a.println("host: %s", out.Host)
				a.println("location: %s", out.Location)
				a.println("server type: %s", out.ServerType)
				if out.Architecture != "" {
					a.println("architecture: %s", out.Architecture)
				}
				if out.FirewallID > 0 {
					a.println("firewall: %s (%d)", out.FirewallName, out.FirewallID)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "Hetzner Cloud token (or HCLOUD_TOKEN)")
	cmd.Flags().StringVar(&name, "name", "", "server name")
	cmd.Flags().StringVar(&serverType, "server-type", "cx23", "Hetzner server type")
	cmd.Flags().StringVar(&location, "location", "fsn1", "Hetzner location")
	cmd.Flags().StringVar(&image, "image", "ubuntu-24.04", "Hetzner image")
	cmd.Flags().StringVar(&sshKeyName, "ssh-key-name", "rascal", "Hetzner SSH key resource name")
	cmd.Flags().StringVar(&sshPublicPath, "ssh-public-key", "~/.ssh/id_ed25519.pub", "local SSH public key path")
	cmd.Flags().StringVar(&firewallName, "firewall-name", "rascal-fw", "Hetzner firewall resource name")
	cmd.Flags().BoolVar(&applyFirewall, "apply-firewall", true, "create/update and attach a basic firewall (22,80,443)")
	cmd.Flags().DurationVar(&timeout, "timeout", 8*time.Minute, "provision timeout")
	return cmd
}

func (a *app) newInfraDeployExistingCmd() *cobra.Command {
	var (
		host               string
		sshUser            string
		sshKey             string
		sshPort            int
		goarch             string
		apiToken           string
		githubRuntimeToken string
		webhookSecret      string
		codexAuthPath      string
		domain             string
	)

	cmd := &cobra.Command{
		Use:   "deploy-existing",
		Short: "Deploy rascald to an existing Linux host over SSH",
		RunE: func(_ *cobra.Command, _ []string) error {
			host = strings.TrimSpace(host)
			sshUser = strings.TrimSpace(sshUser)
			sshKey = strings.TrimSpace(sshKey)
			goarch = strings.TrimSpace(goarch)
			codexAuthPath = strings.TrimSpace(codexAuthPath)
			domain = strings.TrimSpace(domain)
			if host == "" {
				return &cliError{Code: exitInput, Message: "--host is required"}
			}
			if sshPort <= 0 {
				return &cliError{Code: exitInput, Message: "--ssh-port must be positive"}
			}
			if codexAuthPath == "" {
				return &cliError{Code: exitInput, Message: "--codex-auth must be set"}
			}
			expandedAuthPath, err := expandPath(codexAuthPath)
			if err != nil {
				return &cliError{Code: exitInput, Message: "invalid --codex-auth path", Cause: err}
			}
			if _, err := os.Stat(expandedAuthPath); err != nil {
				return &cliError{Code: exitInput, Message: "codex auth file is required", Hint: "run `codex login` first", Cause: err}
			}
			apiToken = firstNonEmpty(strings.TrimSpace(apiToken), a.cfg.APIToken)
			if apiToken == "" {
				created, err := randomToken(32)
				if err != nil {
					return err
				}
				apiToken = created
			}
			githubRuntimeToken = firstNonEmpty(strings.TrimSpace(githubRuntimeToken), strings.TrimSpace(os.Getenv("GITHUB_RUNTIME_TOKEN")), strings.TrimSpace(os.Getenv("RASCAL_GITHUB_RUNTIME_TOKEN")))
			if githubRuntimeToken == "" {
				return &cliError{Code: exitInput, Message: "--github-runtime-token is required"}
			}
			if webhookSecret == "" {
				created, err := randomToken(32)
				if err != nil {
					return err
				}
				webhookSecret = created
			}

			resolvedGoarch := goarch
			if resolvedGoarch == "" {
				detected, err := detectRemoteGOARCH(deployConfig{
					Host:       host,
					SSHUser:    firstNonEmpty(sshUser, "root"),
					SSHKeyPath: sshKey,
					SSHPort:    sshPort,
				})
				if err != nil {
					return &cliError{Code: exitRuntime, Message: "auto-detect goarch failed", Hint: "set --goarch explicitly", Cause: err}
				}
				resolvedGoarch = detected
				a.println("detected goarch: %s (remote host)", resolvedGoarch)
			}

			cfg := deployConfig{
				Host:               host,
				SSHUser:            firstNonEmpty(sshUser, "root"),
				SSHKeyPath:         sshKey,
				SSHPort:            sshPort,
				APIToken:           apiToken,
				WebhookSecret:      webhookSecret,
				GitHubRuntimeToken: githubRuntimeToken,
				CodexAuthPath:      expandedAuthPath,
				RunnerMode:         "docker",
				RunnerImage:        "rascal-runner:latest",
				ServerListenAddr:   ":8080",
				ServerDataDir:      "/var/lib/rascal",
				ServerStatePath:    "/var/lib/rascal/state.json",
				ServerCodexAuthDst: "/etc/rascal/codex_auth.json",
				GOARCH:             resolvedGoarch,
				Domain:             domain,
			}
			if err := deployToExistingHost(cfg); err != nil {
				return &cliError{Code: exitRuntime, Message: "deploy failed", Cause: err}
			}

			serverURL := firstNonEmpty(strings.TrimSpace(a.cfg.ServerURL), "http://"+host+":8080")
			if domain != "" {
				serverURL = "https://" + domain
			}
			return a.emit(map[string]any{
				"host":       host,
				"server_url": serverURL,
				"api_token":  maskSecret(apiToken),
			}, func() error {
				a.println("deployed rascald to %s", host)
				a.println("server_url: %s", serverURL)
				a.println("api_token: %s", maskSecret(apiToken))
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "existing server host")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "root", "SSH user")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	cmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH port")
	cmd.Flags().StringVar(&goarch, "goarch", "", "GOARCH for rascald binary (auto-detected when empty)")
	cmd.Flags().StringVar(&apiToken, "api-token", "", "orchestrator API token")
	cmd.Flags().StringVar(&githubRuntimeToken, "github-runtime-token", "", "GitHub runtime token")
	cmd.Flags().StringVar(&webhookSecret, "webhook-secret", "", "GitHub webhook secret")
	cmd.Flags().StringVar(&codexAuthPath, "codex-auth", "~/.codex/auth.json", "local Codex auth.json path")
	cmd.Flags().StringVar(&domain, "domain", "", "public domain for TLS/Caddy")
	return cmd
}

func provisionHetznerServer(ctx context.Context, cfg hcloudProvisionConfig) (hcloudProvisionResult, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return hcloudProvisionResult{}, fmt.Errorf("hcloud token is required")
	}
	if strings.TrimSpace(cfg.ServerName) == "" {
		return hcloudProvisionResult{}, fmt.Errorf("server name is required")
	}
	if strings.TrimSpace(cfg.SSHPublicPath) == "" {
		return hcloudProvisionResult{}, fmt.Errorf("ssh public key path is required")
	}
	sshPublicRaw, err := os.ReadFile(cfg.SSHPublicPath)
	if err != nil {
		return hcloudProvisionResult{}, fmt.Errorf("read ssh public key: %w", err)
	}
	sshPublic := strings.TrimSpace(string(sshPublicRaw))
	if sshPublic == "" {
		return hcloudProvisionResult{}, fmt.Errorf("ssh public key is empty: %s", cfg.SSHPublicPath)
	}

	client := hcloud.NewClient(hcloud.WithToken(cfg.Token))

	sshKey, _, err := client.SSHKey.GetByName(ctx, cfg.SSHKeyName)
	if err != nil {
		return hcloudProvisionResult{}, fmt.Errorf("lookup ssh key: %w", err)
	}
	if sshKey == nil {
		created, _, err := client.SSHKey.Create(ctx, hcloud.SSHKeyCreateOpts{
			Name:      cfg.SSHKeyName,
			PublicKey: sshPublic,
		})
		if err != nil {
			return hcloudProvisionResult{}, fmt.Errorf("create ssh key: %w", err)
		}
		sshKey = created
	}

	serverType, _, err := client.ServerType.GetByName(ctx, cfg.ServerType)
	if err != nil {
		return hcloudProvisionResult{}, fmt.Errorf("lookup server type %q: %w", cfg.ServerType, err)
	}
	if serverType == nil {
		return hcloudProvisionResult{}, fmt.Errorf("server type not found: %s", cfg.ServerType)
	}
	location, _, err := client.Location.GetByName(ctx, cfg.Location)
	if err != nil {
		return hcloudProvisionResult{}, fmt.Errorf("lookup location %q: %w", cfg.Location, err)
	}
	if location == nil {
		return hcloudProvisionResult{}, fmt.Errorf("location not found: %s", cfg.Location)
	}
	image, _, err := client.Image.GetByName(ctx, cfg.Image)
	if err != nil {
		return hcloudProvisionResult{}, fmt.Errorf("lookup image %q: %w", cfg.Image, err)
	}
	if image == nil {
		return hcloudProvisionResult{}, fmt.Errorf("image not found: %s", cfg.Image)
	}

	var firewall *hcloud.Firewall
	if cfg.ApplyFirewall {
		firewall, err = ensureHetznerFirewall(ctx, client, cfg.FirewallName)
		if err != nil {
			return hcloudProvisionResult{}, err
		}
	}

	createOpts := hcloud.ServerCreateOpts{
		Name:       cfg.ServerName,
		ServerType: serverType,
		Image:      image,
		SSHKeys:    []*hcloud.SSHKey{sshKey},
		Location:   location,
		PublicNet: &hcloud.ServerCreatePublicNet{
			EnableIPv4: true,
			EnableIPv6: true,
		},
		Labels: map[string]string{
			"managed-by": "rascal",
		},
	}
	if firewall != nil {
		createOpts.Firewalls = []*hcloud.ServerCreateFirewall{{Firewall: *firewall}}
	}

	res, _, err := client.Server.Create(ctx, createOpts)
	if err != nil {
		return hcloudProvisionResult{}, fmt.Errorf("create server: %w", err)
	}

	actions := make([]*hcloud.Action, 0, 1+len(res.NextActions))
	if res.Action != nil {
		actions = append(actions, res.Action)
	}
	actions = append(actions, res.NextActions...)
	if len(actions) > 0 {
		if err := client.Action.WaitFor(ctx, actions...); err != nil {
			return hcloudProvisionResult{}, fmt.Errorf("wait for provisioning actions: %w", err)
		}
	}

	server, _, err := client.Server.GetByID(ctx, res.Server.ID)
	if err != nil {
		return hcloudProvisionResult{}, fmt.Errorf("fetch created server: %w", err)
	}
	if server == nil {
		return hcloudProvisionResult{}, fmt.Errorf("created server is missing")
	}

	host := ""
	if !server.PublicNet.IPv4.IsUnspecified() {
		host = strings.TrimSpace(server.PublicNet.IPv4.IP.String())
	}
	if host == "" && !server.PublicNet.IPv6.IsUnspecified() {
		host = strings.TrimSpace(server.PublicNet.IPv6.IP.String())
	}
	if host == "" {
		return hcloudProvisionResult{}, fmt.Errorf("server has no public IP")
	}

	out := hcloudProvisionResult{
		ServerID:      server.ID,
		ServerName:    server.Name,
		Host:          host,
		Location:      cfg.Location,
		ServerType:    cfg.ServerType,
		Architecture:  strings.TrimSpace(string(serverType.Architecture)),
		Image:         cfg.Image,
		SSHKeyName:    sshKey.Name,
		RootPassword:  strings.TrimSpace(res.RootPassword),
		ProvisionedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if firewall != nil {
		out.FirewallName = firewall.Name
		out.FirewallID = firewall.ID
	}
	return out, nil
}

func ensureHetznerFirewall(ctx context.Context, client *hcloud.Client, firewallName string) (*hcloud.Firewall, error) {
	firewallName = strings.TrimSpace(firewallName)
	if firewallName == "" {
		firewallName = "rascal-fw"
	}
	existing, _, err := client.Firewall.GetByName(ctx, firewallName)
	if err != nil {
		return nil, fmt.Errorf("lookup firewall: %w", err)
	}
	rules := defaultHetznerFirewallRules()
	if existing == nil {
		createResult, _, err := client.Firewall.Create(ctx, hcloud.FirewallCreateOpts{
			Name:  firewallName,
			Rules: rules,
			Labels: map[string]string{
				"managed-by": "rascal",
			},
		})
		if err != nil {
			return nil, fmt.Errorf("create firewall: %w", err)
		}
		if len(createResult.Actions) > 0 {
			if err := client.Action.WaitFor(ctx, createResult.Actions...); err != nil {
				return nil, fmt.Errorf("wait for firewall create actions: %w", err)
			}
		}
		return createResult.Firewall, nil
	}
	actions, _, err := client.Firewall.SetRules(ctx, existing, hcloud.FirewallSetRulesOpts{Rules: rules})
	if err != nil {
		return nil, fmt.Errorf("update firewall rules: %w", err)
	}
	if len(actions) > 0 {
		if err := client.Action.WaitFor(ctx, actions...); err != nil {
			return nil, fmt.Errorf("wait for firewall update actions: %w", err)
		}
	}
	return existing, nil
}

func defaultHetznerFirewallRules() []hcloud.FirewallRule {
	world := defaultFirewallIPNets()
	return []hcloud.FirewallRule{
		{
			Direction: hcloud.FirewallRuleDirectionIn,
			Protocol:  hcloud.FirewallRuleProtocolTCP,
			Port:      ptrString("22"),
			SourceIPs: world,
		},
		{
			Direction: hcloud.FirewallRuleDirectionIn,
			Protocol:  hcloud.FirewallRuleProtocolTCP,
			Port:      ptrString("80"),
			SourceIPs: world,
		},
		{
			Direction: hcloud.FirewallRuleDirectionIn,
			Protocol:  hcloud.FirewallRuleProtocolTCP,
			Port:      ptrString("443"),
			SourceIPs: world,
		},
	}
}

func defaultFirewallIPNets() []net.IPNet {
	_, v4, _ := net.ParseCIDR("0.0.0.0/0")
	_, v6, _ := net.ParseCIDR("::/0")
	return []net.IPNet{*v4, *v6}
}

func ptrString(v string) *string {
	return &v
}

func repoRootPath() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func deployAssetPath(rel string) string {
	return filepath.Join(repoRootPath(), rel)
}
