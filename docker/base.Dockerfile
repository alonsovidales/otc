# syntax=docker/dockerfile:1.7
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The two images every release image (docker/Dockerfile) is built on, built
# as a pair:
#
#   build  the toolchain: OpenCV built from source, Go, protoc and ONNX
#          Runtime's headers and library
#   base   what the device runs on: MariaDB, ffmpeg, tini and the OpenCV and
#          ONNX Runtime libraries the build links against
#
#   docker buildx build -f docker/base.Dockerfile --target build -t otc-build --load docker
#   docker buildx build -f docker/base.Dockerfile --target base -t otc-base --load docker
#
# They change when the pins below do, and are rebuilt now and then for
# Debian's security fixes. Every version and hash is scripts/install.sh's:
# change them there and here together. Nothing from the checkout is copied
# in, so any directory will do as the build context.

FROM debian:trixie-slim AS build
ARG DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends build-essential pkg-config cmake libjpeg-dev libpng-dev \
        ca-certificates curl unzip \
 && rm -rf /var/lib/apt/lists/*

# fetch_pinned's rule (install.sh): nothing downloaded here is used unless
# it matches its pinned hash.
RUN cat > /usr/local/bin/fetch-pinned <<'EOF'
#!/bin/sh
set -eu
curl -fL --retry 5 --retry-delay 2 --connect-timeout 30 --speed-limit 1 --speed-time 120 -o "$2.part" "$1"
if [ "$(sha256sum "$2.part" | cut -d' ' -f1)" != "$3" ]; then
    echo "$1 does not match its pinned hash" >&2
    exit 1
fi
mv "$2.part" "$2"
EOF
RUN chmod 0755 /usr/local/bin/fetch-pinned

# OpenCV 4.10.0 with install.sh's recipe: only the modules gocv compiles.
# Debian 13's own packages are the same version, but their libraries bring
# GDAL, Qt and GStreamer along.
ARG OPENCV_SRC_VERSION=4.10.0
ARG OPENCV_SRC_SHA=b2171af5be6b26f7a06b1229948bbb2bdaa74fcf5cd097e0af6378fce50a6eb9
RUN set -eu; \
    d=/var/tmp/otc-opencv; mkdir -p "$d"; log="$d/build.log"; \
    fetch-pinned "https://github.com/opencv/opencv/archive/refs/tags/$OPENCV_SRC_VERSION.tar.gz" "$d/src.tar.gz" "$OPENCV_SRC_SHA"; \
    tar -xzf "$d/src.tar.gz" -C "$d"; \
    jobs=$(nproc); mem=$(awk '/^MemAvailable:/ {print int($2 / 1048576)}' /proc/meminfo); \
    [ "$mem" -lt "$jobs" ] && jobs=$mem; [ "$jobs" -ge 1 ] || jobs=1; \
    { cmake -S "$d/opencv-$OPENCV_SRC_VERSION" -B "$d/build" \
        -D CMAKE_BUILD_TYPE=Release -D CMAKE_INSTALL_PREFIX=/usr/local \
        -D BUILD_LIST=core,imgproc,imgcodecs,videoio,highgui,objdetect,dnn,features2d,calib3d,photo,video \
        -D OPENCV_GENERATE_PKGCONFIG=ON -D BUILD_SHARED_LIBS=ON \
        -D BUILD_TESTS=OFF -D BUILD_PERF_TESTS=OFF -D BUILD_EXAMPLES=OFF -D BUILD_DOCS=OFF -D BUILD_opencv_apps=OFF \
        -D WITH_IPP=OFF -D WITH_KLEIDICV=OFF -D WITH_ADE=OFF -D WITH_GTK=OFF -D WITH_QT=OFF \
        -D WITH_FFMPEG=OFF -D WITH_GSTREAMER=OFF -D WITH_V4L=OFF -D WITH_1394=OFF -D WITH_VTK=OFF \
        -D WITH_TIFF=OFF -D WITH_WEBP=OFF -D WITH_OPENJPEG=OFF -D WITH_JASPER=OFF -D WITH_OPENEXR=OFF \
        -D WITH_AVIF=OFF -D WITH_GDAL=OFF -D WITH_GDCM=OFF \
      && cmake --build "$d/build" -j "$jobs" \
      && cmake --install "$d/build"; } >"$log" 2>&1 \
      || { tail -n 30 "$log" >&2; exit 1; }; \
    ldconfig; \
    rm -rf "$d"; \
    pkg-config --modversion opencv4; \
    mkdir -p /opt/opencv-runtime; \
    cp -a /usr/local/lib/libopencv_*.so.* /opt/opencv-runtime/

# Go, ONNX Runtime and protoc, for the architecture being built.
ARG GO_VERSION=1.26.1
ARG ONNXRUNTIME_VERSION=1.24.3
ARG PROTOC_VERSION=29.3
RUN set -eu; \
    case "$(uname -m)" in \
        aarch64) ARCH=arm64; ORT_ARCH=aarch64; PROTOC_ARCH=aarch_64; \
                 GO_SHA=a290581cfe4fe28ddd737dde3095f3dbeb7f2e4065cab4eae44dfc53b760c2f7; \
                 ORT_SHA=15100fb88b4c692cdd6bf2cca5f4a26a3806cebca8136de6681e2aba4b2ea033; \
                 PROTOC_SHA=6427349140e01f06e049e707a58709a4f221ae73ab9a0425bc4a00c8d0e1ab32 ;; \
        x86_64)  ARCH=amd64; ORT_ARCH=x64; PROTOC_ARCH=x86_64; \
                 GO_SHA=031f088e5d955bab8657ede27ad4e3bc5b7c1ba281f05f245bcc304f327c987a; \
                 ORT_SHA=4c436a280d650f8bf32c921a2bf4de7c42cc32884c51c90e47de991708bbb5a4; \
                 PROTOC_SHA=3e866620c5be27664f3d2fa2d656b5f3e09b5152b42f1bedbf427b333e90021a ;; \
        *) echo "unsupported architecture $(uname -m) (need arm64 or amd64)" >&2; exit 1 ;; \
    esac; \
    t=$(mktemp -d); \
    fetch-pinned "https://go.dev/dl/go$GO_VERSION.linux-$ARCH.tar.gz" "$t/go.tar.gz" "$GO_SHA"; \
    tar -xzf "$t/go.tar.gz" -C /usr/local; \
    fetch-pinned "https://github.com/microsoft/onnxruntime/releases/download/v$ONNXRUNTIME_VERSION/onnxruntime-linux-$ORT_ARCH-$ONNXRUNTIME_VERSION.tgz" "$t/ort.tgz" "$ORT_SHA"; \
    tar -xzf "$t/ort.tgz" -C "$t"; \
    mv "$t/onnxruntime-linux-$ORT_ARCH-$ONNXRUNTIME_VERSION" /opt/onnxruntime; \
    fetch-pinned "https://github.com/protocolbuffers/protobuf/releases/download/v$PROTOC_VERSION/protoc-$PROTOC_VERSION-linux-$PROTOC_ARCH.zip" "$t/protoc.zip" "$PROTOC_SHA"; \
    mkdir -p /opt/protoc; \
    unzip -q "$t/protoc.zip" -d /opt/protoc; \
    ln -s /opt/protoc/bin/protoc /usr/local/bin/protoc; \
    rm -rf "$t"
ENV PATH=/usr/local/go/bin:$PATH

FROM debian:trixie-slim AS base
ARG DEBIAN_FRONTEND=noninteractive
# Fixed ids, made before MariaDB's package would pick one: the files on the
# storage folder belong to these numbers, and a rebuilt image must not hand
# them to someone else.
ARG OTC_UID=7310
ARG MYSQL_UID=7311
# The package's own datadir in /var/lib/mysql is left empty: the database
# lives on the storage volume, made there at the first start.
RUN groupadd -g "$OTC_UID" otc \
 && useradd -r -u "$OTC_UID" -g otc -d /var/lib/otc -s /usr/sbin/nologin otc \
 && groupadd -g "$MYSQL_UID" mysql \
 && useradd -r -u "$MYSQL_UID" -g mysql -d /nonexistent -s /usr/sbin/nologin mysql \
 && apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg mariadb-server mariadb-client tini \
        ca-certificates curl openssl iproute2 libjpeg62-turbo libpng16-16t64 \
 && rm -rf /var/lib/apt/lists/* /var/lib/mysql/* \
 && [ "$(id -u otc)" = "$OTC_UID" ] && [ "$(id -u mysql)" = "$MYSQL_UID" ] && [ "$(id -g mysql)" = "$MYSQL_UID" ] \
 && for c in mysql mysqladmin mariadb-admin mariadb-dump mariadb-install-db mariadb-upgrade mariadbd \
             ffmpeg ffprobe tini setpriv mountpoint ss curl openssl; do \
        command -v "$c" >/dev/null || { echo "missing $c" >&2; exit 1; }; \
    done
COPY --from=build /opt/opencv-runtime/ /usr/local/lib/
COPY --from=build /opt/onnxruntime/lib/ /opt/onnxruntime/lib/
RUN ldconfig
