# Unified interaction regression

This harness runs actual Pi and Python Kimi processes with fresh temporary homes,
dummy credentials, a loopback model, and a loopback MCP server. It does not use a
personal provider account. The executable is a test harness, not another runtime
dependency.

Validated versions: Pi 0.84.1 and MoonshotAI/kimi-cli commit
`86f136422a0aae6b217ea49e7ea1d2e8a1defcd2`. Install those CLIs first, then run from
the acp-go repository:

```sh
go build -o /tmp/acp-unified-probe ./scripts/unified-integration
node scripts/unified-integration/run.mjs /tmp/acp-unified-probe /absolute/path/to/pi /absolute/path/to/kimi
```

The matrix verifies all three permission levels, selected-model review, accepted
answers reaching the next model request, rejected file/MCP operations producing
no side effect, malformed review falling back to human approval, and safe review
metadata arriving through ACP. Artifacts are written to a fresh directory printed
at startup. `ACP_TEST_LONG_QUESTIONS=1` delays every answer for 61 seconds; use
`ACP_TEST_ONLY_CLI=pi` or `kimi` and `ACP_TEST_ONLY_PERMISSION=default` to run one
long-wait case.

```sh
node scripts/unified-integration/cursor-handshake.mjs /tmp/acp-unified-probe /absolute/path/to/cursor-agent
```

The Cursor check validates the installed CLI's initialization under three launch
policies, with an isolated home and a loopback endpoint. It does **not** validate
provider authentication, a model turn, native tool execution, or UI rendering.
Those require separate environment acceptance. Codex and Claude remain covered
by their existing protocol and host tests; this fixture is not evidence of their
commercial-provider execution.
