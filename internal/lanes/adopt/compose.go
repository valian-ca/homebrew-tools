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
	service       composeService
	port          int
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
				r := &resolver{dotenv: p.dotenv(path.Join(path.Dir(f), ".env"))}
				p.mysql = readMySQL(svc, s, r)
				p.reportResolved(svc, r)
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

func readMySQL(svc composeService, s serviceSpec, r *resolver) *mysqlFound {
	m := &mysqlFound{service: svc}
	port, raw, found := publishedPort(s.Ports, mysqlContainerPort, r)
	switch {
	case !found:
		m.portIssue = fmt.Sprintf("%s publishes no host port for 3306", svc)
	case port == 0:
		m.portIssue = fmt.Sprintf("%s publishes 3306 as %q, which lanes init cannot resolve", svc, raw)
	default:
		m.port = port
	}
	env := environment(s.Environment)
	m.password, m.passwordIssue = rootPassword(svc, env, r)
	return m
}

func publishedPort(ports []yaml.Node, container string, r *resolver) (port int, raw string, found bool) {
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
		if t, ok := r.resolve(target); !ok || t != container {
			continue
		}
		if h, ok := r.resolve(host); ok {
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

var (
	varRef     = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:?-)([^}$]*))?\}$|^\$([A-Za-z_][A-Za-z0-9_]*)$`)
	dotenvLine = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*?)\s*$`)
)

// resolver reads a compose value as docker compose would from the .env
// beside the compose file, without the shell's environment: a literal, a
// variable that .env sets, or the default of ${VAR:-default}. $$ is
// compose's escaped dollar.
type resolver struct {
	dotenv   map[string]string
	fromEnv  []string
	defaults []string
}

func (r *resolver) resolve(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if m := varRef.FindStringSubmatch(s); m != nil {
		name := m[1] + m[4]
		v, set := r.dotenv[name]
		if set && (v != "" || m[2] != ":-") {
			r.fromEnv = append(r.fromEnv, name)
			return v, true
		}
		if m[2] != "" {
			r.defaults = append(r.defaults, name)
			return m[3], true
		}
		return "", false
	}
	literal := strings.ReplaceAll(s, "$$", "")
	if strings.Contains(literal, "$") {
		return "", false
	}
	return strings.ReplaceAll(s, "$$", "$"), true
}

func (p *project) dotenv(rel string) map[string]string {
	env := map[string]string{}
	data, err := p.read(rel)
	if err != nil {
		return env
	}
	for _, line := range strings.Split(string(data), "\n") {
		m := dotenvLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v := m[2]
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		env[m[1]] = v
	}
	return env
}

func (p *project) reportResolved(svc composeService, r *resolver) {
	if len(r.fromEnv) > 0 {
		p.report.check("%s: %s read from the .env beside it in this checkout — check the values lanes.yaml took", svc, strings.Join(slices.Compact(r.fromEnv), ", "))
	}
	if len(r.defaults) > 0 {
		p.report.check("%s: %s not set in the .env beside it — lanes.yaml took the compose default; check the server runs with it", svc, strings.Join(slices.Compact(r.defaults), ", "))
	}
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

func rootPassword(svc composeService, env map[string]envValue, r *resolver) (password, issue string) {
	for _, key := range []string{"MYSQL_ROOT_PASSWORD", "MARIADB_ROOT_PASSWORD"} {
		e, ok := env[key]
		if !ok {
			continue
		}
		if !e.set {
			return "", fmt.Sprintf("%s takes %s from the shell's environment", svc, key)
		}
		v, ok := r.resolve(e.value)
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
