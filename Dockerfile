ARG SOURCE=linux

# Build Image
FROM debian:trixie-20261104-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS toolchain
RUN sed -i -e '/^URIs:/d' -e 's|^# \(http://snapshot\.debian\.org/.*\)$|URIs: \1|' \
    /etc/apt/sources.list.d/debian.sources \
    && grep -q '^URIs: http://snapshot.debian.org/' /etc/apt/sources.list.d/debian.sources \
    && apt-get -o Acquire::Check-Valid-Until=false update \
    && apt-get install -y --no-install-recommends \
    bc bison build-essential flex gawk gcc-aarch64-linux-gnu kmod \
    libelf-dev libssl-dev python3 xz-utils zstd \
    && rm -rf /var/lib/apt/lists/*

# Build Tool
FROM golang:1.27.4-trixie@sha256:f18c3d73a184fb0582828510722e723b63a93f7188d11dd223d9be6f6880e65d AS tool
WORKDIR /tool
COPY tool/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags=-buildid= -o /kernel-tool .

# Sources
FROM scratch AS linux
ADD --checksum=sha256:020d45510c539972e6f14c5c71566878d3b7ef56fb082b3e09134f2ebaade562 \
    https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.18.64.tar.xz /linux-6.18.64.tar.xz

FROM scratch AS linux-rpi
ADD --checksum=sha256:53a3967359cf1b68953bbfc5aef5d3c7fcf9ebf61518fbba108c6a8bfce0c079 \
    https://github.com/raspberrypi/linux/archive/refs/tags/stable_20261103.tar.gz /linux-raspberrypi-stable_20261103.tar.gz

FROM scratch AS sources
COPY --from=linux / /
COPY --from=linux-rpi / /

FROM scratch AS firmware
ADD --checksum=sha256:a10d1ab5baf2c74ba92e8c7cf313e691696c26bddfa6df00db795e48d70cd4ca \
    https://cdn.kernel.org/pub/linux/kernel/firmware/linux-firmware-20261019.tar.xz /linux-firmware.tar.xz
ADD --checksum=sha256:b22e0901227b820cd1c280abe681a15b773a5103a5e10dc442e94ebb34cbf58d \
    https://cdn.kernel.org/pub/software/network/wireless-regdb/wireless-regdb-2026.09.03.tar.xz /wireless-regdb.tar.xz
ADD --checksum=sha256:989a5cd345808babee491da69ed80ae5741697bc87743a71d0ba611f3fd6afa5 \
    https://github.com/intel/Intel-Linux-Processor-Microcode-Data-Files/archive/microcode-20261110.tar.gz /intel-microcode.tar.gz
ADD --checksum=sha256:c973f51a9f56c2cd8510c82e1ded5349dd321f16cdcbab3ec027b28547b4634a \
    https://github.com/RPi-Distro/firmware-nonfree/archive/9794282eb9f4a2de1f23b41a738926740e975d83.tar.gz /rpi-firmware-nonfree.tar.gz
ADD --checksum=sha256:56bcee9bac20720ceeef983949ba4d6b8d81c2f9602613232e642de547240841 \
    https://github.com/RPi-Distro/bluez-firmware/archive/78d6a07730e2d20c035899521ab67726dc028e1c.tar.gz /rpi-bluez-firmware.tar.gz

FROM ${SOURCE} AS source

# Compile the Kernel
FROM toolchain AS kernel
ARG TARGET
ARG SOURCE_DATE_EPOCH=0
COPY --from=tool /kernel-tool /usr/local/bin/

COPY config/common.config config/${TARGET}.config \
    config/common.drivers config/${TARGET}.drivers /config/
RUN --mount=type=bind,from=source,target=/sources \
    kernel-tool kernel -target "${TARGET}"

ARG VERSION
ARG COMMIT=unknown
COPY config/common.firmware config/${TARGET}.firmware* /config/
RUN --mount=type=bind,from=firmware,target=/sources \
    kernel-tool package -target "${TARGET}" -version "${VERSION}" -commit "${COMMIT}"

FROM scratch AS release
COPY --from=kernel /release/ /
