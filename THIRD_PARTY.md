# Third-party software

Bookwarden is licensed under the GPL-3.0 ([LICENSE](LICENSE)). The binary and
the container image include the following third-party software. Licence texts
are at the links; they are not copied here.

## Vendored

| Component | Version | Licence | Where |
|---|---|---|---|
| [htmx](https://htmx.org) | 2.0.11 | 0BSD | `internal/web/static/htmx.min.js`; `TestHtmxIsVendored` pins its SHA-256 |

## Go modules compiled into the binary

The direct requirements of `go.mod`:

| Module | Licence |
|---|---|
| [github.com/icholy/digest](https://github.com/icholy/digest) | MIT |
| [github.com/jackc/pgx/v5](https://github.com/jackc/pgx) | MIT |
| [github.com/jackc/puddle/v2](https://github.com/jackc/puddle) | MIT |
| [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto) | BSD-3-Clause |
| [golang.org/x/image](https://pkg.go.dev/golang.org/x/image) | BSD-3-Clause |
| [golang.org/x/sync](https://pkg.go.dev/golang.org/x/sync) | BSD-3-Clause |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | BSD-3-Clause |
| [golang.org/x/text](https://pkg.go.dev/golang.org/x/text) | BSD-3-Clause |

The indirect requirements `github.com/jackc/pgpassfile` and
`github.com/jackc/pgservicefile` are MIT, and the Go standard library is
BSD-3-Clause. `go.mod` and `go.sum` pin every version.

## In the container image

| Component | Licence | Source |
|---|---|---|
| Alpine Linux base image (`alpine:3.24.4`, pinned by digest in the `Dockerfile`) | per package | [aports, branch 3.24-stable](https://gitlab.alpinelinux.org/alpine/aports/-/tree/3.24-stable) has the build recipe and sources of every packaged binary. The branch, not the `v3.24.4` tag: `apk add` installs the current 3.24 packages at build time |
| [Tesseract OCR](https://github.com/tesseract-ocr/tesseract) (Alpine package `tesseract-ocr`) | Apache-2.0 | aports, as above |
| [Poppler](https://poppler.freedesktop.org) utilities (Alpine package `poppler-utils`) | GPL-2.0-or-later | aports, as above |
| [tessdata_fast](https://github.com/tesseract-ocr/tessdata_fast) language models | Apache-2.0 | commit `87416418657359cb625c412a48b6e1d6d41c29bd`, the `TESSDATA_COMMIT` the `Dockerfile` pins |
| CA certificates (Alpine package `ca-certificates`) | MPL-2.0 AND MIT | aports, as above |
