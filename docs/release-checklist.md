# Release Checklist

## Automated Evidence

- `go test ./...`
- `VERSION=<tag> make release-artifacts`
- `./scripts/install-smoke.sh dist/microhook_<tag>_linux_amd64.tar.gz`
- `./scripts/verify-systemd.sh`
- `docker build -t microhook:<tag> .`

## Automated Coverage Map

- Required endpoints: `TestServerHealthz`, `TestInvokeActionSyncReturnsCompletedRun`, `TestInvokeActionAsyncReturnsAcceptedRunAndSupportsLookup`, `TestListRunsSupportsActionAndStatusFilters`
- Auth and action scoping: `TestProtectedRoutesRequireAuth`, `TestActionRoutesEnforceScopedAuthorization`, `TestAuthenticateMiddlewareStoresIdentityInRequestContext`
- Timeout handling: `TestServiceInvokeMarksTimedOutRuns`
- Concurrency behavior: `TestInvokeActionBadRequestConflictAndNotFound`, `TestServiceInvokeRejectReturnsConflictWhileRunInFlight`
- Restart durability: `TestServiceRecoverCancelsInterruptedRunningRuns`, `TestBootstrapCancelsInterruptedRunsOnStartup`
- Invalid config failure: `TestLoadRejectsInvalidConfig`, `TestLoadRejectsMalformedSections`, `TestValidateConfigRejectsInvalidConfig`
- Logging and token redaction: `TestProtectedRoutesRequireAuth`, `TestServerLogsStructuredRequestEventsWithoutLeakingTokens`
- Minimal child environment: `TestServiceInvokeCommandModePassesInputAndMetadata`

## Manual Release Sign-Off

- [ ] Confirm the release version matches the tagged source.
- [ ] Confirm both Linux tarballs were generated and checksums were produced.
- [ ] Confirm the Docker image was built or published for the release version.
- [ ] Confirm install docs still match the packaged artifact layout.
- [ ] Confirm the clean-host smoke test passed on Linux.
- [ ] Confirm the packaged `systemd` unit verifies cleanly.

## Manual Security Review

- [ ] Auth: bearer tokens are required on all `/v1/...` routes and scoped tokens still restrict actions as expected.
- [ ] Process spawning: `command` mode remains the preferred path and no request-driven interpolation was introduced.
- [ ] Logging: request and error logs do not leak bearer tokens.
- [ ] Environment handling: child processes still receive only explicit env plus Microhook metadata variables.
- [ ] Shell mode: docs still clearly warn that `shell_command` is less safe than argv `command` mode.
- [ ] Deployment guidance: docs still recommend localhost binding by default and reverse proxying only when remote access is required.
