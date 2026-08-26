# Changelog

> Generated from `git` by `scripts/gen-changelog.sh` — do not edit by hand.

- **v1.8.0** (2026-08-26) · `feat` — Google's discovery document becomes a dialect: they publish one for over three hundred of their APIs at a predictable address and no OpenAPI parser reads it, so writing this reader once makes Drive, Sheets, Calendar, Gmail and the rest reachable without a line of code per API
- **v1.7.0** (2026-08-26) · `feat` — responses can hand the model a file path instead of a megabyte of base64: an inlined image arrives as 1.17 MB in a single field, which is a third of a million tokens the model can neither look at nor save, so --blob-dir writes those bytes to disk and leaves the shape of the answer intact
- **v1.6.0** (2026-08-26) · `feat` — the server now tells the client what it is for, taken from the spec's own info: under tool search a client loads only tool names and the server instructions up front, so a server that says nothing about itself is one the model never searches
- **v1.5.0** (2026-08-23) · `refactor` — releases move to CI: pushing a tag now builds and publishes, and push.sh only ships commits and tag unless --full is given — Actions is free on public repositories, so the published binaries stop depending on whichever toolchain this workstation happens to have
- **v1.4.0** (2026-08-22) · `feat` — sign gains method, base64 and iso8601 timestamps: a scheme that signs the verb, encodes base64 and stamps an ISO instant had no way to be expressed — and every one of those mismatches fails as an authentication error that never names the format
- **v1.3.1** (2026-08-19) · `fix` — property names the MCP client refuses are aliased: it validates arguments against ^[a-zA-Z0-9_.-]{1,64}$ and rejects the whole CALL when one fails — PHP-style filters[offer_id] broke 7 of 9 tools on a real API
- **v1.3.0** (2026-08-19) · `feat` — per-request signature auth: APIs that sign every call over its own content — sha256 or hmac-sha256, with the payload and destination as templates
- **v1.2.0** (2026-08-19) · `feat` — secrets can come from the environment with env:NAME — a value passed as a flag is readable in ps and /proc/<pid>/cmdline by every process on the machine
- **v1.1.1** (2026-08-19) · `fix` — push.sh checks WRITE access, not just read: a public repo reads fine from any account, which is how the commits went up and the Release did not
- **v1.1.0** (2026-08-19) · `feat` — push.sh builds the release binaries and attaches them: five targets with checksums, built before anything is pushed
- **v1.0.1** (2026-08-19) · `chore` — root shortcuts for commit.sh and push.sh
- **v1.0.0** (2026-08-19) · `major` — serve any API as an MCP server from its specification: OpenAPI 3.x, Swagger 2.0 and GraphQL
