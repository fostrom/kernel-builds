ARG SOURCE=linux

# Build Image
FROM debian:trixie-20260824-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132 AS toolchain
RUN sed -i -e '/^URIs:/d' -e 's|^# \(http://snapshot\.debian\.org/.*\)$|URIs: \1|' \
    /etc/apt/sources.list.d/debian.sources \
    && grep -q '^URIs: http://snapshot.debian.org/' /etc/apt/sources.list.d/debian.sources \
    && apt-get -o Acquire::Check-Valid-Until=false update \
    && apt-get install -y --no-install-recommends \
    bc bison build-essential flex gawk gcc-aarch64-linux-gnu kmod \
    libelf-dev libssl-dev python3 xz-utils zstd \
    && rm -rf /var/lib/apt/lists/*

# Build Tool
FROM golang:1.27.2-trixie@sha256:47667934874913a449b96faa7ef4777fa285d9ec29126ee80ed61c45b7cccf0e AS tool
WORKDIR /tool
COPY tool/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags=-buildid= -o /kernel-tool .

# Sources
FROM scratch AS linux
ADD --checksum=sha256:fbab86c9f471c81075b280cca30bd85d790c060063a1245859b6344b07c9c44e \
    https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.18.36.tar.xz /linux-6.18.36.tar.xz

FROM scratch AS linux-rpi
ADD --checksum=sha256:b148a879496ed0936843b3fbad76f91d445bd2d3140175f7d737813cb642ab69 \
    https://github.com/raspberrypi/linux/archive/refs/tags/stable_20260527.tar.gz /linux-raspberrypi-stable_20260527.tar.gz

FROM scratch AS sources
COPY --from=linux / /
COPY --from=linux-rpi / /

FROM scratch AS firmware
ADD --checksum=sha256:b7812ed6d59f6b09ecceddaa0be842a7e82a79cc0e46ca60478a4ebf02f1e178 \
    https://cdn.kernel.org/pub/linux/kernel/firmware/linux-firmware-20260410.tar.xz /linux-firmware.tar.xz
ADD --checksum=sha256:8a27bfc081bafed8c24dd70fab0d96f098e5a0bfcd08d3da672595f225ab8993 \
    https://cdn.kernel.org/pub/software/network/wireless-regdb/wireless-regdb-2026.05.30.tar.xz /wireless-regdb.tar.xz
ADD --checksum=sha256:7614616d7b2988c278060486f47de716ef44c19317928cb45ac9e288fafd5bd1 \
    https://github.com/intel/Intel-Linux-Processor-Microcode-Data-Files/archive/microcode-20260812.tar.gz /intel-microcode.tar.gz
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
