# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The image and the two it is built on, in one build (docker buildx bake),
# as .github/workflows/docker.yml makes it for every release:
#
#   otc-build  the toolchain (docker/base.Dockerfile, target build)
#   otc-base   what the device runs on (docker/base.Dockerfile, target base)
#   otc        the release image (docker/Dockerfile), on those two
#   smoke      otc-smoke (docker/smoke), the sign-in test the image passes
#              before it is published - never part of the image
#
# From a release's own source and its signed web app, never a working copy:
#
#   SRC=<release src> WEB=<unpacked web-dist.tar.gz> OTC_RELEASE=<N> \
#       docker buildx bake -f docker/docker-bake.hcl otc --set otc.output=type=docker,name=otc:<N>
#
# (with --allow fs.read=<dir> for SRC and WEB outside the current directory).
#
# smoke's context is a directory holding only go.mod, go.sum,
# proto/messages.proto and docker/smoke (SMOKE_SRC); its output is the
# binary alone.

variable "SRC" { default = "." }
variable "WEB" { default = "web-dist" }
variable "SMOKE_SRC" { default = "." }
variable "OTC_RELEASE" { default = "" }
variable "OTC_REVISION" { default = "" }

target "otc-build" {
  context    = "${SRC}/docker"
  dockerfile = "base.Dockerfile"
  target     = "build"
}

target "otc-base" {
  context    = "${SRC}/docker"
  dockerfile = "base.Dockerfile"
  target     = "base"
}

target "otc" {
  context    = SRC
  dockerfile = "docker/Dockerfile"
  contexts = {
    otc-build = "target:otc-build"
    otc-base  = "target:otc-base"
    web       = WEB
  }
  args = {
    BUILD_IMAGE  = "otc-build"
    BASE_IMAGE   = "otc-base"
    OTC_RELEASE  = OTC_RELEASE
    OTC_REVISION = OTC_REVISION
  }
}

target "smoke" {
  context  = SMOKE_SRC
  contexts = { otc-build = "target:otc-build" }
  dockerfile-inline = <<EOT
FROM otc-build AS smoke
WORKDIR /src
COPY go.mod go.sum ./
RUN GOBIN=/usr/local/bin go install "google.golang.org/protobuf/cmd/protoc-gen-go@$(awk '/google.golang.org\/protobuf /{print $2}' go.mod)"
COPY proto/messages.proto proto/
COPY docker/smoke docker/smoke
RUN mkdir -p proto/generated \
 && protoc -I=proto --go_out=proto/generated --go_opt=paths=source_relative proto/messages.proto \
 && CGO_ENABLED=0 go build -o /out/otc-smoke ./docker/smoke
FROM scratch
COPY --from=smoke /out/otc-smoke /
EOT
}
