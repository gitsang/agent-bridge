package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"
	"time"

	"github.com/docker/go-units"
	"github.com/gitsang/agent-bridge/pkg/util/timex"
	"github.com/gitsang/logi"
	"github.com/natefinch/lumberjack"
)

type LogConfig struct {
	Format    string         `json:"format" yaml:"format" default:"json" usage:"log format (json|console)"`
	Level     string         `json:"level" yaml:"level" default:"info" usage:"log level (debug|info|warn|error)"`
	Verbosity int            `json:"verbosity" yaml:"verbosity" default:"3" usage:"log verbosity (0-4)"`
	Attrs     map[string]any `json:"attrs" yaml:"attrs" default:"{}" usage:"log attrs"`
	Output    struct {
		Stdout struct {
			Enable bool `json:"enable" yaml:"enable" default:"false" usage:"enable stdout log"`
		} `json:"stdout" yaml:"stdout"`
		Stderr struct {
			Enable bool `json:"enable" yaml:"enable" default:"false" usage:"enable stderr log"`
		} `json:"stderr" yaml:"stderr"`
		File struct {
			Enable     bool   `json:"enable" yaml:"enable" default:"false" usage:"enable file log"`
			Path       string `json:"path" yaml:"path" default:"/var/log/yauth/yauth.log" usage:"log file path"`
			MaxSize    string `json:"max_size" yaml:"max_size" default:"10mb" usage:"log file max size using SI(decimal) standard (K|mb|Gb...)"`
			MaxAge     string `json:"max_age" yaml:"max_age" default:"7d" usage:"log file max age (d|h|m|s)"`
			MaxBackups int    `json:"max_backups" yaml:"max_backups" default:"10" usage:"log file max backups"`
			Compress   bool   `json:"compress" yaml:"compress" default:"true" usage:"enable log file compress"`
		} `json:"file" yaml:"file"`
	} `json:"output" yaml:"output"`
}

func NewLogHandler(config LogConfig) slog.Handler {
	writers := make([]io.Writer, 0)
	if config.Output.Stdout.Enable {
		writers = append(writers, os.Stdout)
	}
	if config.Output.Stderr.Enable {
		writers = append(writers, os.Stderr)
	}
	if config.Output.File.Enable {
		maxSize, err := units.FromHumanSize(config.Output.File.MaxSize)
		if err != nil {
			panic(err)
		}
		maxAgeDur, err := timex.ParseDuration(config.Output.File.MaxAge)
		if err != nil {
			panic(err)
		}
		err = os.MkdirAll(path.Dir(config.Output.File.Path), 0o755)
		if err != nil {
			panic(err)
		}
		writers = append(writers, &lumberjack.Logger{
			Filename:   config.Output.File.Path,
			MaxSize:    int(maxSize / units.MB),
			MaxAge:     int(maxAgeDur / (24 * time.Hour)),
			MaxBackups: config.Output.File.MaxBackups,
			LocalTime:  false,
			Compress:   config.Output.File.Compress,
		})
	}

	return logi.NewHandler(
		logi.HandlerOptions{
			Format:       config.Format,
			Level:        config.Level,
			Attrs:        config.Attrs,
			Writers:      writers,
			Verbosity:    config.Verbosity,
			CallerSkip:   11,
			ReplaceAttrs: []logi.ReplaceAttrFunc{},
		},
	)
}

func NewLogHandlers(configs ...LogConfig) []slog.Handler {
	handlers := make([]slog.Handler, 0)
	for _, config := range configs {
		handlers = append(handlers, NewLogHandler(config))
	}
	return handlers
}

func NewFanoutLogHandler(configs ...LogConfig) slog.Handler {
	return logi.NewFanOutHandler(NewLogHandlers(configs...)...)
}

type LogHandlers struct {
	DefaultName string
	Handlers    map[string]slog.Handler
}

func BuildLogHandlers(c Config) (*LogHandlers, error) {
	h := &LogHandlers{
		DefaultName: c.Log.Handlers.Default,
		Handlers:    make(map[string]slog.Handler),
	}
	for name, logConfig := range c.Log.Providers {
		h.Handlers[strings.ToLower(name)] = NewFanoutLogHandler(logConfig...)
	}

	_, ok := h.Handlers[strings.ToLower(c.Log.Handlers.Default)]
	if !ok {
		return nil, errors.New("default log handler not found")
	}

	return h, nil
}

func (h *LogHandlers) Get(name string) slog.Handler {
	handler, ok := h.Handlers[strings.ToLower(name)]
	if !ok {
		handler, ok = h.Handlers[strings.ToLower(h.DefaultName)]
		if !ok {
			panic(fmt.Sprintf("default log handler %s not found", h.DefaultName))
		}
	}
	return handler
}
