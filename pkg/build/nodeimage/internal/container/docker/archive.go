/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package docker contains helpers for working with docker
// This package has no stability guarantees whatsoever!
package docker

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"sigs.k8s.io/kind/pkg/errors"
)

// ioContainerdImageName appears as an annotation in a manifest entry in "index.json".
const ioContainerdImageName = "io.containerd.image.name"

// GetArchiveTags obtains a list of "repo:tag" docker image tags from a
// given docker image archive (tarball) path
// compatible with all known specs:
// https://github.com/moby/moby/blob/master/image/spec/v1.md
// https://github.com/moby/moby/blob/master/image/spec/v1.1.md
// https://github.com/moby/moby/blob/master/image/spec/v1.2.md
// https://github.com/opencontainers/image-spec/blob/v1.0.2/image-index.md  (annotation "io.containerd.image.name")
func GetArchiveTags(path string) ([]string, error) {
	// open the archive and find the repositories entry
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	var hdr *tar.Header
	for {
		hdr, err = tr.Next()
		if err == io.EOF {
			return nil, errors.New("could not find image metadata")
		}
		if err != nil {
			return nil, err
		}
		if hdr.Name == "manifest.json" || hdr.Name == "repositories" || hdr.Name == "index.json" {
			break
		}
	}
	// read and parse the tags
	b, err := io.ReadAll(tr)
	if err != nil {
		return nil, err
	}
	res := []string{}
	// parse
	if hdr.Name == "repositories" {
		repoTags, err := parseRepositories(b)
		if err != nil {
			return nil, err
		}
		// convert to tags in the docker CLI sense
		for repo, tags := range repoTags {
			for tag := range tags {
				res = append(res, fmt.Sprintf("%s:%s", repo, tag))
			}
		}
	} else if hdr.Name == "manifest.json" {
		manifest, err := parseDockerV1Manifest(b)
		if err != nil {
			return nil, err
		}
		res = append(res, manifest[0].RepoTags...)
	} else if hdr.Name == "index.json" {
		var idx ocispec.Index
		if err := json.Unmarshal(b, &idx); err != nil {
			return nil, err
		}
		for _, mani := range idx.Manifests {
			if repoTag, ok := mani.Annotations[ioContainerdImageName]; ok {
				res = append(res, repoTag)
			}
		}
	}
	return res, nil
}

// EditArchive applies edit to reader's image repositories,
// IE the repository part of repository:tag in image tags
// This supports v1 / v1.1 / v1.2 Docker Image Archives and OCI Image Spec v1.0 Archives.
//
// editRepositories should be a function that returns the input or an edited
// form, where the input is the image repository
//
// https://github.com/moby/moby/blob/master/image/spec/v1.md
// https://github.com/moby/moby/blob/master/image/spec/v1.1.md
// https://github.com/moby/moby/blob/master/image/spec/v1.2.md
// https://github.com/opencontainers/image-spec/blob/v1.0.2/image-index.md  (annotation "io.containerd.image.name")
func EditArchive(reader io.Reader, writer io.Writer, editRepositories func(string) string, architectureOverride string) error {
	tarReader := tar.NewReader(reader)
	tarWriter := tar.NewWriter(writer)
	// iterate all entries in the tarball
	for {
		// read an entry
		hdr, err := tarReader.Next()
		if err == io.EOF {
			return tarWriter.Close()
		} else if err != nil {
			return err
		}
		b, err := io.ReadAll(tarReader)
		if err != nil {
			return err
		}

		// edit the repostories and manifests files when we find them
		if hdr.Name == "repositories" {
			b, err = editRepositoriesFile(b, editRepositories)
			if err != nil {
				return err
			}
			hdr.Size = int64(len(b))
		} else if hdr.Name == "manifest.json" {
			b, err = editManifestRepositories(b, editRepositories)
			if err != nil {
				return err
			}
			hdr.Size = int64(len(b))
		} else if hdr.Name == "index.json" {
			b, err = editIndexJSON(b, editRepositories)
			if err != nil {
				return err
			}
			hdr.Size = int64(len(b))
			// edit image config when we find that
		} else if strings.HasSuffix(hdr.Name, ".json") {
			if architectureOverride != "" {
				b, err = editConfigArchitecture(b, architectureOverride)
				if err != nil {
					return err
				}
				hdr.Size = int64(len(b))
			}
		}

		// write to the output tarball
		if err := tarWriter.WriteHeader(hdr); err != nil {
			return err
		}
		if len(b) > 0 {
			if _, err := tarWriter.Write(b); err != nil {
				return err
			}
		}
	}
}

/* helpers */

func editConfigArchitecture(raw []byte, architectureOverride string) ([]byte, error) {
	var cfg map[string]interface{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	const architecture = "architecture"
	if _, ok := cfg[architecture]; !ok {
		return raw, nil
	}
	cfg[architecture] = architectureOverride
	return json.Marshal(cfg)
}

// archiveRepositories represents repository:tag:ref
//
// https://github.com/moby/moby/blob/master/image/spec/v1.md
// https://github.com/moby/moby/blob/master/image/spec/v1.1.md
// https://github.com/moby/moby/blob/master/image/spec/v1.2.md
type archiveRepositories map[string]map[string]string

func editRepositoriesFile(raw []byte, editRepositories func(string) string) ([]byte, error) {
	tags, err := parseRepositories(raw)
	if err != nil {
		return nil, err
	}

	fixed := make(archiveRepositories)
	for repository, tagsToRefs := range tags {
		fixed[editRepositories(repository)] = tagsToRefs
	}

	return json.Marshal(fixed)
}

// https://github.com/moby/moby/blob/master/image/spec/v1.2.md#combined-image-json--filesystem-changeset-format
type metadataEntry struct {
	Config       string                               `json:"Config"`
	RepoTags     []string                             `json:"RepoTags"`
	Layers       []string                             `json:"Layers"`
	LayerSources map[digest.Digest]ocispec.Descriptor `json:"LayerSources,omitempty"` // since Docker v25
}

// applies
func editManifestRepositories(raw []byte, editRepositories func(string) string) ([]byte, error) {
	var entries []metadataEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}

	for i, entry := range entries {
		fixed := make([]string, len(entry.RepoTags))
		for i, tag := range entry.RepoTags {
			parts := strings.Split(tag, ":")
			if len(parts) > 2 {
				return nil, fmt.Errorf("invalid repotag: %s", entry)
			}
			parts[0] = editRepositories(parts[0])
			fixed[i] = strings.Join(parts, ":")
		}

		entries[i].RepoTags = fixed
	}

	return json.Marshal(entries)
}

// editIndexJSON edits "index.json" that appears in OCI Image Spec v1 archives.
func editIndexJSON(raw []byte, editRepositories func(string) string) ([]byte, error) {
	var idx ocispec.Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, err
	}

	for i := range idx.Manifests {
		mani := &idx.Manifests[i]
		if repoTag, ok := mani.Annotations[ioContainerdImageName]; ok {
			parts := strings.Split(repoTag, ":")
			if len(parts) > 2 {
				return nil, fmt.Errorf("invalid repotag: %s", repoTag)
			}
			parts[0] = editRepositories(parts[0])
			mani.Annotations[ioContainerdImageName] = strings.Join(parts, ":")
		}
	}

	// NOTE: we may lose some JSON fields if the original JSON was produced with a newer version of OCI Image Spec.
	return json.Marshal(idx)
}

// returns repository:tag:ref
func parseRepositories(data []byte) (archiveRepositories, error) {
	var repoTags archiveRepositories
	if err := json.Unmarshal(data, &repoTags); err != nil {
		return nil, err
	}
	return repoTags, nil
}

// parseDockerV1Manifest parses Docker Image Spec v1 manifest (not OCI Image Spec manifest)
// https://github.com/moby/moby/blob/v20.10.22/image/spec/v1.2.md#combined-image-json--filesystem-changeset-format
func parseDockerV1Manifest(data []byte) ([]metadataEntry, error) {
	var entries []metadataEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}
