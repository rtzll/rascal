package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
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

type hetznerProvisionInput struct {
	Token         string
	Name          string
	ServerType    string
	Location      string
	Image         string
	SSHKeyName    string
	SSHPublicPath string
	FirewallName  string
	ApplyFirewall bool
	Timeout       time.Duration
}

func resolveHetznerProvisionConfig(input hetznerProvisionInput) (hcloudProvisionConfig, time.Duration, error) {
	cfg := hcloudProvisionConfig{
		Token:         strings.TrimSpace(input.Token),
		ServerName:    firstNonEmpty(strings.TrimSpace(input.Name), fmt.Sprintf("rascal-%d", time.Now().UTC().Unix())),
		ServerType:    firstNonEmpty(strings.TrimSpace(input.ServerType), "cx23"),
		Location:      firstNonEmpty(strings.TrimSpace(input.Location), "fsn1"),
		Image:         firstNonEmpty(strings.TrimSpace(input.Image), "ubuntu-24.04"),
		SSHKeyName:    firstNonEmpty(strings.TrimSpace(input.SSHKeyName), "rascal"),
		SSHPublicPath: firstNonEmpty(strings.TrimSpace(input.SSHPublicPath), "~/.ssh/id_ed25519.pub"),
		FirewallName:  firstNonEmpty(strings.TrimSpace(input.FirewallName), "rascal-fw"),
		ApplyFirewall: input.ApplyFirewall,
	}
	timeout := input.Timeout
	if timeout <= 0 {
		timeout = 8 * time.Minute
	}
	publicPath, err := expandPath(cfg.SSHPublicPath)
	if err != nil {
		return hcloudProvisionConfig{}, 0, err
	}
	cfg.SSHPublicPath = publicPath
	return cfg, timeout, nil
}

func runHetznerProvision(cfg hcloudProvisionConfig, timeout time.Duration) (hcloudProvisionResult, error) {
	if timeout <= 0 {
		timeout = 8 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return provisionHetznerServer(ctx, cfg)
}

func (a *app) newInfraCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "infra",
		Short: "Infrastructure operations (provisioning/deploy)",
		Long:  "Provision and deploy Rascal infrastructure resources.",
		Example: strings.TrimSpace(`
rascal infra provision-hetzner --token "$HCLOUD_TOKEN"
rascal infra deploy-existing --host 203.0.113.10 --ssh-key ~/.ssh/id_ed25519
`),
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
			cfg, timeout, err := resolveHetznerProvisionConfig(hetznerProvisionInput{
				Token:         token,
				Name:          name,
				ServerType:    serverType,
				Location:      location,
				Image:         image,
				SSHKeyName:    sshKeyName,
				SSHPublicPath: sshPublicPath,
				FirewallName:  firewallName,
				ApplyFirewall: applyFirewall,
				Timeout:       timeout,
			})
			if err != nil {
				return &cliError{Code: exitInput, Message: "invalid ssh public key path", Cause: err}
			}

			out, err := runHetznerProvision(cfg, timeout)
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
	return a.newDeployExistingCmd("deploy-existing", "Deploy rascald to an existing Linux host over SSH")
}

func (a *app) newDeployExistingCmd(use, short string) *cobra.Command {
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
		runnerImage        string
		uploadEnv          bool
		uploadAuth         bool
	)

	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(_ *cobra.Command, _ []string) error {
			result, err := a.runDeployExisting(deployExistingInput{
				Host:               host,
				SSHUser:            sshUser,
				SSHKey:             sshKey,
				SSHPort:            sshPort,
				GOARCH:             goarch,
				APIToken:           apiToken,
				GitHubRuntimeToken: githubRuntimeToken,
				WebhookSecret:      webhookSecret,
				CodexAuthPath:      codexAuthPath,
				Domain:             domain,
				RunnerImage:        runnerImage,
				SkipEnvUpload:      !uploadEnv,
				SkipAuthUpload:     !uploadAuth,
			})
			if err != nil {
				return err
			}
			return a.emit(map[string]any{
				"host":       result.Host,
				"server_url": result.ServerURL,
				"api_token":  maskSecret(result.APIToken),
			}, func() error {
				a.println("deployed rascald to %s", result.Host)
				a.println("server_url: %s", result.ServerURL)
				if strings.TrimSpace(result.APIToken) != "" {
					a.println("api_token: %s", maskSecret(result.APIToken))
				} else {
					a.println("api_token: unchanged on remote")
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "existing server host (defaults to config host/ssh_host)")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "root", "SSH target user")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	cmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH target port")
	cmd.Flags().StringVar(&goarch, "goarch", "", "GOARCH for rascald binary (auto-detected when empty)")
	cmd.Flags().StringVar(&apiToken, "api-token", "", "orchestrator API token")
	cmd.Flags().StringVar(&githubRuntimeToken, "github-runtime-token", "", "GitHub runtime token")
	cmd.Flags().StringVar(&webhookSecret, "webhook-secret", "", "GitHub webhook secret")
	cmd.Flags().StringVar(&codexAuthPath, "codex-auth", "~/.codex/auth.json", "local Codex auth.json path")
	cmd.Flags().StringVar(&domain, "domain", "", "public domain for TLS/Caddy")
	cmd.Flags().StringVar(&runnerImage, "runner-image", "rascal-runner:latest", "runner docker image tag")
	cmd.Flags().BoolVar(&uploadEnv, "upload-env", false, "upload/update /etc/rascal/rascal.env on server")
	cmd.Flags().BoolVar(&uploadAuth, "upload-auth", false, "upload/update codex auth file on server")
	return cmd
}

type deployExistingInput struct {
	Host               string
	SSHUser            string
	SSHKey             string
	SSHPort            int
	GOARCH             string
	ProvisionedArch    string
	APIToken           string
	GitHubRuntimeToken string
	WebhookSecret      string
	CodexAuthPath      string
	Domain             string
	RunnerImage        string
	SkipEnvUpload      bool
	SkipAuthUpload     bool
	SkipIfHealthy      bool
	RawErrors          bool
}

type deployExistingResult struct {
	Host            string
	ServerURL       string
	APIToken        string
	DeployPerformed bool
}

var (
	deployToExistingHostFn        = deployToExistingHost
	runRemoteDoctorFn             = runRemoteDoctor
	remoteCaddyDomainConfiguredFn = remoteCaddyDomainConfigured
)

func (a *app) runDeployExisting(input deployExistingInput) (deployExistingResult, error) {
	host := firstNonEmpty(strings.TrimSpace(input.Host), strings.TrimSpace(a.cfg.Host), strings.TrimSpace(a.cfg.SSHHost))
	sshUser := strings.TrimSpace(input.SSHUser)
	sshKey := strings.TrimSpace(input.SSHKey)
	goarch := strings.TrimSpace(input.GOARCH)
	provisionedArch := strings.TrimSpace(input.ProvisionedArch)
	codexAuthPath := strings.TrimSpace(input.CodexAuthPath)
	domain := firstNonEmpty(strings.TrimSpace(input.Domain), strings.TrimSpace(a.cfg.Domain))
	runnerImage := firstNonEmpty(strings.TrimSpace(input.RunnerImage), "rascal-runner:latest")
	sshPort := input.SSHPort
	apiToken := strings.TrimSpace(input.APIToken)
	githubRuntimeToken := strings.TrimSpace(input.GitHubRuntimeToken)
	webhookSecret := strings.TrimSpace(input.WebhookSecret)

	if host == "" {
		return deployExistingResult{}, &cliError{Code: exitInput, Message: "--host is required (or set config host)"}
	}
	if sshPort <= 0 {
		return deployExistingResult{}, &cliError{Code: exitInput, Message: "--ssh-port must be positive"}
	}

	expandedAuthPath := ""
	if !input.SkipAuthUpload {
		if codexAuthPath == "" {
			return deployExistingResult{}, &cliError{Code: exitInput, Message: "--codex-auth must be set when --upload-auth is used"}
		}
		var err error
		expandedAuthPath, err = expandPath(codexAuthPath)
		if err != nil {
			return deployExistingResult{}, &cliError{Code: exitInput, Message: "invalid --codex-auth path", Cause: err}
		}
		if _, err := os.Stat(expandedAuthPath); err != nil {
			return deployExistingResult{}, &cliError{Code: exitInput, Message: "codex auth file is required", Hint: "run `codex login` first", Cause: err}
		}
	}

	if !input.SkipEnvUpload {
		apiToken = firstNonEmpty(apiToken, a.cfg.APIToken)
		if apiToken == "" {
			created, err := randomToken(32)
			if err != nil {
				return deployExistingResult{}, err
			}
			apiToken = created
		}
		githubRuntimeToken = firstNonEmpty(githubRuntimeToken, strings.TrimSpace(os.Getenv("GITHUB_RUNTIME_TOKEN")), strings.TrimSpace(os.Getenv("RASCAL_GITHUB_RUNTIME_TOKEN")))
		if githubRuntimeToken == "" {
			return deployExistingResult{}, &cliError{Code: exitInput, Message: "--github-runtime-token is required when --upload-env is used"}
		}
		if webhookSecret == "" {
			created, err := randomToken(32)
			if err != nil {
				return deployExistingResult{}, err
			}
			webhookSecret = created
		}
	}

	resolvedGoarch := goarch
	if resolvedGoarch == "" {
		switch {
		case provisionedArch != "":
			detected, ok := goarchFromHetznerArchitecture(provisionedArch)
			if ok {
				resolvedGoarch = detected
				a.println("detected goarch: %s (hcloud architecture: %s)", resolvedGoarch, provisionedArch)
				break
			}
			detected, err := detectRemoteGOARCH(deployConfig{
				Host:       host,
				SSHUser:    firstNonEmpty(sshUser, "root"),
				SSHKeyPath: sshKey,
				SSHPort:    sshPort,
			})
			if err != nil {
				return deployExistingResult{}, fmt.Errorf("auto-detect goarch: unable to map Hetzner architecture %q and ssh detection failed: %w", provisionedArch, err)
			}
			resolvedGoarch = detected
			a.println("detected goarch: %s (remote host)", resolvedGoarch)
		default:
			detected, err := detectRemoteGOARCH(deployConfig{
				Host:       host,
				SSHUser:    firstNonEmpty(sshUser, "root"),
				SSHKeyPath: sshKey,
				SSHPort:    sshPort,
			})
			if err != nil {
				if input.RawErrors {
					return deployExistingResult{}, fmt.Errorf("auto-detect goarch: %w", err)
				}
				return deployExistingResult{}, &cliError{Code: exitRuntime, Message: "auto-detect goarch failed", Hint: "set --goarch explicitly", Cause: err}
			}
			resolvedGoarch = detected
			a.println("detected goarch: %s (remote host)", resolvedGoarch)
		}
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
		RunnerImage:        runnerImage,
		ServerListenAddr:   ":8080",
		ServerDataDir:      "/var/lib/rascal",
		ServerStatePath:    "/var/lib/rascal/state.db",
		ServerCodexAuthDst: "/etc/rascal/codex_auth.json",
		GOARCH:             resolvedGoarch,
		Domain:             domain,
		UploadEnvFile:      !input.SkipEnvUpload,
		UploadCodexAuth:    !input.SkipAuthUpload,
	}
	healthyExisting := false
	if input.SkipIfHealthy {
		if st, err := runRemoteDoctorFn(deployConfig{
			Host:       cfg.Host,
			SSHUser:    cfg.SSHUser,
			SSHKeyPath: cfg.SSHKeyPath,
			SSHPort:    cfg.SSHPort,
		}); err == nil {
			caddyOK := st.CaddyInstalled || strings.TrimSpace(domain) == ""
			if caddyOK && strings.TrimSpace(domain) != "" {
				configured, err := remoteCaddyDomainConfiguredFn(deployConfig{
					Host:       cfg.Host,
					SSHUser:    cfg.SSHUser,
					SSHKeyPath: cfg.SSHKeyPath,
					SSHPort:    cfg.SSHPort,
				}, domain)
				if err != nil || !configured {
					caddyOK = false
				}
			}
			healthyExisting = st.RascalService && st.DockerInstalled && st.SQLiteInstalled && caddyOK && st.EnvFilePresent && st.AuthRuntimeSynced && st.CodexAuthPresent && st.RunnerImagePresent
		}
	}
	deployPerformed := false
	if !healthyExisting {
		if err := deployToExistingHostFn(cfg); err != nil {
			if input.RawErrors {
				return deployExistingResult{}, err
			}
			return deployExistingResult{}, &cliError{Code: exitRuntime, Message: "deploy failed", Cause: err}
		}
		deployPerformed = true
	} else if input.SkipIfHealthy {
		a.println("existing deployment detected on %s; skipping deploy", host)
	}

	serverURL := firstNonEmpty(strings.TrimSpace(a.cfg.ServerURL), "http://"+host+":8080")
	if domain != "" {
		serverURL = "https://" + domain
	}
	return deployExistingResult{
		Host:            host,
		ServerURL:       serverURL,
		APIToken:        apiToken,
		DeployPerformed: deployPerformed,
	}, nil
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
			if hcloud.IsError(err, hcloud.ErrorCodeUniquenessError) {
				existing, lookupErr := findSSHKeyByPublicKey(ctx, client, sshPublic)
				if lookupErr != nil {
					return hcloudProvisionResult{}, fmt.Errorf("create ssh key: %w (lookup existing key failed: %v)", err, lookupErr)
				}
				if existing != nil {
					sshKey = existing
				} else {
					return hcloudProvisionResult{}, fmt.Errorf("create ssh key: %w", err)
				}
			} else {
				return hcloudProvisionResult{}, fmt.Errorf("create ssh key: %w", err)
			}
		} else {
			sshKey = created
		}
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

func findSSHKeyByPublicKey(ctx context.Context, client *hcloud.Client, publicKey string) (*hcloud.SSHKey, error) {
	want := normalizeAuthorizedPublicKey(publicKey)
	if want == "" {
		return nil, fmt.Errorf("invalid ssh public key")
	}
	keys, err := client.SSHKey.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list ssh keys: %w", err)
	}
	for _, key := range keys {
		if normalizeAuthorizedPublicKey(key.PublicKey) == want {
			return key, nil
		}
	}
	return nil, nil
}

func normalizeAuthorizedPublicKey(raw string) string {
	fields := strings.Fields(strings.TrimSpace(raw))
	for i := 0; i+1 < len(fields); i++ {
		if !looksLikeSSHKeyType(fields[i]) {
			continue
		}
		if !looksLikeSSHKeyMaterial(fields[i+1]) {
			continue
		}
		return fields[i] + " " + fields[i+1]
	}
	return ""
}

func looksLikeSSHKeyType(s string) bool {
	return strings.HasPrefix(s, "ssh-") || strings.HasPrefix(s, "ecdsa-") || strings.HasPrefix(s, "sk-")
}

func looksLikeSSHKeyMaterial(s string) bool {
	if s == "" {
		return false
	}
	if _, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return true
	}
	if _, err := base64.StdEncoding.DecodeString(s); err == nil {
		return true
	}
	return false
}
