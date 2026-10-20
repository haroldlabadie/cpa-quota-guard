package main

import (
	"bytes"
	"gopkg.in/yaml.v3"
	"os"
	"reflect"
	"regexp"
	"testing"
)

func TestVersionedHostBootstrapKeepsStrictValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"host version selector", "version: 0.2.0\nenabled: true\nmode: observe\n", false},
		{"unversioned installation", "enabled: true\nmode: observe\n", false},
		{"unknown field remains rejected", "version: 0.2.0\nunknown_option: true\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg bootstrap
			d := yaml.NewDecoder(bytes.NewBufferString(tc.raw))
			d.KnownFields(true)
			err := d.Decode(&cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("decode error = %v; want error %v", err, tc.wantErr)
			}
		})
	}
}

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
	if !ok || !cfg.Plugins.Enabled || cfg.Plugins.Dir != "/CLIProxyAPI/plugins" || item.Version != "0.2.0" || !item.Enabled || item.Mode != "observe" || item.CheckSeconds != 20 || item.ProbeSeconds != 20 {
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
