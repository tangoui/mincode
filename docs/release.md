# Desktop releases

GitHub Actions builds each target with cgo on a native runner. Each version
starts as a draft for maintainer review.

| Manifest platform | GitHub runner | Installers |
|---|---|---|
| `darwin-arm64` | `macos-15` | `.dmg` |
| `darwin-amd64` | `macos-15-intel` | `.dmg` |
| `windows-amd64` | `windows-latest` | NSIS `-setup.exe` |
| `windows-arm64` | `windows-11-arm` | NSIS `-setup.exe` |
| `linux-amd64` | `ubuntu-22.04` | `.tar.gz`, `.AppImage`, `.deb`, `.rpm` |
| `linux-arm64` | `ubuntu-24.04-arm` | `.tar.gz`, `.AppImage`, `.deb`, `.rpm` |

The GitHub release also contains `latest.json` when update signing is
configured, full update payloads (macOS `.app.tar.gz`, Windows app `.exe`,
and the Linux `.tar.gz`), and any generated `.patch` files.

## Configure update signing

Run `tan keygen -o mincode-update` once, outside the repository. Store the
contents of `mincode-update.key` in the repository Actions secret
`TANGO_UPDATE_KEY`. Keep a private backup of that file. The release workflow
derives the matching public key and embeds it in each build. Keep the same
key for subsequent releases and manifest renewals so installed copies can
verify them.

When the secret is absent, the workflow builds the installers without an
update manifest. Those builds have in-app updates disabled.

## Cut a release

1. Set the same version in `tan.json` and `backend/misc/misc.go`
   (`AppVersion`). Optionally add `docs/releases/<version>.md` for the GitHub
   release notes and the in-app update dialog.
2. Commit the version change, then create and push the matching `vX.Y.Z` tag.
3. Open the draft in GitHub Releases. Review the notes and files, then select
   **Publish release**. Clients find the published manifest at
   `/releases/latest/download/latest.json`.

The workflow generates full update payloads and uses the previous published
release's signed manifest and payloads to generate patches when they are
smaller. Linux and Windows ARM64 start with full payloads in 0.4.1 because
0.4.0 has no assets for those targets.

The Linux manifest uses the `dir` format and points to the tarball, so 0.4.0
tarball users can update in place from the extracted application directory.
AppImage, deb, and rpm installations show new versions in Settings and link
to GitHub Releases to download the matching package.

The **Renew update manifest** workflow refreshes `latest.json` each Monday.
Run it manually after publishing a draft that has been open for several
weeks. The signature expires after 30 days. GitHub must allow replacing
`latest.json` on the published release, so release immutability must be
disabled for this renewal process.

## Optional platform signing

- macOS uses the ad-hoc signature from Tango by default. To use a Developer
  ID certificate, set `APPLE_CERT_BASE64` to a base64-encoded PKCS#12 file,
  `APPLE_CERT_PASSWORD` to its password, and `APPLE_SIGN_IDENTITY` to its
  codesign identity. The workflow signs the app and disk image. For builds
  without Apple notarization, move MinCode to Applications and, after trying
  to open it, use **System Settings → Privacy & Security → Open Anyway** if
  macOS blocks it.
- Windows installers use Authenticode when `WINDOWS_CERT_BASE64` contains a
  base64-encoded PFX certificate. Set `WINDOWS_CERT_PASSWORD` to its
  password.

## Package locally

Install the Go version from `go.mod`, Node.js 26, the pnpm version from
`package.json`, and the `tan` CLI at the project's Tango revision.

```sh
go install "github.com/tangoui/tango/cmd/tan@$(go list -m -f '{{.Version}}' github.com/tangoui/tango)"
pnpm install --frozen-lockfile
pnpm package:desktop
```

Ensure the Go bin directory is on `PATH`. Tango reads the build tags directly
from `tan.json` and writes the packages to `dist/`. On Windows and Linux, use
`pnpm package:desktop -features all`, which selects the backend profile
available on those hosts. The release matrix uses the same platform-specific
settings. On Linux, use `pnpm package:desktop -features all -format tar,appimage,deb,rpm`
with `appimagetool` on `PATH` to build all four formats. The deb and rpm
include Tango's GTK 3 and WebKitGTK 4.1 runtime dependencies.
