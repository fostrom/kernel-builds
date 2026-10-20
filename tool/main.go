// This tool builds a target's kernel and packs its release. It resolves the
// kernel options with the driver lists, runs the kernel's own build, chooses
// the firmware the drivers need, packs CPU microcode, and writes the archive
// and its manifest.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const usage = `usage: kernel-tool <command> [flags]

  kernel     resolve a target's options and build its kernel, modules and device trees
  package    add firmware and microcode, and pack the archive and its manifest

  The steps of those two, one at a time:

  drivers    the driver lists into kernel options, on stdout
  firmware   install the firmware the built drivers ask for
  microcode  pack CPU microcode as the initrd the kernel's early loader reads
  manifest   describe what a build produced, as JSON
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	commands := map[string]func([]string) error{
		"kernel":    kernel,
		"package":   pack,
		"drivers":   drivers,
		"firmware":  firmware,
		"microcode": microcode,
		"manifest":  manifest,
	}
	run, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err := run(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}

// parse reads a command's flags and refuses anything after them, which would
// otherwise end parsing there and leave every later flag at its default.
func parse(flags *flag.FlagSet, args []string) {
	flags.Parse(args)
	if flags.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "%s: unexpected argument %q\n", flags.Name(), flags.Arg(0))
		os.Exit(2)
	}
}

// lists returns the files of one kind for a target: common.<kind>, which must
// exist, and <target>.<kind>, which need not.
func lists(config, target, kind string) ([]string, error) {
	common := filepath.Join(config, "common."+kind)
	if _, err := os.Stat(common); err != nil {
		return nil, err
	}
	paths := []string{common}
	own := filepath.Join(config, target+"."+kind)
	if _, err := os.Stat(own); err == nil {
		paths = append(paths, own)
	}
	return paths, nil
}

// entries reads list files a line at a time, without comments or blank lines.
func entries(paths []string) ([]string, error) {
	var out []string
	for _, path := range paths {
		lines, err := readLines(path)
		if err != nil {
			return nil, err
		}
		for _, line := range lines {
			line = trailingComment.ReplaceAllString(line, "")
			line = strings.TrimSpace(line)
			if line == "" || line[0] == '#' {
				continue
			}
			out = append(out, line)
		}
	}
	return out, nil
}

var trailingComment = regexp.MustCompile(`\s+#.*$`)

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func writeLines(path string, lines []string) error {
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Where a build keeps things: under / in the build container, and under a
// scratch directory in a test.
type layout struct{ config, sources, linux, out, vmlinux, release string }

func under(root string) layout {
	at := func(p string) string { return filepath.Join(root, p) }
	return layout{at("config"), at("sources"), at("linux"), at("out"), at("vmlinux"), at("release")}
}

// sourceDateEpoch is the commit's time, which everything that records a time
// records instead.
func sourceDateEpoch() (int64, error) {
	v := os.Getenv("SOURCE_DATE_EPOCH")
	if v == "" {
		return 0, nil
	}
	return strconv.ParseInt(v, 10, 64)
}

// buildTime is that time as date(1) prints it, which is the form the kernel
// puts in its version string.
func buildTime() (string, error) {
	epoch, err := sourceDateEpoch()
	return time.Unix(epoch, 0).UTC().Format("Mon Jan _2 15:04:05 MST 2006"), err
}

// run runs a command in dir, its output going to the build's.
func run(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// runQuiet shows a command's output only when it fails. merge_config.sh
// reports every option a driver list asked for and did not get.
func runQuiet(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Stderr.Write(out)
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// unpack extracts an archive's one top directory into dir, through tar since
// Go's standard library reads no xz.
func unpack(archive, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return run("", "tar", "-xf", archive, "-C", dir, "--strip-components=1")
}

// copyFile copies a file's contents, mode 0644, replacing whatever was there.
func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	os.Remove(dest)
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dest, 0o644)
}

// copyTree copies a file, or a directory into another, keeping links as links.
func copyTree(src, dest string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dest, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(to, 0o755)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return symlink(link, to)
		default:
			return copyFile(p, to)
		}
	})
}

// symlink makes a link at path to target, replacing whatever was there.
func symlink(target, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	os.Remove(path)
	return os.Symlink(target, path)
}
