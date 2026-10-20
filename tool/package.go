package main

import (
	"archive/tar"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// pack installs what goes beside a built kernel and packs the release: the
// firmware the built drivers ask for, the wifi regulatory database, CPU
// microcode, the Pi's own radio firmware, the licence texts of all of it, then
// the archive and its manifest.
func pack(args []string) error {
	flags := flag.NewFlagSet("package", flag.ExitOnError)
	name := flags.String("target", "", "amd64, arm64 or rpi")
	version := flags.String("version", "", "this repository's VERSION")
	commit := flags.String("commit", "unknown", "the commit built")
	root := flags.String(
		"root",
		"/",
		"where config/, sources/, out/ and vmlinux are, and release/ goes",
	)
	parse(flags, args)

	t, err := lookup(*name)
	if err != nil {
		return err
	}
	if *version == "" {
		return fmt.Errorf("no -version")
	}
	l := under(*root)
	epoch, err := sourceDateEpoch()
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "kernel-tool-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	source := func(archive string) string { return filepath.Join(l.sources, archive) }
	unpacked := func(dir string) string { return filepath.Join(tmp, dir) }
	firmware := filepath.Join(l.out, "lib/firmware")
	report := unpacked("report")

	if err := unpack(source("linux-firmware.tar.xz"), unpacked("linux-firmware")); err != nil {
		return err
	}
	err = installFirmware(
		l.config,
		*name,
		unpacked("linux-firmware"),
		l.out,
		report,
		[]string{l.vmlinux},
	)
	if err != nil {
		return err
	}
	if err := licences(unpacked("linux-firmware"), "linux-firmware", l.out); err != nil {
		return err
	}

	if err := unpack(source("wireless-regdb.tar.xz"), unpacked("wireless-regdb")); err != nil {
		return err
	}
	for _, f := range []string{"regulatory.db", "regulatory.db.p7s"} {
		if err := copyFile(
			filepath.Join(unpacked("wireless-regdb"), f),
			filepath.Join(firmware, f),
		); err != nil {
			return err
		}
	}
	if err := licences(unpacked("wireless-regdb"), "wireless-regdb", l.out); err != nil {
		return err
	}

	if t.microcode {
		if err := unpack(
			source("intel-microcode.tar.gz"),
			unpacked("intel-microcode"),
		); err != nil {
			return err
		}
		if err := licences(unpacked("intel-microcode"), "intel-microcode", l.out); err != nil {
			return err
		}
		intel := filepath.Join(unpacked("intel-microcode"), "intel-ucode")
		amd := filepath.Join(unpacked("linux-firmware"), "amd-ucode")
		if err := packMicrocode(intel, amd, filepath.Join(l.out, "boot/initrd.img")); err != nil {
			return err
		}
	}

	if t.piRadios {
		for _, archive := range piRadios.archives {
			if err := unpack(source(archive+".tar.gz"), unpacked(archive)); err != nil {
				return err
			}
			if err := licences(unpacked(archive), archive, l.out); err != nil {
				return err
			}
		}
		for _, c := range piRadios.copies {
			if err := copyTree(unpacked(c[0]), filepath.Join(l.out, c[1])); err != nil {
				return err
			}
		}
		for _, r := range piRadios.removes {
			if err := os.Remove(filepath.Join(l.out, r)); err != nil {
				return err
			}
		}
		for _, link := range piRadios.links {
			if err := symlink(link[1], filepath.Join(l.out, link[0])); err != nil {
				return err
			}
		}
	}

	release := fmt.Sprintf("kernel-%s-%s", *name, *version)
	if err := os.MkdirAll(l.release, 0o755); err != nil {
		return err
	}
	if err := writeArchive(
		l.out,
		filepath.Join(l.release, release+".tar.zst"),
		time.Unix(epoch, 0),
	); err != nil {
		return err
	}
	return writeManifest(
		l.out,
		*name,
		*version,
		*commit,
		report,
		filepath.Join(l.release, release+".manifest.json"),
	)
}

// The licence texts of each source a release carries something from, as the
// source ships them. They go in the archive under usr/share/licenses/<source>/.
var licenceFiles = map[string][]string{
	"linux": {"COPYING", "LICENSES"},
	"linux-firmware": {
		"WHENCE",
		"GPL-2",
		"GPL-3",
		"LICENSE",
		"LICENCE.*",
		"LICENSE.*",
		"wfx/LICENCE.wf200",
	},
	"wireless-regdb":       {"LICENSE"},
	"intel-microcode":      {"license"},
	"rpi-firmware-nonfree": {"debian/copyright"},
	"rpi-bluez-firmware": {
		"COPYING",
		"debian/copyright",
		"debian/firmware/broadcom/LICENSE.cypress",
		"debian/firmware/synaptics/LICENSE.synaptics",
	},
}

// licences copies a source's licence texts into out. A pattern that matches
// nothing is a source whose layout moved.
func licences(unpacked, source, out string) error {
	for _, pattern := range licenceFiles[source] {
		matches, err := filepath.Glob(filepath.Join(unpacked, pattern))
		if err != nil {
			return err
		}
		if len(matches) == 0 {
			return fmt.Errorf("%s has no %s", source, pattern)
		}
		for _, m := range matches {
			rel, _ := filepath.Rel(unpacked, m)
			if err := copyTree(
				m,
				filepath.Join(out, "usr/share/licenses", source, rel),
			); err != nil {
				return err
			}
		}
	}
	return nil
}

// The Pi's own wifi and Bluetooth firmware, from Raspberry Pi's archives, laid
// out as Raspberry Pi OS lays it out.
var piRadios = struct {
	archives []string
	copies   [][2]string // from an unpacked archive, to a path in the release
	removes  []string
	links    [][2]string // a link in the release, and what it points at
}{
	archives: []string{"rpi-firmware-nonfree", "rpi-bluez-firmware"},
	copies: [][2]string{
		{"rpi-firmware-nonfree/debian/config/brcm80211/brcm", "lib/firmware/brcm"},
		{"rpi-firmware-nonfree/debian/config/brcm80211/cypress", "lib/firmware/cypress"},
		{"rpi-firmware-nonfree/debian/rpi-brcmfmac.conf", "lib/modprobe.d/rpi-brcmfmac.conf"},
		{
			"rpi-bluez-firmware/debian/firmware/broadcom/BCM43430A1.hcd",
			"lib/firmware/brcm/BCM43430A1.hcd",
		},
		{
			"rpi-bluez-firmware/debian/firmware/broadcom/BCM43430B0.hcd",
			"lib/firmware/brcm/BCM43430B0.hcd",
		},
		{
			"rpi-bluez-firmware/debian/firmware/broadcom/BCM4345C0.hcd",
			"lib/firmware/brcm/BCM4345C0.hcd",
		},
		{
			"rpi-bluez-firmware/debian/firmware/broadcom/BCM4345C5.hcd",
			"lib/firmware/brcm/BCM4345C5.hcd",
		},
		{"rpi-bluez-firmware/debian/firmware/synaptics", "lib/firmware/synaptics"},
	},
	removes: []string{
		"lib/firmware/cypress/cyfmac43455-sdio-minimal.bin",
		"lib/firmware/cypress/README.txt",
	},
	links: [][2]string{
		{"lib/firmware/cypress/cyfmac43455-sdio.bin", "cyfmac43455-sdio-standard.bin"},
		{"etc/modprobe.d/rpi-brcmfmac.conf", "../../lib/modprobe.d/rpi-brcmfmac.conf"},
		{
			"lib/firmware/brcm/BCM43430A1.raspberrypi,3-model-b.hcd",
			"/lib/firmware/brcm/BCM43430A1.hcd",
		},
		{
			"lib/firmware/brcm/BCM43430A1.raspberrypi,model-zero-w.hcd",
			"/lib/firmware/brcm/BCM43430A1.hcd",
		},
		{
			"lib/firmware/brcm/BCM4345C0.raspberrypi,3-model-a-plus.hcd",
			"/lib/firmware/brcm/BCM4345C0.hcd",
		},
		{
			"lib/firmware/brcm/BCM4345C0.raspberrypi,3-model-b-plus.hcd",
			"/lib/firmware/brcm/BCM4345C0.hcd",
		},
		{
			"lib/firmware/brcm/BCM4345C0.raspberrypi,4-compute-module.hcd",
			"/lib/firmware/brcm/BCM4345C0.hcd",
		},
		{
			"lib/firmware/brcm/BCM4345C0.raspberrypi,4-model-b.hcd",
			"/lib/firmware/brcm/BCM4345C0.hcd",
		},
		{
			"lib/firmware/brcm/BCM4345C0.raspberrypi,5-model-b.hcd",
			"/lib/firmware/brcm/BCM4345C0.hcd",
		},
		{
			"lib/firmware/brcm/BCM4345C5.raspberrypi,4-compute-module.hcd",
			"/lib/firmware/brcm/BCM4345C5.hcd",
		},
		{"lib/firmware/brcm/BCM4345C5.raspberrypi,400.hcd", "/lib/firmware/brcm/BCM4345C5.hcd"},
		{
			"lib/firmware/brcm/BCM43430A1.raspberrypi,model-zero-2-w.hcd",
			"/lib/firmware/synaptics/SYN43430A1.hcd",
		},
		{
			"lib/firmware/brcm/BCM43430B0.raspberrypi,model-zero-2-w.hcd",
			"/lib/firmware/synaptics/SYN43430B0.hcd",
		},
	},
}

// writeArchive packs a tree as tar, compressed with zstd, with nothing in it
// that depends on the machine or the moment: names in order, one time, owner
// root, and modes normalised to 0644 and 0755.
func writeArchive(dir, dest string, mtime time.Time) error {
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	zstd := exec.Command("zstd", "-19", "-T4", "-q", "-c")
	zstd.Stdout, zstd.Stderr = out, os.Stderr
	pipe, err := zstd.StdinPipe()
	if err != nil {
		return err
	}
	if err := zstd.Start(); err != nil {
		return err
	}

	tw := tar.NewWriter(pipe)
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		hdr := &tar.Header{Name: filepath.ToSlash(rel), ModTime: mtime, Mode: 0o644}
		switch {
		case d.IsDir():
			hdr.Typeflag, hdr.Name, hdr.Mode = tar.TypeDir, hdr.Name+"/", 0o755
		case info.Mode()&fs.ModeSymlink != 0:
			hdr.Typeflag, hdr.Mode = tar.TypeSymlink, 0o777
			if hdr.Linkname, err = os.Readlink(p); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			hdr.Typeflag, hdr.Size = tar.TypeReg, info.Size()
			if info.Mode()&0o111 != 0 {
				hdr.Mode = 0o755
			}
		default:
			return fmt.Errorf("%s is neither a file, a directory nor a link", p)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err == nil {
		err = tw.Close()
	}
	if cerr := pipe.Close(); err == nil {
		err = cerr
	}
	if werr := zstd.Wait(); err == nil {
		err = werr
	}
	return err
}
