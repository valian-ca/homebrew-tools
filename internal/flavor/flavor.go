// Package flavor detects Flutter flavors declared across android, ios,
// and lib/ in a project. Best-effort: each source is independent and a
// parse failure in one doesn't stop the others.
package flavor

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Detect returns the union (order-preserved, deduplicated) of flavor names
// declared in the project rooted at dir.
func Detect(dir string) []string {
	var out []string
	seen := make(map[string]struct{})
	add := func(names []string) {
		for _, n := range names {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if _, ok := seen[n]; ok {
				continue
			}
			seen[n] = struct{}{}
			out = append(out, n)
		}
	}
	add(fromGradleGroovy(filepath.Join(dir, "android", "app", "build.gradle")))
	add(fromGradleKotlin(filepath.Join(dir, "android", "app", "build.gradle.kts")))
	add(fromXcschemes(filepath.Join(dir, "ios", "Runner.xcodeproj", "xcshareddata", "xcschemes")))
	add(fromMainDart(filepath.Join(dir, "lib")))
	return out
}

// Groovy: scan the productFlavors { ... } block and pull identifiers
// that open their own sub-block (e.g. `staging {`).
var groovyFlavorLine = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*\{`)

func fromGradleGroovy(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return scanBracedBlock(f, "productFlavors", func(line string) string {
		line = strings.TrimSpace(line)
		loc := groovyFlavorLine.FindString(line)
		if loc == "" {
			return ""
		}
		// Strip trailing "{" and any whitespace before it.
		name := strings.TrimSpace(strings.TrimSuffix(loc, "{"))
		return name
	})
}

// Kotlin: create("name") calls inside productFlavors { ... }.
var kotlinFlavorCall = regexp.MustCompile(`create\("([^"]+)"\)`)

func fromGradleKotlin(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return scanBracedBlock(f, "productFlavors", func(line string) string {
		m := kotlinFlavorCall.FindStringSubmatch(line)
		if m == nil {
			return ""
		}
		return m[1]
	})
}

// scanBracedBlock reads r line by line. Once it enters a block whose header
// matches "<marker>{", it calls extract on each subsequent line until the
// block closes. Brace depth is tracked to survive nested blocks.
func scanBracedBlock(r *os.File, marker string, extract func(string) string) []string {
	markerRe := regexp.MustCompile(regexp.QuoteMeta(marker) + `\s*\{`)
	var out []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	inBlock := false
	depth := 0
	for sc.Scan() {
		line := sc.Text()
		if !inBlock {
			if markerRe.MatchString(line) {
				inBlock = true
				depth = 1
			}
			continue
		}
		opens := strings.Count(line, "{")
		closes := strings.Count(line, "}")
		depth += opens - closes
		if depth <= 0 {
			break
		}
		if name := extract(line); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// Xcode schemes: every *.xcscheme under xcshareddata/xcschemes is a flavor,
// except Runner itself. The "Runner-" prefix is stripped when present.
func fromXcschemes(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".xcscheme") {
			continue
		}
		base := strings.TrimSuffix(name, ".xcscheme")
		if base == "Runner" {
			continue
		}
		base = strings.TrimPrefix(base, "Runner-")
		out = append(out, base)
	}
	return out
}

// lib/main_<flavor>.dart → <flavor>.
func fromMainDart(libDir string) []string {
	matches, err := filepath.Glob(filepath.Join(libDir, "main_*.dart"))
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range matches {
		base := filepath.Base(m)
		name := strings.TrimSuffix(strings.TrimPrefix(base, "main_"), ".dart")
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}
