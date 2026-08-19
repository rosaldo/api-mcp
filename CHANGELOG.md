# Changelog

> Generated from `git` by `scripts/gen-changelog.sh` — do not edit by hand.

- **v1.2.0** (2026-08-19) · `feat` — secrets can come from the environment with env:NAME — a value passed as a flag is readable in ps and /proc/<pid>/cmdline by every process on the machine
- **v1.1.1** (2026-08-19) · `fix` — push.sh checks WRITE access, not just read: a public repo reads fine from any account, which is how the commits went up and the Release did not
- **v1.1.0** (2026-08-19) · `feat` — push.sh builds the release binaries and attaches them: five targets with checksums, built before anything is pushed
- **v1.0.1** (2026-08-19) · `chore` — root shortcuts for commit.sh and push.sh
- **v1.0.0** (2026-08-19) · `major` — serve any API as an MCP server from its specification: OpenAPI 3.x, Swagger 2.0 and GraphQL
