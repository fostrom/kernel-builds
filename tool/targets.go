package main

import "fmt"

// What differs between the targets. The rest of a build is the same for all.
type target struct {
	arch      string // the kernel's ARCH
	cross     string // CROSS_COMPILE, the compiler's prefix
	start     string // a defconfig our options go over, rather than the kernel's defaults
	image     string // the kernel image, under arch/<arch>/boot
	dtbs      bool   // device trees and overlays, for a board that boots without UEFI
	microcode bool   // CPU microcode, as an initrd for the kernel's early loader
	piRadios  bool   // Raspberry Pi's firmware for its own wifi and Bluetooth
}

var targets = map[string]target{
	// x86-64 machines that boot through UEFI.
	"amd64": {
		arch:      "x86_64",
		image:     "bzImage",
		microcode: true,
	},

	// arm64 servers and SystemReady or EBBR boards, booted through UEFI. The
	// kernel's arm64 defconfig covers every SoC family.
	"arm64": {
		arch:  "arm64",
		cross: "aarch64-linux-gnu-",
		start: "arch/arm64/configs/defconfig",
		image: "Image",
	},

	// Every 64 bit Raspberry Pi, on Raspberry Pi's own kernel.
	"rpi": {
		arch:     "arm64",
		cross:    "aarch64-linux-gnu-",
		image:    "Image",
		dtbs:     true,
		piRadios: true,
	},
}

func lookup(name string) (target, error) {
	t, ok := targets[name]
	if !ok {
		return t, fmt.Errorf("no target %q: amd64, arm64 or rpi", name)
	}
	return t, nil
}
