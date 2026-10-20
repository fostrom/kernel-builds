# kernel builds for Fostrom OS

This repo builds the Linux Kernel for Fostrom OS.


| Target  | Kernel                                    |
|---------|-------------------------------------------|
| `amd64` | Linux 6.18.36                             |
| `arm64` | Linux 6.18.36                             |
| `rpi`   | Raspberry Pi's 6.18.33, `stable_20260527` |


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
