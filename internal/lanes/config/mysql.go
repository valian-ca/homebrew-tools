package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// MySQLHost is the only address lanes connects to: the port guard looks at
	// this Mac's listener, and could not vouch for another host.
	MySQLHost        = "127.0.0.1"
	DefaultMySQLPort = 3306
	DefaultMySQLUser = "root"
	LanePlaceholder  = "{lane}"
	// MaxLaneIDLen is the longest lane ID: a 24-character slug, '_' and 6 hex.
	MaxLaneIDLen    = 31
	MaxDatabaseName = 64
)

var DefaultMySQLListeners = []string{"mysqld"}

type MySQL struct {
	// Host catches a host key, refused with its reason rather than as unknown.
	Host      yaml.Node `yaml:"host" json:"-"`
	Port      int       `yaml:"port" json:"port"`
	User      string    `yaml:"user" json:"user"`
	Password  string    `yaml:"password" json:"password"`
	Start     string    `yaml:"start" json:"start,omitempty"`
	Listeners []string  `yaml:"listeners" json:"listeners"`
	Databases Databases `yaml:"databases" json:"databases"`
}

type Database struct {
	Key      string `json:"key"`
	Template string `json:"template"`
}

// Databases keeps the order lanes.yaml declares them in, which the exported
// variables and every error follow.
type Databases []Database

func (d *Databases) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("mysql.databases: must map each database key to a name template (e.g. dev: app_{lane})")
	}
	out := make(Databases, 0, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		k, v := node.Content[i], node.Content[i+1]
		if v.Kind != yaml.ScalarNode {
			return fmt.Errorf("mysql.databases.%s: must be a name template (e.g. app_{lane})", k.Value)
		}
		out = append(out, Database{Key: k.Value, Template: v.Value})
	}
	*d = out
	return nil
}

func (d Database) Name(laneID string) string {
	return strings.ReplaceAll(d.Template, LanePlaceholder, laneID)
}

var databaseKey = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// templateText is what a database template may hold around {lane}: the
// characters of an unquoted MySQL identifier, ASCII only, so no name reaches
// CREATE DATABASE with a character the server refuses.
var templateText = regexp.MustCompile(`^[A-Za-z0-9_$]*$`)

func (m *MySQL) validate() error {
	if !m.Host.IsZero() {
		return fmt.Errorf("host: not a key — lanes always connects to %s, the server on this Mac", MySQLHost)
	}
	if m.Port < 0 || m.Port > 65535 {
		return fmt.Errorf("port: %d is not a TCP port (1-65535)", m.Port)
	}
	if m.Listeners != nil && len(m.Listeners) == 0 {
		return errors.New("listeners: must name at least one process (default [mysqld])")
	}
	for i, l := range m.Listeners {
		if strings.TrimSpace(l) == "" {
			return fmt.Errorf("listeners[%d]: empty process name", i)
		}
	}
	if len(m.Databases) == 0 {
		return errors.New("databases: required, at least one (e.g. dev: app_{lane})")
	}
	byTemplate, byVar := map[string]string{}, map[string]string{}
	for _, d := range m.Databases {
		if !databaseKey.MatchString(d.Key) {
			return fmt.Errorf("databases.%s: the key must be letters, digits or '_'", d.Key)
		}
		if first, ok := byVar[strings.ToUpper(d.Key)]; ok {
			return fmt.Errorf("databases.%s: same variable as databases.%s (LANES_MYSQL_DATABASE_%s)", d.Key, first, strings.ToUpper(d.Key))
		}
		byVar[strings.ToUpper(d.Key)] = d.Key
		n := strings.Count(d.Template, LanePlaceholder)
		if n == 0 {
			return fmt.Errorf("databases.%s: %q must contain %s", d.Key, d.Template, LanePlaceholder)
		}
		if !templateText.MatchString(strings.ReplaceAll(d.Template, LanePlaceholder, "")) {
			return fmt.Errorf("databases.%s: %q may hold only letters, digits, '_' and '$' around %s", d.Key, d.Template, LanePlaceholder)
		}
		if strings.HasPrefix(d.Template, LanePlaceholder) {
			return fmt.Errorf("databases.%s: %q must start with a fixed prefix before %s (e.g. app_%s), or reconcile could take other databases on the server for a lane's", d.Key, d.Template, LanePlaceholder, LanePlaceholder)
		}
		if longest := len(d.Template) + n*(MaxLaneIDLen-len(LanePlaceholder)); longest > MaxDatabaseName {
			return fmt.Errorf("databases.%s: %q can make a %d-character name with a %d-character lane ID, over MySQL's %d-character limit",
				d.Key, d.Template, longest, MaxLaneIDLen, MaxDatabaseName)
		}
		if first, ok := byTemplate[d.Template]; ok {
			return fmt.Errorf("databases.%s: same template as databases.%s", d.Key, first)
		}
		byTemplate[d.Template] = d.Key
	}
	return nil
}

func (m *MySQL) setDefaults() {
	if m.Port == 0 {
		m.Port = DefaultMySQLPort
	}
	if m.User == "" {
		m.User = DefaultMySQLUser
	}
	if m.Listeners == nil {
		m.Listeners = DefaultMySQLListeners
	}
}

// checkSchema2Keys runs before the strict decode, which would otherwise
// report a schema-2 key in a schema-1 file as unknown instead of naming the
// schema it needs.
func checkSchema2Keys(data []byte, schema int) error {
	if schema >= 2 {
		return nil
	}
	var head struct {
		Env       yaml.Node   `yaml:"env"`
		Resources []yaml.Node `yaml:"resources"`
	}
	if err := yaml.Unmarshal(data, &head); err != nil {
		return nil
	}
	if !head.Env.IsZero() {
		return errors.New("env: requires schema: 2")
	}
	for i, r := range head.Resources {
		if r.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(r.Content); j += 2 {
			if r.Content[j].Value == "mysql" {
				return fmt.Errorf("resources[%d].mysql: requires schema: 2", i)
			}
		}
	}
	return nil
}
