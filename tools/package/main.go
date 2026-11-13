// Build official packages from their manifests, and publish a signed catalogue.
package main

import (
	"archive/zip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type entry struct {
	HostServices         []string             `json:"hostServices,omitempty"`
	ID                   string               `json:"id"`
	Version              string               `json:"version"`
	ProtocolVersion      int                  `json:"protocolVersion"`
	Name                 map[string]string    `json:"name"`
	Summary              map[string]string    `json:"summary"`
	Platforms            map[string]asset     `json:"platforms"`
	Author               string               `json:"author,omitempty"`
	Description          map[string]string    `json:"description,omitempty"`
	Usage                map[string]string    `json:"usage,omitempty"`
	ReleaseNotes         map[string]string    `json:"releaseNotes,omitempty"`
	Homepage             string               `json:"homepage,omitempty"`
	Icon                 *asset               `json:"icon,omitempty"`
	RequiredHostServices []serviceRequirement `json:"requiredHostServices,omitempty"`
}
type asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type manifest struct {
	HostServices                                    []string
	ID, Version, Executable, Icon, Author, Homepage string
	ProtocolVersion                                 int
	RequiredHostServices                            []serviceRequirement
}
type serviceRequirement struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

func main() {
	tag := flag.String("tag", "local", "release tag")
	repository := flag.String("repository", "FluentLauncher/FluentLauncher-plugins", "public release repository")
	signed := flag.Bool("signed", false, "sign the exact catalogue bytes")
	flag.Parse()
	if err := build(*tag, *repository, *signed); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func build(tag, repository string, signed bool) error {
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(repository) {
		return fmt.Errorf("invalid repository")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+$`).MatchString(tag) {
		return fmt.Errorf("invalid release tag")
	}
	var key ed25519.PrivateKey
	if signed {
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("FLUENTLAUNCHER_PLUGIN_PRIVATE_KEY")))
		pub, pubErr := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("FLUENTLAUNCHER_PLUGIN_PUBLIC_KEY")))
		if err != nil || pubErr != nil || len(b) != ed25519.PrivateKeySize || len(pub) != ed25519.PublicKeySize {
			return fmt.Errorf("plugin signing keys are missing or invalid")
		}
		key = b
		if !key.Public().(ed25519.PublicKey).Equal(ed25519.PublicKey(pub)) {
			return fmt.Errorf("plugin signing keys do not match")
		}
	}
	data, err := os.ReadFile("catalog.source.json")
	if err != nil {
		return err
	}
	var entries []entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}
	if err := os.MkdirAll("build", 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp("build", ".packages-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	seen := map[string]bool{}
	for i := range entries {
		e := &entries[i]
		if !regexp.MustCompile(`^official\.[a-z0-9_-]+$`).MatchString(e.ID) || seen[e.ID] {
			return fmt.Errorf("invalid or duplicate plugin ID: %s", e.ID)
		}
		seen[e.ID] = true
		dir := filepath.Join("plugins", strings.TrimPrefix(e.ID, "official."))
		manifestBytes, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
		if err != nil {
			return err
		}
		var m manifest
		if err := json.Unmarshal(manifestBytes, &m); err != nil {
			return err
		}
		if m.ID != e.ID || m.ProtocolVersion != 1 || !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(m.Version) || !regexp.MustCompile(`^[A-Za-z0-9_-]+\.exe$`).MatchString(m.Executable) {
			return fmt.Errorf("invalid manifest: %s", e.ID)
		}
		e.Version, e.ProtocolVersion = m.Version, m.ProtocolVersion
		e.Author, e.Homepage = m.Author, m.Homepage
		e.RequiredHostServices = m.RequiredHostServices
		e.HostServices = m.HostServices
		var resources []packageResource
		if m.Icon != "" {
			if !fs.ValidPath(m.Icon) || strings.ContainsAny(m.Icon, `\:`) || filepath.Ext(m.Icon) != ".png" {
				return fmt.Errorf("invalid icon path: %s", e.ID)
			}
			data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(m.Icon)))
			if err != nil {
				return err
			}
			if len(data) == 0 || len(data) > 1<<20 {
				return fmt.Errorf("invalid icon size: %s", e.ID)
			}
			resources = append(resources, packageResource{m.Icon, data})
			iconName := e.ID + "-" + m.Version + "-icon.png"
			if err := os.WriteFile(filepath.Join(stage, iconName), data, 0644); err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			e.Icon = &asset{URL: "https://github.com/" + repository + "/releases/download/" + tag + "/" + iconName, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
		}
		exe := filepath.Join(stage, m.Executable)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", exe, "./"+filepath.ToSlash(dir))
		cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
		name := e.ID + "-" + m.Version + "-windows-amd64.zip"
		archive := filepath.Join(stage, name)
		if err := packageZip(archive, manifestBytes, exe, m.Executable, resources...); err != nil {
			return err
		}
		if err := os.Remove(exe); err != nil {
			return err
		}
		blob, err := os.ReadFile(archive)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(blob)
		e.Platforms = map[string]asset{"windows-amd64": {URL: "https://github.com/" + repository + "/releases/download/" + tag + "/" + name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(blob))}}
	}
	catalog, err := json.MarshalIndent(struct {
		SchemaVersion int     `json:"schemaVersion"`
		Plugins       []entry `json:"plugins"`
	}{1, entries}, "", "  ")
	if err != nil {
		return err
	}
	catalog = append(catalog, '\n')
	if err := os.WriteFile(filepath.Join(stage, "catalog.json"), catalog, 0644); err != nil {
		return err
	}
	if key != nil {
		sum := sha256.Sum256(catalog)
		sig := base64.StdEncoding.EncodeToString(ed25519.Sign(key, sum[:])) + "\n"
		if err := os.WriteFile(filepath.Join(stage, "catalog.sig"), []byte(sig), 0644); err != nil {
			return err
		}
	}
	// Only this tool's generated output is replaced. Source directories are
	// never removed and each run uses a fresh staging directory.
	if err := os.RemoveAll(filepath.Join("build", "release")); err != nil {
		return err
	}
	if err := os.Rename(stage, filepath.Join("build", "release")); err != nil {
		return err
	}
	fmt.Println("Built plugin ZIPs and catalogue in build/release")
	return nil
}

type packageResource struct {
	name string
	data []byte
}

func packageZip(path string, manifest []byte, executable, name string, resources ...packageResource) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := zip.NewWriter(f)
	for _, file := range []struct {
		name string
		data []byte
	}{{"plugin.json", manifest}} {
		dst, err := w.Create(file.name)
		if err != nil {
			return err
		}
		if _, err := dst.Write(file.data); err != nil {
			return err
		}
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		return err
	}
	dst, err := w.Create(name)
	if err != nil {
		return err
	}
	if _, err := dst.Write(binary); err != nil {
		return err
	}
	for _, resource := range resources {
		dst, err := w.Create(resource.name)
		if err != nil {
			return err
		}
		if _, err := dst.Write(resource.data); err != nil {
			return err
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	return f.Close()
}
