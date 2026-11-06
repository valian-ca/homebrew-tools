package adopt

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var composeNames = []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}

type composeService struct {
	file, name, image string
}

func (s composeService) String() string {
	return fmt.Sprintf("%s service %s (%s)", s.file, s.name, s.image)
}

type mysqlFound struct {
	service composeService
	port    int
	// portIssue and passwordIssue say what lanes init could not read; the
	// key is then left for the agent to fill in.
	portIssue     string
	password      string
	passwordIssue string
}

const (
	mysqlContainerPort    = "3306"
	postgresContainerPort = "5432"
)

func (p *project) detectCompose() {
	for _, f := range p.files {
		if !slices.Contains(composeNames, path.Base(f)) {
			continue
		}
		data, err := p.read(f)
		if err != nil {
			continue
		}
		services, err := composeServices(data)
		if err != nil {
			p.report.check("%s: not a valid compose file (%v) — skipped", f, err)
			continue
		}
		for _, s := range services {
			svc := composeService{file: f, name: s.name, image: s.Image}
			switch imageName(s.Image) {
			case "mysql", "mariadb":
				if p.mysql != nil {
					p.report.check("%s: another MySQL service — lanes.yaml uses %s, and a project has at most one mysql resource", svc, p.mysql.service)
					continue
				}
				p.mysql = readMySQL(svc, s)
			case "postgres":
				p.postgres = append(p.postgres, svc)
				p.report.NotWritten = append(p.report.NotWritten, "Postgres: adapter not available in this lanes — "+svc.String())
			}
		}
	}
}

type serviceSpec struct {
	name        string
	Image       string      `yaml:"image"`
	Ports       []yaml.Node `yaml:"ports"`
	Environment yaml.Node   `yaml:"environment"`
}

func composeServices(data []byte) ([]serviceSpec, error) {
	var doc struct {
		Services yaml.Node `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Services.Kind != yaml.MappingNode {
		return nil, nil
	}
	var out []serviceSpec
	for i := 0; i+1 < len(doc.Services.Content); i += 2 {
		var s serviceSpec
		if err := doc.Services.Content[i+1].Decode(&s); err != nil {
			return nil, err
		}
		s.name = doc.Services.Content[i].Value
		out = append(out, s)
	}
	return out, nil
}

func imageName(image string) string {
	img := strings.ToLower(strings.TrimSpace(image))
	if i := strings.Index(img, "@"); i >= 0 {
		img = img[:i]
	}
	img = img[strings.LastIndex(img, "/")+1:]
	if i := strings.Index(img, ":"); i >= 0 {
		img = img[:i]
	}
	return img
}

func readMySQL(svc composeService, s serviceSpec) *mysqlFound {
	m := &mysqlFound{service: svc}
	port, raw, found := publishedPort(s.Ports, mysqlContainerPort)
	switch {
	case !found:
		m.portIssue = fmt.Sprintf("%s publishes no host port for 3306", svc)
	case port == 0:
		m.portIssue = fmt.Sprintf("%s publishes 3306 as %q, which lanes init cannot resolve", svc, raw)
	default:
		m.port = port
	}
	env := environment(s.Environment)
	m.password, m.passwordIssue = rootPassword(svc, env)
	return m
}

// publishedPort finds the host port mapped to container; found without a
// port means the mapping exists but its host side is not a plain number.
func publishedPort(ports []yaml.Node, container string) (port int, raw string, found bool) {
	for _, n := range ports {
		var host, target string
		switch n.Kind {
		case yaml.ScalarNode:
			raw = n.Value
			host, target = splitShortPort(n.Value)
		case yaml.MappingNode:
			var long struct {
				Target    yaml.Node `yaml:"target"`
				Published yaml.Node `yaml:"published"`
			}
			if n.Decode(&long) != nil {
				continue
			}
			raw = long.Published.Value
			host, target = long.Published.Value, long.Target.Value
		default:
			continue
		}
		if t, ok := resolve(target); !ok || t != container {
			continue
		}
		if h, ok := resolve(host); ok {
			if v, err := strconv.Atoi(h); err == nil && v > 0 && v <= 65535 {
				return v, raw, true
			}
		}
		return 0, raw, true
	}
	return 0, "", false
}

// splitShortPort reads [IP:][HOST:]CONTAINER[/PROTO], where ${VAR:-N} and a
// bracketed IPv6 address hold colons of their own.
func splitShortPort(s string) (host, container string) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "/"); i > strings.LastIndex(s, "}") {
		s = s[:i]
	}
	var parts []string
	depth, start := 0, 0
	for i, c := range s {
		switch c {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case ':':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])
	switch len(parts) {
	case 1:
		return "", parts[0]
	case 2:
		return parts[0], parts[1]
	default:
		return parts[len(parts)-2], parts[len(parts)-1]
	}
}

var defaultRef = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*:?-([^}$]*)\}$`)

// resolve reads a compose value as docker compose would without the
// environment: a literal, or the default of ${VAR:-default}. $$ is compose's
// escaped dollar.
func resolve(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if m := defaultRef.FindStringSubmatch(s); m != nil {
		return m[1], true
	}
	literal := strings.ReplaceAll(s, "$$", "")
	if strings.Contains(literal, "$") {
		return "", false
	}
	return strings.ReplaceAll(s, "$$", "$"), true
}

type envValue struct {
	value string
	set   bool
}

func environment(n yaml.Node) map[string]envValue {
	env := map[string]envValue{}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			v := n.Content[i+1]
			env[n.Content[i].Value] = envValue{value: v.Value, set: v.Tag != "!!null"}
		}
	case yaml.SequenceNode:
		for _, e := range n.Content {
			k, v, ok := strings.Cut(e.Value, "=")
			env[k] = envValue{value: v, set: ok}
		}
	}
	return env
}

func rootPassword(svc composeService, env map[string]envValue) (password, issue string) {
	for _, key := range []string{"MYSQL_ROOT_PASSWORD", "MARIADB_ROOT_PASSWORD"} {
		e, ok := env[key]
		if !ok {
			continue
		}
		if !e.set {
			return "", fmt.Sprintf("%s takes %s from the shell's environment", svc, key)
		}
		v, ok := resolve(e.value)
		if !ok {
			return "", fmt.Sprintf("%s sets %s to %q, which lanes init does not copy", svc, key, e.value)
		}
		return v, ""
	}
	for _, key := range []string{"MYSQL_ALLOW_EMPTY_PASSWORD", "MARIADB_ALLOW_EMPTY_ROOT_PASSWORD"} {
		if e, ok := env[key]; ok && e.set && e.value != "" && !strings.EqualFold(e.value, "no") && !strings.EqualFold(e.value, "false") && e.value != "0" {
			return "", ""
		}
	}
	return "", fmt.Sprintf("%s sets no MYSQL_ROOT_PASSWORD lanes init can read", svc)
}
