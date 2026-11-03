"""llmrx-client — the llmRx edge client, Python edition.

A local OpenAI-compatible endpoint that forwards to a remote llmRx
server, with a response cache, an offline fallback chain, usage
accounting, a signed control plane and one-time-code enrollment.

Wire-compatible with the Go client (docs/P15-CLIENT.md); the deliberate
divergences are listed in docs/P18-PYTHON-CLIENT.md §5.
"""

__version__ = "0.1.0"

# Overridable at build time so `-version` can self-identify the same way
# the Go binary does (ldflags there, environment here).
BUILD_COMMIT = "none"
BUILD_DATE = "unknown"
