package flavor

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGroovy(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "android/app/build.gradle"), `
android {
    flavorDimensions "env"
    productFlavors {
        development {
            dimension "env"
            applicationIdSuffix ".dev"
        }
        staging {
            dimension "env"
        }
        production {
            dimension "env"
        }
    }
}
`)
	got := Detect(dir)
	want := []string{"development", "staging", "production"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestKotlin(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "android/app/build.gradle.kts"), `
android {
    productFlavors {
        create("development") {
            applicationIdSuffix = ".dev"
        }
        create("staging") {
        }
    }
}
`)
	got := Detect(dir)
	want := []string{"development", "staging"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestXcschemes(t *testing.T) {
	dir := t.TempDir()
	schemes := filepath.Join(dir, "ios/Runner.xcodeproj/xcshareddata/xcschemes")
	if err := os.MkdirAll(schemes, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"Runner.xcscheme", "Runner-development.xcscheme", "staging.xcscheme"} {
		writeFile(t, filepath.Join(schemes, n), "")
	}
	got := Detect(dir)
	sort.Strings(got)
	want := []string{"development", "staging"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMainDart(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "lib/main_development.dart"), "")
	writeFile(t, filepath.Join(dir, "lib/main_staging.dart"), "")
	writeFile(t, filepath.Join(dir, "lib/main.dart"), "")
	got := Detect(dir)
	sort.Strings(got)
	want := []string{"development", "staging"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestUnion(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "android/app/build.gradle"), `
productFlavors {
    development { }
    staging { }
}
`)
	writeFile(t, filepath.Join(dir, "lib/main_production.dart"), "")
	got := Detect(dir)
	sort.Strings(got)
	want := []string{"development", "production", "staging"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestEmpty(t *testing.T) {
	dir := t.TempDir()
	if got := Detect(dir); len(got) != 0 {
		t.Fatalf("expected no flavors, got %v", got)
	}
}
