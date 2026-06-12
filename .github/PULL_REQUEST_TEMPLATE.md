<!-- Thanks for contributing! Keep PRs focused and small where possible. -->

## What & why

<!-- What does this change and why? Link any related issue (e.g. Closes #123). -->

## Checklist

- [ ] `make test` passes (`go test -race ./...`)
- [ ] `make vet` and `gofmt` are clean
- [ ] Added/updated tests for the change
- [ ] If a tool was added/removed, bumped `expectedToolCount` in
      `internal/mcpserver/integration_test.go` and updated the README tool list
- [ ] If config/flags changed, updated the README, `.env.example`, and the Helm chart
- [ ] Updated `CHANGELOG.md` under "Unreleased"
