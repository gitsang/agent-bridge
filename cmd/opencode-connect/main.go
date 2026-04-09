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

	"github.com/gitsang/configer"
	"github.com/gitsang/opencode-connect/internal/connect"
	"github.com/gitsang/opencode-connect/internal/opencode"
	"github.com/gitsang/opencode-connect/internal/plugin"
	_ "github.com/gitsang/opencode-connect/internal/plugin/openai_compatible"
	_ "github.com/gitsang/opencode-connect/internal/plugin/ume"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

var rootCmd = &cobra.Command{
	Use:   "opencode-connect",
	Short: "Connect opencode to chat apps",
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
			configer.WithEnvPrefix("OPENCODE_CONNECT"),
			configer.WithEnvDelim("_"),
		),
		configer.WithFlagBind(
			configer.WithCommand(rootCmd),
			configer.WithFlagPrefix("occ"),
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
	logger.Debug("Preparing...",
		slog.Any("flags", rootFlags),
		slog.Any("config", c),
		slog.String("pid", fmt.Sprintf("%d", os.Getpid())),
	)

	// Dependency Injection
	opencodeClient := opencode.NewClient(
		c.Opencode.BaseURL,
		opencode.WithLogger(logger),
		opencode.WithAuthentication(c.Opencode.Username, c.Opencode.Password),
		opencode.WithTimeout(c.Opencode.Timeout),
	)
	conversationStore, err := buildConversationStore(c)
	if err != nil {
		return err
	}
	connector := connect.New(
		connect.WithLogger(logger),
		connect.WithAgentClient(opencodeClient),
		connect.WithConversationStore(conversationStore),
	)

	runCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if len(c.Plugins) == 0 {
		return fmt.Errorf("no enabled plugins configured")
	}

	pluginInfra := plugin.Infrastructure{Logger: logger}
	group, groupCtx := errgroup.WithContext(runCtx)

	instanceNames := make([]string, 0, len(c.Plugins))
	for instanceName := range c.Plugins {
		instanceNames = append(instanceNames, instanceName)
	}
	sort.Strings(instanceNames)

	for _, instanceName := range instanceNames {
		instanceConfigRaw := c.Plugins[instanceName]
		instanceConfigMap, ok := instanceConfigRaw.(map[string]any)
		if !ok {
			return fmt.Errorf("plugin %s config must be a map", instanceName)
		}
		if len(instanceConfigMap) != 1 {
			return fmt.Errorf("plugin %s config must define exactly one plugin type", instanceName)
		}

		for pluginType, pluginConfigRaw := range instanceConfigMap {
			if pluginConfigRaw == nil {
				return fmt.Errorf("plugin %s config is nil", instanceName)
			}

			pluginFactory, ok := plugin.GetPluginFactory(pluginType)
			if !ok {
				return fmt.Errorf("unsupported plugin type %q for %q", pluginType, instanceName)
			}

			plugin, err := pluginFactory.Construct(instanceName, pluginConfigRaw, pluginInfra)
			if err != nil {
				return fmt.Errorf("build plugin %s (%s): %w", instanceName, pluginType, err)
			}
			if plugin == nil {
				return fmt.Errorf("build plugin %s (%s): factory returned nil plugin", instanceName, pluginType)
			}

			currentPlugin := plugin
			group.Go(func() error {
				logger := logger.With(
					slog.String("plugin_name", instanceName),
					slog.String("plugin_type", pluginType),
					slog.Any("plugin_config", pluginConfigRaw),
				)
				defer func() {
					logger.Debug("plugin stopped")
				}()
				logger.Debug("starting plugin")

				if err := currentPlugin.Serve(groupCtx, connector.Handle); err != nil {
					return fmt.Errorf("%s plugin failed: %w", currentPlugin.Name(), err)
				}
				return nil
			})
		}
	}

	return group.Wait()
}

func buildConversationStore(c Config) (connect.ConversationStore, error) {
	storeType := strings.ToLower(strings.TrimSpace(c.ConversationStore.Type))
	switch storeType {
	case "", "memory":
		return connect.NewMemoryConversationStore(c.ConversationStore.TTL, c.ConversationStore.MaxItems), nil
	case "file":
		return connect.NewFileConversationStore(c.ConversationStore.FilePath, c.ConversationStore.TTL, c.ConversationStore.MaxItems)
	default:
		return nil, fmt.Errorf("unsupported conversation store type %q", c.ConversationStore.Type)
	}
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "command failed: %v\n", err)
		os.Exit(1)
	}
}
