package plugin

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"time"
)

// BundleManifest describes one exported plugin registry.
type BundleManifest struct {
	Format           int            `json:"format"`
	ExportedAt       time.Time      `json:"exported_at"`
	Instance         BundleInstance `json:"instance"`
	DufflebagVersion string         `json:"dufflebag_version"`
	Plugins          []BundlePlugin `json:"plugins"`
}

// BundleInstance identifies where a bundle was exported.
type BundleInstance struct {
	Host         string `json:"host"`
	Organization string `json:"organization"`
}

// BundlePlugin describes one plugin in a bundle.
type BundlePlugin struct {
	Name     string          `json:"name"`
	Origin   BundleOrigin    `json:"origin"`
	Versions []BundleVersion `json:"versions"`
}

// BundleOrigin identifies the stored source of a plugin.
type BundleOrigin struct {
	Kind       string `json:"kind"`
	Repository string `json:"repository,omitempty"`
}

// BundleVersion describes one non-revoked plugin version.
type BundleVersion struct {
	Version   string       `json:"version"`
	Platforms []string     `json:"platforms"`
	Files     []BundleFile `json:"files"`
}

// BundleFile describes one archive entry and its stored object.
type BundleFile struct {
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	ObjectKey string `json:"-"`
}

// WriteBundle writes a gzip-compressed tar with the manifest first.
func WriteBundle(w io.Writer, manifest BundleManifest, open func(BundleFile) (io.ReadCloser, error)) error {
	gz := gzip.NewWriter(w)
	gz.ModTime = manifest.ExportedAt
	tw := tar.NewWriter(gz)
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode bundle manifest: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o644, Size: int64(len(encoded)), ModTime: manifest.ExportedAt}); err != nil {
		return fmt.Errorf("write bundle manifest header: %w", err)
	}
	if _, err := tw.Write(encoded); err != nil {
		return fmt.Errorf("write bundle manifest: %w", err)
	}
	for _, bundledPlugin := range manifest.Plugins {
		for _, version := range bundledPlugin.Versions {
			for _, file := range version.Files {
				name := path.Join("plugins", bundledPlugin.Name, version.Version, file.Name)
				if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: file.Size, ModTime: manifest.ExportedAt}); err != nil {
					return fmt.Errorf("write bundle file header %q: %w", name, err)
				}
				body, err := open(file)
				if err != nil {
					return fmt.Errorf("open bundle file %q: %w", name, err)
				}
				written, copyErr := io.Copy(tw, body)
				closeErr := body.Close()
				if copyErr != nil {
					return fmt.Errorf("write bundle file %q: %w", name, copyErr)
				}
				if closeErr != nil {
					return fmt.Errorf("close bundle file %q: %w", name, closeErr)
				}
				if written != file.Size {
					return fmt.Errorf("write bundle file %q: got %d bytes, want %d", name, written, file.Size)
				}
			}
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("close bundle tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("close bundle gzip: %w", err)
	}
	return nil
}
