package main

import (
	"errors"
	"os"
	"strings"

	"github.com/spf13/viper"
)

const (
	configSourceFlag     = "flag"
	configSourceEnv      = "env"
	configSourceConfig   = "config"
	configSourceDefault  = "default"
	configSourceUnset    = "unset"
	configSourceResolved = "resolved"
)

type resolvedString struct {
	value  string
	source string
}

type configResolver struct {
	v *viper.Viper
}

func newConfigResolver(path string) (*configResolver, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("toml")
	v.SetDefault("server_url", "http://127.0.0.1:8080")
	v.SetEnvPrefix("RASCAL")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) && !os.IsNotExist(err) {
			return nil, err
		}
	}

	return &configResolver{v: v}, nil
}

func (r *configResolver) stringValue(key string) string {
	return strings.TrimSpace(r.v.GetString(key))
}

func (r *configResolver) intValue(key string) int {
	return r.v.GetInt(key)
}

func (r *configResolver) resolveString(flagValue, envKey, configKey, fallbackSource string) resolvedString {
	flagValue = strings.TrimSpace(flagValue)
	if flagValue != "" {
		return resolvedString{value: flagValue, source: configSourceFlag}
	}

	envValue := strings.TrimSpace(os.Getenv(envKey))
	configValue := strings.TrimSpace(r.v.GetString(configKey))
	if envValue != "" {
		return resolvedString{value: configValue, source: configSourceEnv}
	}
	if r.v.InConfig(configKey) {
		return resolvedString{value: configValue, source: configSourceConfig}
	}
	return resolvedString{value: configValue, source: fallbackSource}
}
