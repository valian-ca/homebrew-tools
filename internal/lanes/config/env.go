package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/valian-ca/homebrew-tools/internal/lanes/env"
)

type EnvVar struct {
	Name     string
	Template string
}

// EnvVars keeps lanes.yaml's order: each entry sees the ones before it.
type EnvVars []EnvVar

func (e *EnvVars) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("env: must map each variable name to a template (e.g. APP_URL: http://{{.LANES_HOST}}:{{.PORT_WEB}})")
	}
	out := make(EnvVars, 0, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		k, v := node.Content[i], node.Content[i+1]
		if v.Kind != yaml.ScalarNode {
			return fmt.Errorf("env.%s: must be a template string", k.Value)
		}
		out = append(out, EnvVar{Name: k.Value, Template: v.Value})
	}
	*e = out
	return nil
}

func (v EnvVar) Parse() (*template.Template, error) {
	return template.New("env." + v.Name).Option("missingkey=error").Parse(v.Template)
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (e EnvVars) validate() error {
	seen := map[string]bool{}
	for _, v := range e {
		if !envName.MatchString(v.Name) {
			return fmt.Errorf("env.%s: not a variable name (letters, digits, '_', not starting with a digit)", v.Name)
		}
		if seen[v.Name] {
			return fmt.Errorf("env.%s: declared twice", v.Name)
		}
		seen[v.Name] = true
		if reserved(v.Name) {
			return fmt.Errorf("env.%s: redefines a variable lanes sets (TMPDIR, LANE_ID, PORT_*, LANES_*, the Firebase emulator variables)", v.Name)
		}
		if _, err := v.Parse(); err != nil {
			return fmt.Errorf("env.%s: %w", v.Name, err)
		}
	}
	return nil
}

func reserved(name string) bool {
	return slices.Contains(env.Fixed, name) || strings.HasPrefix(name, "PORT_") || strings.HasPrefix(name, "LANES_")
}
