package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// kernel resolves a target's options, then builds its kernel, modules and
// device trees into out/. vmlinux is kept beside out/ for the package step,
// which reads the firmware built-in drivers ask for out of it.
func kernel(args []string) error {
	flags := flag.NewFlagSet("kernel", flag.ExitOnError)
	name := flags.String("target", "", "amd64, arm64 or rpi")
	root := flags.String("root", "/", "where config/ and sources/ are, and out/ goes")
	parse(flags, args)

	t, err := lookup(*name)
	if err != nil {
		return err
	}
	l := under(*root)
	stamp, err := buildTime()
	if err != nil {
		return err
	}
	for k, v := range map[string]string{
		"ARCH":                   t.arch,
		"CROSS_COMPILE":          t.cross,
		"KBUILD_BUILD_TIMESTAMP": stamp,
		"KBUILD_BUILD_USER":      "kernel-builds",
		"KBUILD_BUILD_HOST":      "fostrom",
		"KBUILD_BUILD_VERSION":   "1",
	} {
		os.Setenv(k, v)
	}

	source, err := filepath.Glob(filepath.Join(l.sources, "linux-*.tar.*"))
	if err != nil || len(source) != 1 {
		return fmt.Errorf("want one linux-*.tar.* in %s, found %v", l.sources, source)
	}
	if err := unpack(source[0], l.linux); err != nil {
		return err
	}

	// Our options over the kernel's defaults, or over the defconfig the target
	// starts from. Then the driver lists, for whatever that left unset. Then
	// all of it again, with our options last so they win.
	options := []string{
		filepath.Join(l.config, "common.config"),
		filepath.Join(l.config, *name+".config"),
	}
	if err := mergeConfig(l.linux, t.start, options...); err != nil {
		return err
	}
	driversFile := filepath.Join(l.config, "drivers.config")
	f, err := os.Create(driversFile)
	if err != nil {
		return err
	}
	err = writeDrivers(l.config, *name, l.linux, filepath.Join(l.linux, ".config"), f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := mergeConfig(
		l.linux,
		t.start,
		append([]string{driversFile}, options...)...); err != nil {
		return err
	}

	goals := []string{"-j" + strconv.Itoa(runtime.NumCPU()), t.image, "modules"}
	if t.dtbs {
		goals = append(goals, "dtbs")
	}
	if err := run(l.linux, "make", goals...); err != nil {
		return err
	}
	if err := run(
		l.linux,
		"make",
		"modules_install",
		"INSTALL_MOD_PATH="+l.out,
		"INSTALL_MOD_STRIP=1",
	); err != nil {
		return err
	}
	builds, _ := filepath.Glob(filepath.Join(l.out, "lib/modules/*/build"))
	sources, _ := filepath.Glob(filepath.Join(l.out, "lib/modules/*/source"))
	for _, link := range append(builds, sources...) {
		if err := os.Remove(link); err != nil {
			return err
		}
	}
	if err := copyFile(
		filepath.Join(l.linux, "arch", t.arch, "boot", t.image),
		filepath.Join(l.out, "boot", t.image),
	); err != nil {
		return err
	}
	if t.dtbs {
		if err := run(
			l.linux,
			"make",
			"dtbs_install",
			"INSTALL_DTBS_PATH="+filepath.Join(l.out, "boot/dtbs"),
		); err != nil {
			return err
		}
	}
	if err := copyFile(
		filepath.Join(l.linux, ".config"),
		filepath.Join(l.out, "kernel.config"),
	); err != nil {
		return err
	}
	if err := licences(l.linux, "linux", l.out); err != nil {
		return err
	}
	if err := run(l.linux, t.cross+"objcopy", "--strip-debug", "vmlinux", l.vmlinux); err != nil {
		return err
	}
	return os.RemoveAll(l.linux)
}

// mergeConfig resolves option files with the kernel's own merge_config.sh,
// later files winning, over a defconfig when one is given.
func mergeConfig(linux, start string, files ...string) error {
	args := files
	if start != "" {
		args = append([]string{start}, files...)
	}
	return runQuiet(linux, "scripts/kconfig/merge_config.sh", args...)
}
