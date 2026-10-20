package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// A firmware file is installed when a built module, or the kernel image,
// lists it in its module info; when its name appears whole in a built module
// or the kernel image and linux-firmware's WHENCE documents it; or when a
// `<pattern> <module>` line matches it and that module was built. A
// `!<pattern>` line leaves out the file, or any link on the way to it. An
// Intel wifi name stands for the newest file at or below the API it gives.
func firmware(args []string) error {
	flags := flag.NewFlagSet("firmware", flag.ExitOnError)
	config := flags.String("config", "config", "directory holding the firmware lists")
	target := flags.String("target", "", "the target whose list to read beside common's")
	from := flags.String("from", "", "an unpacked linux-firmware")
	root := flags.String("root", "", "the root the modules are installed under, and firmware goes")
	report := flags.String("report", "", "where to write what was installed and why")
	var images multiFlag
	flags.Var(&images, "image", "a kernel image to read built-in drivers' names from (repeatable)")
	parse(flags, args)
	return installFirmware(*config, *target, *from, *root, *report, images)
}

func installFirmware(config, target, from, root, report string, images []string) error {
	paths, err := lists(config, target, "firmware")
	if err != nil {
		return err
	}
	lines, err := entries(paths)
	if err != nil {
		return err
	}
	rules, err := parseFirmwareLists(lines)
	if err != nil {
		return err
	}
	tree, err := loadTree(from)
	if err != nil {
		return err
	}
	mods, err := findModules(root)
	if err != nil {
		return err
	}

	declared, err := mods.declared()
	if err != nil {
		return err
	}
	unlisted, err := tree.unlisted(
		append(append([]string{}, mods.loadable...), images...),
		declared,
	)
	if err != nil {
		return err
	}
	built, err := mods.built()
	if err != nil {
		return err
	}

	named, missing := map[string]bool{}, map[string]bool{}
	for _, name := range append(sorted(declared), unlisted...) {
		found := tree.resolve(name)
		if len(found) == 0 {
			missing[name] = true
		}
		for _, p := range found {
			named[p] = true
		}
	}

	var idle []string
	for _, inc := range rules.includes {
		if !built[inc.module] {
			idle = append(idle, inc.pattern+" "+inc.module)
			continue
		}
		if err := limitIsCurrent(inc.pattern, declared); err != nil {
			return err
		}
		found := tree.resolve(inc.pattern)
		if len(found) == 0 {
			return fmt.Errorf(
				"%s, for %s, matches nothing in this linux-firmware",
				inc.pattern,
				inc.module,
			)
		}
		for _, p := range found {
			named[p] = true
		}
	}

	kept, leftOut := map[string]bool{}, map[string]bool{}
	for _, p := range sorted(named) {
		chain := tree.chain(p)
		into := kept
		for _, hop := range chain {
			if matchesAny(rules.excludes, hop) {
				into = leftOut
				break
			}
		}
		for _, hop := range chain {
			into[hop] = true
		}
	}

	if err := tree.install(sorted(kept), filepath.Join(root, "lib/firmware")); err != nil {
		return err
	}

	if err := os.MkdirAll(report, 0o755); err != nil {
		return err
	}
	sort.Strings(idle)
	for name, lines := range map[string][]string{
		"installed":             sorted(kept),
		"left-out":              sorted(leftOut),
		"not-in-linux-firmware": sorted(missing),
		"declared":              sorted(declared),
		"unlisted":              unlisted,
		"no-driver":             idle,
	} {
		if err := writeLines(filepath.Join(report, name), lines); err != nil {
			return err
		}
	}

	var size int64
	for p := range kept {
		if tree.files[p] {
			info, err := os.Stat(filepath.Join(tree.dir, p))
			if err != nil {
				return err
			}
			size += info.Size()
		}
	}
	fmt.Printf(
		"firmware: %d files, %d MiB. %d names from the built drivers and %d they ask for and never list, "+
			"%d of them not in linux-firmware, %d files left out by the lists, %d of their lines for drivers that were not built\n",
		len(kept),
		size>>20,
		len(declared),
		len(unlisted),
		len(missing),
		len(leftOut),
		len(idle),
	)
	return nil
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

type include struct{ pattern, module string }

type firmwareRules struct {
	includes []include
	excludes []*regexp.Regexp
}

func parseFirmwareLists(lines []string) (firmwareRules, error) {
	var rules firmwareRules
	for _, line := range lines {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 1 && strings.HasPrefix(fields[0], "!"):
			rules.excludes = append(rules.excludes, glob(fields[0][1:]))
		case len(fields) == 2 && !strings.HasPrefix(fields[0], "!"):
			rules.includes = append(rules.includes, include{fields[0], fields[1]})
		default:
			return rules, fmt.Errorf("`%s` is neither `<pattern> <module>` nor `!<pattern>`", line)
		}
	}
	return rules, nil
}

// glob: * stays within a directory, ** crosses them, ? is one character.
func glob(pattern string) *regexp.Regexp {
	re := regexp.QuoteMeta(pattern)
	re = strings.ReplaceAll(re, `\*\*`, `.*`)
	re = strings.ReplaceAll(re, `\*`, `[^/]*`)
	re = strings.ReplaceAll(re, `\?`, `[^/]`)
	return regexp.MustCompile("^" + re + "$")
}

// A linux-firmware tree: its files, and the links WHENCE declares, which the
// release tarball does not carry as links.
type tree struct {
	dir    string
	files  map[string]bool
	links  map[string]string
	whence []string
}

var (
	whenceFile = regexp.MustCompile(`^(?:File|RawFile):\s+"?(.+?)"?\s*$`)
	whenceLink = regexp.MustCompile(`^Link:\s+(\S+)\s+->\s+(\S+)\s*$`)
)

func loadTree(dir string) (*tree, error) {
	t := &tree{dir: dir, files: map[string]bool{}, links: map[string]string{}}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != dir && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(dir, p)
			t.files[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	lines, err := readLines(filepath.Join(dir, "WHENCE"))
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		if m := whenceFile.FindStringSubmatch(line); m != nil {
			t.whence = append(t.whence, m[1])
		} else if m := whenceLink.FindStringSubmatch(line); m != nil {
			t.links[m[1]] = m[2]
		}
	}
	return t, nil
}

func (t *tree) targetOf(link string) string {
	return strings.TrimPrefix(path.Clean("/"+path.Join(path.Dir(link), t.links[link])), "/")
}

// chain is a path and every link it goes through, the final file first. A
// loop gives nothing.
func (t *tree) chain(p string) []string {
	var seen []string
	for {
		if slices.Contains(seen, p) {
			return nil
		}
		seen = append(seen, p)
		if _, ok := t.links[p]; !ok {
			break
		}
		p = t.targetOf(p)
	}
	out := make([]string, len(seen))
	for i, s := range seen {
		out[len(seen)-1-i] = s
	}
	return out
}

func (t *tree) exists(p string) bool {
	c := t.chain(p)
	return len(c) > 0 && t.files[c[0]]
}

func (t *tree) names() []string {
	out := make([]string, 0, len(t.files)+len(t.links))
	for f := range t.files {
		out = append(out, f)
	}
	for l := range t.links {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

func (t *tree) resolve(name string) []string {
	switch {
	case strings.ContainsAny(name, "*?["):
		re := glob(name)
		var out []string
		for _, p := range t.names() {
			if re.MatchString(p) && t.exists(p) {
				out = append(out, p)
			}
		}
		return out
	case t.exists(name):
		return []string{name}
	default:
		return t.newestBelow(name)
	}
}

var (
	iwlwifiName  = regexp.MustCompile(`^(.*iwlwifi-.*-)(c?)(\d+)\.ucode$`)
	iwlwifiTail  = regexp.MustCompile(`^(c?)(\d+)\.ucode$`)
	iwlwifiLimit = regexp.MustCompile(`^iwlwifi-([^-]+)-.*-(c?\d+)\.ucode$`)
)

// newestBelow does what iwlwifi does with the name it asks for: the newest
// file at or below that API version. The newest chips count in core
// releases, written with a c, and below those the driver goes back to plain
// numbers no higher than 101.
func (t *tree) newestBelow(name string) []string {
	m := iwlwifiName.FindStringSubmatch(name)
	if m == nil {
		return nil
	}
	stem, core := m[1], m[2]
	wanted, _ := strconv.Atoi(m[3])
	best, bestC, bestN := "", "", -1
	for _, p := range t.names() {
		if !strings.HasPrefix(p, stem) {
			continue
		}
		tail := iwlwifiTail.FindStringSubmatch(strings.TrimPrefix(p, stem))
		if tail == nil {
			continue
		}
		c := tail[1]
		n, _ := strconv.Atoi(tail[2])
		if !((c == core && n <= wanted) || (core == "c" && c == "" && n <= 101)) || !t.exists(p) {
			continue
		}
		if bestN < 0 || c > bestC || (c == bestC && (n > bestN || (n == bestN && p > best))) {
			best, bestC, bestN = p, c, n
		}
	}
	if bestN < 0 {
		return nil
	}
	return []string{best}
}

// limitIsCurrent holds a versioned iwlwifi line to the limit the built driver
// asks for on that MAC, so a kernel that moves the limit fails here by name
// rather than installing firmware one step too old.
func limitIsCurrent(pattern string, declared map[string]bool) error {
	m := iwlwifiLimit.FindStringSubmatch(pattern)
	if m == nil {
		return nil
	}
	current := regexp.MustCompile(`^iwlwifi-` + regexp.QuoteMeta(m[1]) + `-.*-` + m[2] + `\.ucode$`)
	for name := range declared {
		if current.MatchString(name) {
			return nil
		}
	}
	return fmt.Errorf("%s asks for %s, and the built iwlwifi asks for that on no %s chip. "+
		"Its limit moved, and this line has to follow it", pattern, m[2], m[1])
}

// unlisted finds names WHENCE documents that appear as a whole string in a
// built module or kernel image, without being in any module info: rtl8723bs
// lists no firmware at all, and rt2800pci leaves one chip out.
func (t *tree) unlisted(files []string, declared map[string]bool) ([]string, error) {
	names := map[string]bool{}
	for _, n := range t.whence {
		names[n] = true
	}
	for l := range t.links {
		names[l] = true
	}
	found := map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for s := range bytes.SplitSeq(data, []byte{0}) {
			if names[string(s)] && !declared[string(s)] {
				found[string(s)] = true
			}
		}
	}
	return sorted(found), nil
}

func (t *tree) install(paths []string, dir string) error {
	for _, p := range paths {
		dest := filepath.Join(dir, p)
		var err error
		if to, ok := t.links[p]; ok {
			err = symlink(to, dest)
		} else {
			err = copyFile(filepath.Join(t.dir, p), dest)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// The modules a kernel installed under <root>/lib/modules/<release>.
type modules struct {
	loadable []string
	modinfo  []string
	builtin  []string
}

func findModules(root string) (*modules, error) {
	m := &modules{}
	base := filepath.Join(root, "lib/modules")
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		switch {
		case strings.HasSuffix(name, ".ko"):
			m.loadable = append(m.loadable, p)
		case strings.Contains(name, ".ko."):
			return fmt.Errorf("%s is compressed, and module info is read uncompressed", p)
		case name == "modules.builtin.modinfo" && filepath.Dir(filepath.Dir(p)) == base:
			m.modinfo = append(m.modinfo, p)
		case name == "modules.builtin" && filepath.Dir(filepath.Dir(p)) == base:
			m.builtin = append(m.builtin, p)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if len(m.loadable) == 0 {
		return nil, fmt.Errorf(
			"no kernel modules under %s, so the kernel has not been installed",
			base,
		)
	}
	sort.Strings(m.loadable)
	return m, nil
}

var firmwareInfo = regexp.MustCompile(`(?:\x00|\.)firmware=([^\x00]+)`)

// declared is every name a built driver lists in its module info, and the
// built-in drivers' too.
func (m *modules) declared() (map[string]bool, error) {
	out := map[string]bool{}
	for _, f := range append(append([]string{}, m.loadable...), m.modinfo...) {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for _, match := range firmwareInfo.FindAllSubmatch(data, -1) {
			out[string(match[1])] = true
		}
	}
	return out, nil
}

// built is every module by the name modprobe knows it by, built in or not.
func (m *modules) built() (map[string]bool, error) {
	out := map[string]bool{}
	add := func(p string) {
		out[strings.ReplaceAll(strings.TrimSuffix(filepath.Base(p), ".ko"), "-", "_")] = true
	}
	for _, p := range m.loadable {
		add(p)
	}
	for _, list := range m.builtin {
		lines, err := readLines(list)
		if err != nil {
			return nil, err
		}
		for _, line := range lines {
			if line = strings.TrimSpace(line); line != "" {
				add(line)
			}
		}
	}
	return out, nil
}
