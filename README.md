# kernel builds for Fostrom OS

This repo builds the Linux Kernel for Fostrom OS.


| Target  | Kernel                                    |
|---------|-------------------------------------------|
| `amd64` | Linux 6.18.64                             |
| `arm64` | Linux 6.18.64                             |
| `rpi`   | Raspberry Pi's 6.18.60, `stable_20261103` |


## Building

Requirements: Docker and [just](https://just.systems)

```bash
# build all targets:
just build

# build one target:
just build <amd64 | arm64 | rpi>
```

Outputs are in `.release/`


## License

Apache 2.0. The kernel, the firmware and the microcode in a release are under
their own licenses.
