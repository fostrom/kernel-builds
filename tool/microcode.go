package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// The kernel's early loader looks for CPU microcode in an uncompressed newc
// cpio handed to it as an initrd, at kernel/x86/microcode/<vendor>.bin, and
// applies it before deciding which CPU features and mitigations to use. With
// no init in the archive the kernel then mounts its root as it would without
// one.
func microcode(args []string) error {
	flags := flag.NewFlagSet("microcode", flag.ExitOnError)
	intel := flags.String("intel", "", "Intel's intel-ucode directory")
	amd := flags.String("amd", "", "linux-firmware's amd-ucode directory")
	out := flags.String("out", "", "the initrd to write")
	parse(flags, args)
	return packMicrocode(*intel, *amd, *out)
}

func packMicrocode(intel, amd, out string) error {
	genuineIntel, err := concat(intel, regexp.MustCompile(`^[0-9a-f]{2}-[0-9a-f]{2}-[0-9a-f]{2}$`))
	if err != nil {
		return err
	}
	authenticAMD, err := concat(amd, regexp.MustCompile(`^microcode_amd.*\.bin$`))
	if err != nil {
		return err
	}

	var c cpio
	c.dir("kernel")
	c.dir("kernel/x86")
	c.dir("kernel/x86/microcode")
	c.file("kernel/x86/microcode/AuthenticAMD.bin", authenticAMD)
	c.file("kernel/x86/microcode/GenuineIntel.bin", genuineIntel)
	c.trailer()
	if err := os.WriteFile(out, c.buf.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Printf(
		"microcode: %d KiB for Intel, %d KiB for AMD\n",
		len(genuineIntel)>>10,
		len(authenticAMD)>>10,
	)
	return nil
}

// concat joins every file in a directory whose name matches, in name order.
func concat(dir string, name *regexp.Regexp) ([]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && name.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var all []byte
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		all = append(all, data...)
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("no microcode in %s", dir)
	}
	return all, nil
}

// A newc cpio with nothing in it that depends on where or when it was made:
// owner root, every time zero, inodes numbered in order.
type cpio struct {
	buf bytes.Buffer
	ino int
}

func (c *cpio) dir(name string)               { c.entry(name, 0o040755, 2, nil) }
func (c *cpio) file(name string, data []byte) { c.entry(name, 0o100644, 1, data) }
func (c *cpio) trailer()                      { c.ino = -1; c.entry("TRAILER!!!", 0, 1, nil) }

func (c *cpio) entry(name string, mode, nlink int, data []byte) {
	c.ino++
	fmt.Fprintf(&c.buf, "070701%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
		c.ino, mode, 0, 0, nlink, 0, len(data), 0, 0, 0, 0, len(name)+1, 0)
	c.buf.WriteString(name)
	c.buf.WriteByte(0)
	c.pad()
	c.buf.Write(data)
	c.pad()
}

func (c *cpio) pad() {
	for c.buf.Len()%4 != 0 {
		c.buf.WriteByte(0)
	}
}
