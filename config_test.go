package main

import (
	"bytes"
	"gopkg.in/yaml.v3"
	"os"
	"reflect"
	"regexp"
	"testing"
)

// Mirror the host's plugins.enabled/dir/configs shape, then decode each flat
// instance with the same strict bootstrap type used by plugin.register.
func TestExampleMatchesHostConfigurationContract(t *testing.T) {
	example, err := os.ReadFile("config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Plugins struct {
			Enabled bool                 `yaml:"enabled"`
			Dir     string               `yaml:"dir"`
			Configs map[string]bootstrap `yaml:"configs"`
		} `yaml:"plugins"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(example))
	decoder.KnownFields(true)
	if err = decoder.Decode(&cfg); err != nil {
		t.Fatal(err)
	}
	item, ok := cfg.Plugins.Configs[pluginID]
	if !ok || !cfg.Plugins.Enabled || cfg.Plugins.Dir != "/CLIProxyAPI/plugins" || !item.Enabled || item.Mode != "observe" || item.CheckSeconds != 20 || item.ProbeSeconds != 20 {
		t.Fatal("invalid host plugin example")
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile("(?s)```yaml\\n(.*?)```").FindSubmatch(readme)
	if len(match) != 2 {
		t.Fatal("README YAML example missing")
	}
	var a, b any
	if yaml.Unmarshal(example, &a) != nil || yaml.Unmarshal(match[1], &b) != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("README and example configuration differ")
	}
}
