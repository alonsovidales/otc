# Contributing to Off The Cloud

Thanks for considering contributing. This document covers the practical basics; see
[README.md](README.md) for architecture and build/deploy instructions.

## License and SPDX headers

OTC is licensed under the [GNU Affero General Public License v3.0](LICENSE)
(`AGPL-3.0-or-later`). By submitting a contribution, you agree to license it under the same
terms.

Every source file (`.go`, `.ts`, `.tsx`, `.swift`) starts with:

```
// SPDX-License-Identifier: AGPL-3.0-or-later
```

as its first line (followed by a blank line, so it doesn't get absorbed into a package/file doc
comment that immediately follows). Generated files (`proto/generated/`, `web/src/proto/`,
`*.pb.swift`) are exempt - they're produced by `make pb` from `proto/messages.proto`, not
hand-authored. New source files should carry the header from the start.

## Signing the CLA

Every pull request needs the [Contributor License Agreement](CLA.md) signed once per contributor.
A bot ([CLA Assistant](.github/workflows/cla.yml)) checks this automatically on each PR and will
comment with instructions if you haven't signed yet - it comes down to leaving a comment on your
own PR that says:

```
I have read the CLA Document and I hereby sign the CLA
```

You keep your copyright on anything you contribute. The short version of what you're agreeing to:
your contribution is licensed to the project under AGPL-3.0-or-later like everything else, *and*
you also grant the maintainer (Alonso Vidales) the right to offer the project under other license
terms too - e.g. a commercial license sold to companies that don't want the AGPL's obligations,
which is a common way self-hosted open source projects fund themselves (see e.g. MongoDB, Grafana,
n8n, Nextcloud). Without that grant, doing so later would need to track down and re-ask every past
contributor individually, which usually just doesn't happen. See [CLA.md](CLA.md) for the full
text.

## Getting started

See [README.md](README.md) for:
- Project architecture (device, bridge, web, iOS/macOS/Windows clients)
- Build and deploy commands (`make all`, `make web`, `make pb`, etc.)
- Toolchain requirements (Go version, CGO/ONNX Runtime for the device binary)
- Running tests (`go test ./cfg/... ./log/...` etc.)

## Pull requests

- Keep PRs focused - one issue/feature per PR is easier to review than a bundle of unrelated
  changes.
- Regenerate protobuf bindings with `make pb` after editing `proto/messages.proto`; don't
  hand-edit generated files.
- Match the existing code style in whichever package/app you're touching rather than introducing
  a new convention.
