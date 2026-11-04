package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDistributionUsesOwnedContainerImage(t *testing.T) {
	root := filepath.Join("..", "..")
	want := "ghcr.io/hypnguyen1209/v-artex"
	for _, name := range []string{"docker-compose.yml", ".github/workflows/release.yml", "README.md"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Contains(text, "autumn27/artex") {
			t.Errorf("%s still references the upstream image", name)
		}
		if !strings.Contains(text, want) {
			t.Errorf("%s does not reference %s", name, want)
		}
	}

	release, err := os.ReadFile(filepath.Join(root, ".github/workflows/release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(release)
	for _, required := range []string{"packages: write", "registry: ghcr.io", "password: ${{ secrets.GITHUB_TOKEN }}"} {
		if !strings.Contains(text, required) {
			t.Errorf("release workflow is missing %q", required)
		}
	}
	if strings.Contains(text, "DOCKERHUB_") {
		t.Error("release workflow still requires Docker Hub secrets")
	}
}

func TestDistributionUsesOwnedNormaModule(t *testing.T) {
	root := filepath.Join("..", "..")
	want := "github.com/hypnguyen1209/norma"
	for _, name := range []string{"go.mod", "README.md"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Contains(text, "github.com/Autumn-27/norma") {
			t.Errorf("%s still references the upstream Norma module", name)
		}
		if !strings.Contains(text, want) {
			t.Errorf("%s does not reference %s", name, want)
		}
	}
}
