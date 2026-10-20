package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The manifest says what a kernel build produced, so it can be read without
// unpacking the archive: the kernel release and compiler, its modules, the
// firmware and why each file is there, and the hash of every file. Nothing in it records
// when or where it was built, so two builds of one commit agree on it too.
type manifestDoc struct {
	Target   string         `json:"target"`
	Version  string         `json:"version"`
	Commit   string         `json:"commit"`
	Kernel   kernelInfo     `json:"kernel"`
	Firmware firmwareReport `json:"firmware"`
	Files    []fileInfo     `json:"files"`
}

type kernelInfo struct {
	Release  string   `json:"release"`
	Compiler string   `json:"compiler"`
	Modules  []string `json:"modules"`
}

type firmwareReport struct {
	Installed []string `json:"installed"`
	Unlisted  []string `json:"unlisted"`
	Missing   []string `json:"missing"`
	LeftOut   []string `json:"left_out"`
	NoDriver  []string `json:"no_driver"`
}

type fileInfo struct {
	Path   string `json:"path"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Link   string `json:"link,omitempty"`
}

func manifest(args []string) error {
	flags := flag.NewFlagSet("manifest", flag.ExitOnError)
	root := flags.String("root", "", "the tree the archive is made from")
	target := flags.String("target", "", "the target")
	version := flags.String("version", "", "this repository's VERSION")
	commit := flags.String("commit", "unknown", "the commit built")
	report := flags.String("report", "", "the firmware command's report directory")
	out := flags.String("out", "", "the manifest to write")
	parse(flags, args)
	return writeManifest(*root, *target, *version, *commit, *report, *out)
}

func writeManifest(root, target, version, commit, report, out string) error {
	doc := manifestDoc{Target: target, Version: strings.TrimSpace(version), Commit: commit}

	config, err := readConfig(filepath.Join(root, "kernel.config"))
	if err != nil {
		return err
	}
	doc.Kernel.Compiler = strings.Trim(config["CONFIG_CC_VERSION_TEXT"], `"`)

	releases, err := os.ReadDir(filepath.Join(root, "lib/modules"))
	if err != nil {
		return err
	}
	for _, r := range releases {
		doc.Kernel.Release = r.Name()
	}

	reports := map[string]*[]string{
		"installed":             &doc.Firmware.Installed,
		"unlisted":              &doc.Firmware.Unlisted,
		"left-out":              &doc.Firmware.LeftOut,
		"no-driver":             &doc.Firmware.NoDriver,
		"not-in-linux-firmware": &doc.Firmware.Missing,
	}
	for name, into := range reports {
		lines, err := readLines(filepath.Join(report, name))
		if err != nil {
			return err
		}
		*into = nonNil(lines)
	}

	// A name linux-firmware lacks is only missing if nothing else, Raspberry
	// Pi's radio firmware or the regulatory database, put it in the tree.
	var missing []string
	for _, name := range doc.Firmware.Missing {
		if !lands(root, path.Join("lib/firmware", name)) {
			missing = append(missing, name)
		}
	}
	doc.Firmware.Missing = nonNil(missing)

	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, ".ko") {
			doc.Kernel.Modules = append(
				doc.Kernel.Modules,
				strings.TrimSuffix(path.Base(rel), ".ko"),
			)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			to, err := os.Readlink(p)
			if err != nil {
				return err
			}
			doc.Files = append(doc.Files, fileInfo{Path: rel, Link: to})
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		doc.Files = append(
			doc.Files,
			fileInfo{Path: rel, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])},
		)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(doc.Kernel.Modules)

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(out, append(data, '\n'), 0o644)
}

var configValue = regexp.MustCompile(`^(CONFIG_[A-Za-z0-9_]+)=(.*)$`)

func readConfig(file string) (map[string]string, error) {
	lines, err := readLines(file)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range lines {
		if m := configValue.FindStringSubmatch(line); m != nil {
			out[m[1]] = m[2]
		}
	}
	return out, nil
}

// lands follows links the way a device will, with an absolute link resolved
// inside the tree rather than on the machine building it.
func lands(root, rel string) bool {
	for range 8 {
		p := filepath.Join(root, rel)
		info, err := os.Lstat(p)
		if err != nil {
			return false
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			return true
		}
		to, err := os.Readlink(p)
		if err != nil {
			return false
		}
		if path.IsAbs(to) {
			rel = strings.TrimPrefix(path.Clean(to), "/")
		} else {
			rel = path.Join(path.Dir(rel), to)
		}
	}
	return false
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
