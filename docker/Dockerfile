# syntax=docker/dockerfile:1.7
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Off The Cloud as a container image (beta), for Linux home servers and NAS
# boxes. On a Raspberry Pi, use the Off The Cloud image instead: it builds
# and watches the disk mirror.
#
# Built from a release's own source and its signed web app - never from a
# working copy, so nothing untracked gets in:
#
#   git archive v<N> | tar -x -C <src>      (or the release's src.tar.gz)
#   tar -xzf web-dist.tar.gz -C <web>       (checked against the release manifest)
#   docker buildx build -f <src>/docker/Dockerfile --build-context web=<web> \
#       --build-arg OTC_RELEASE=<N> --build-arg OTC_REVISION=<commit> -t otc:<N> --load <src>
#
# BUILD_IMAGE and BASE_IMAGE are the pair docker/base.Dockerfile makes,
# under the local tags its own commands give unless named here.
ARG BUILD_IMAGE=otc-build:local
ARG BASE_IMAGE=otc-base:local

# Replaced by --build-context web=<dir>; without it the build stops here.
FROM ${BUILD_IMAGE} AS web
RUN echo "the web app is missing: pass --build-context web=<the release's web-dist.tar.gz, unpacked>" >&2; exit 1

FROM ${BUILD_IMAGE} AS build
WORKDIR /src
# protoc-gen-go at go.mod's protobuf version, as install.sh does.
COPY go.mod go.sum ./
RUN go mod download \
 && GOBIN=/usr/local/bin go install "google.golang.org/protobuf/cmd/protoc-gen-go@$(awk '/google.golang.org\/protobuf /{print $2}' go.mod)"
COPY . .
# The binary as install.sh builds it, and install.sh's own schema block,
# taken out between its two marks so the container migrates its database
# with exactly what a re-installed Raspberry Pi runs. Without the closing
# mark sed would take the rest of install.sh, which root then sources at
# every upgrade: the build stops instead.
RUN mkdir -p proto/generated \
 && protoc -I=proto --go_out=proto/generated --go_opt=paths=source_relative proto/messages.proto \
 && CGO_ENABLED=1 CGO_CFLAGS="-I/opt/onnxruntime/include" CGO_LDFLAGS="-L/opt/onnxruntime/lib -lonnxruntime" \
    go build -o /out/otc ./bin/otc.go \
 && sed -n '/^# >>> schema-migrations/,/^# <<< schema-migrations/p' scripts/install.sh > /out/schema-migrations.sh \
 && grep -q '^apply_schema_migrations otc$' /out/schema-migrations.sh \
 && [ "$(tail -n 1 /out/schema-migrations.sh)" = '# <<< schema-migrations' ]

FROM ${BASE_IMAGE}
ARG OTC_RELEASE
ARG OTC_REVISION=
COPY --from=build /out/otc /usr/bin/otc
COPY --from=build /out/schema-migrations.sh /usr/share/otc/
COPY --from=web / /var/www/
COPY db/db.sql models/models/tag_list_4585.txt.gz scripts/release-signing.pub /usr/share/otc/
COPY docker/mariadb.cnf /etc/mysql/mariadb.conf.d/60-otc.cnf
COPY --chmod=0755 scripts/bridge-runner/otc-bridge-runner.sh /usr/local/bin/otc-bridge-runner
COPY --chmod=0755 docker/reboot-runner-stub.sh /usr/local/bin/otc-reboot-runner
COPY --chmod=0755 docker/entrypoint.sh /usr/local/bin/otc-entrypoint
# The release this image is (the version check and the migration marker
# read it); the app's logs and the update log live on the volumes.
RUN case "$OTC_RELEASE" in ''|*[!0-9]*) echo "OTC_RELEASE must be the release number" >&2; exit 1 ;; esac \
 && echo "$OTC_RELEASE" > /usr/share/otc/release \
 && ln -s /var/lib/otc/log /var/log/otc \
 && ln -s /etc/otc/update-log /var/log/otc-update \
 && ! ldd /usr/bin/otc | grep 'not found'
ENV OTC_CONTAINER=1
LABEL org.opencontainers.image.title="Off The Cloud (beta)" \
      org.opencontainers.image.source=https://github.com/alonsovidales/otc \
      org.opencontainers.image.licenses=AGPL-3.0-or-later \
      org.opencontainers.image.version=$OTC_RELEASE \
      org.opencontainers.image.revision=$OTC_REVISION
# The first start downloads the models (about 880 MB); the first healthy
# answer ends the start period early.
HEALTHCHECK --start-period=30m --interval=30s --timeout=10s \
    CMD curl -fsS "http://127.0.0.1:${OTC_HTTP_PORT:-8080}/check_healty" >/dev/null && mariadb-admin ping >/dev/null || exit 1
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/otc-entrypoint"]
CMD ["run"]
