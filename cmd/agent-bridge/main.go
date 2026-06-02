package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/gitsang/agent-bridge/internal/agent"
	_ "github.com/gitsang/agent-bridge/internal/agent/claude"
	_ "github.com/gitsang/agent-bridge/internal/agent/codex"
	_ "github.com/gitsang/agent-bridge/internal/agent/opencode"
	"github.com/gitsang/agent-bridge/internal/bridge"
	"github.com/gitsang/agent-bridge/internal/conversation"
	"github.com/gitsang/agent-bridge/internal/platform"
	_ "github.com/gitsang/agent-bridge/internal/platform/mattermost"
	_ "github.com/gitsang/agent-bridge/internal/platform/openai_compatible"
	_ "github.com/gitsang/agent-bridge/internal/platform/ume"
	"github.com/gitsang/agent-bridge/internal/version"
	"github.com/gitsang/configer"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

var rootCmd = &cobra.Command{
	Use:   "agent-bridge",
	Short: "Bridge agents to chat apps",
	RunE:  Run,
}

var rootFlags = struct {
	ConfigFile string `json:"config_file" yaml:"config_file"`
}{}

var cfger *configer.Configer

func init() {
	rootCmd.PersistentFlags().StringVarP(&rootFlags.ConfigFile, "config-file", "c",
		"conf/config.yaml", "specify the config file.")

	cfger = configer.New(
		configer.WithTemplate((*Config)(nil)),
		configer.WithEnvBind(
			configer.WithEnvPrefix("AGENT_BRIDGE"),
			configer.WithEnvDelim("__"),
		),
		configer.WithFlagBind(
			configer.WithCommand(rootCmd),
			configer.WithFlagPrefix("ab"),
			configer.WithFlagDelim("."),
		),
	)
}

func Run(cmd *cobra.Command, _ []string) error {
	// Setup
	var c Config
	err := cfger.Load(&c, rootFlags.ConfigFile)
	if err != nil {
		fmt.Println(err)
		os.Exit(-1)
	}

	logHandlers, err := BuildLogHandlers(c)
	if err != nil {
		fmt.Println(err)
		os.Exit(-1)
	}

	logger := slog.New(logHandlers.Get(c.Log.Handlers.Default))
	logger.Debug("Preparing...", versionLog,
		slog.Any("flags", rootFlags),
		slog.Any("config", redactLogValue(c)),
		slog.String("pid", fmt.Sprintf("%d", os.Getpid())),
	)

	// Dependency Injection
	agentClient, err := buildAgentClient(c, logger)
	if err != nil {
		return err
	}
	conversationStore, err := buildConversationStore(c)
	if err != nil {
		return err
	}
	connector := bridge.New(
		bridge.WithLogger(logger),
		bridge.WithAgentClient(agentClient),
		bridge.WithMessageOutputOptions(c.Agent.MessageOutput),
		bridge.WithConversationStore(conversationStore),
		bridge.WithIncludeUserIdentity(c.Conversation.Message.IncludeUserIdentity),
	)

	// Run
	runCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if len(c.Platforms) == 0 {
		return fmt.Errorf("no enabled platforms configured")
	}

	platformInfra := platform.Infrastructure{
		Logger:      logger,
		Version:     version.String(),
		AgentDriver: c.Agent.Driver,
	}
	group, groupCtx := errgroup.WithContext(runCtx)

	instanceNames := make([]string, 0, len(c.Platforms))
	for instanceName := range c.Platforms {
		instanceNames = append(instanceNames, instanceName)
	}
	sort.Strings(instanceNames)

	for _, instanceName := range instanceNames {
		instanceConfigRaw := c.Platforms[instanceName]
		instanceConfigMap, ok := instanceConfigRaw.(map[string]any)
		if !ok {
			return fmt.Errorf("platform %s config must be a map", instanceName)
		}
		if len(instanceConfigMap) != 1 {
			return fmt.Errorf("platform %s config must define exactly one platform type", instanceName)
		}

		for platformType, platformConfigRaw := range instanceConfigMap {
			if platformConfigRaw == nil {
				return fmt.Errorf("platform %s config is nil", instanceName)
			}

			platformFactory, ok := platform.GetPlatformFactory(platformType)
			if !ok {
				return fmt.Errorf("unsupported platform type %q for %q", platformType, instanceName)
			}

			plt, err := platformFactory.Construct(instanceName, platformConfigRaw, platformInfra)
			if err != nil {
				return fmt.Errorf("build platform %s (%s): %w", instanceName, platformType, err)
			}
			if plt == nil {
				return fmt.Errorf("build platform %s (%s): factory returned nil platform", instanceName, platformType)
			}

			currentPlatform := plt
			logPlatformConfig := redactLogValue(platformConfigRaw)
			group.Go(func() error {
				logger := logger.With(
					slog.String("platform_name", instanceName),
					slog.String("platform_type", platformType),
					slog.Any("platform_config", logPlatformConfig),
				)
				defer func() {
					logger.Debug("platform stopped")
				}()
				logger.Debug("starting platform")

				if err := currentPlatform.Serve(groupCtx, connector.Handle); err != nil {
					return fmt.Errorf("%s platform failed: %w", currentPlatform.Name(), err)
				}
				return nil
			})
		}
	}

	return group.Wait()
}

func buildConversationStore(c Config) (conversation.Store, error) {
	storeType := strings.ToLower(strings.TrimSpace(c.Conversation.Store.Type))
	switch storeType {
	case "", "memory":
		return conversation.NewMemoryConversationStore(c.Conversation.Store.TTL, c.Conversation.Store.MaxItems), nil
	case "file":
		return conversation.NewFileConversationStore(c.Conversation.Store.Path, c.Conversation.Store.TTL, c.Conversation.Store.MaxItems)
	case "sqlite":
		return conversation.NewSQLiteConversationStore(c.Conversation.Store.Path, c.Conversation.Store.TTL, c.Conversation.Store.MaxItems)
	default:
		return nil, fmt.Errorf("unsupported conversation store type %q", c.Conversation.Store.Type)
	}
}

func buildAgentClient(c Config, logger *slog.Logger) (bridge.Agent, error) {
	driver := strings.ToLower(strings.TrimSpace(c.Agent.Driver))
	if driver == "" {
		driver = "opencode"
	}

	factory, ok := agent.GetAgentFactory(driver)
	if !ok {
		return nil, fmt.Errorf("unsupported agent driver %q", c.Agent.Driver)
	}

	infra := agent.Infrastructure{
		Logger: logger,
	}

	var configRaw any
	switch driver {
	case "opencode":
		configRaw = map[string]any{
			"base_url": c.Agent.Opencode.BaseURL,
			"username": c.Agent.Opencode.Username,
			"password": c.Agent.Opencode.Password,
			"timeout":  c.Agent.Opencode.Timeout.String(),
			"db_path":  c.Agent.Opencode.DBPath,
		}
	case "codex":
		configRaw = map[string]any{
			"command":            c.Agent.Codex.Command,
			"args":               c.Agent.Codex.Args,
			"env":                c.Agent.Codex.Env,
			"timeout":            c.Agent.Codex.Timeout.String(),
			"initialize_timeout": c.Agent.Codex.InitializeTimeout.String(),
		}
	case "claude", "claude-code":
		configRaw = map[string]any{
			"command": c.Agent.Claude.Command,
			"args":    c.Agent.Claude.Args,
			"env":     c.Agent.Claude.Env,
			"timeout": c.Agent.Claude.Timeout.String(),
		}
	default:
		return nil, fmt.Errorf("unsupported agent driver %q", c.Agent.Driver)
	}

	return factory.Factory(driver, configRaw, infra)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "command failed: %v\n", err)
		os.Exit(1)
	}
}
