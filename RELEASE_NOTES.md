## What's Changed

Full OpenAI Codex CLI support across all models: multi-agent namespaces, custom programmatic tools (`exec`), tool call results, and reasoning efforts are fully functional.

### Features

- Register as a native CLIProxyAPI quota provider: on hosts `v7.2.159+` (including v8), OpenCode Go credentials report `supports_quota` in `/v0/management/auth-files` and serve rolling, weekly, and monthly windows through `POST /v0/management/quota/fetch`. The plugin keeps schema version 3, so older v7 hosts still load it and keep using the separate `OpenCode Go Quota` page. Quota lookups are now capped at 30 seconds.

### Bug Fixes

- Fix missing Responses stream item lifecycle completion events (output_text.done, content_part.done, function_call_arguments.done, output_item.done) prior to response.completed, resolving dropped assistant output and tool calls in OpenAI Codex CLI and strict Responses clients.
- Fix handling of in-history messages with role: "system" across protocol adapters without rejecting them as unsupported roles or forwarding invalid turn roles to upstream providers that require alternating user/assistant turns.
- Fix handling of multi-part content arrays in function_call_output.output under /v1/responses decoding.
- Fix upstream HTTP >= 400 error propagation in streaming and non-streaming requests by reading and extracting the upstream error payload instead of discarding it with generic fallback errors.
- Add two-way Responses tool namespace and additional_tools translation: merge dynamic declarations from input items (type: "additional_tools"), unroll client-side grouping tools (type: "namespace") into qualified wire names for upstream models, and restore original tool names and namespaces across non-streaming output and streaming SSE events for OpenAI Codex CLI and multi-agent harnesses.
- Add Codex custom tool normalization and hosted tool dropping: rewrite generic custom tools (`type: "custom"`, e.g. `exec`) into function tools with default object schemas, and drop client-only/hosted tools (`apply_patch`, `web_search`, `web_search_preview`, `tool_search`, `image_generation`) when routing to Chat Completions or Messages endpoints, resolving Codex CLI failures on models like `space-bunny-free`.
- Add bidirectional support for `custom_tool_call` and `custom_tool_call_output` conversation items and outbound custom tool events: translate inbound custom tool calls and results across Chat Completions and Messages protocols, unwrap arguments into clean raw input, and restore `custom_tool_call` and `response.custom_tool_call_input.done` on outbound non-streaming and streaming responses, resolving Codex tool dispatch aborts on programmatic tools like `exec`.
- Remove local reasoning effort validation across request translators: allow client-declared reasoning effort levels (e.g. `xhigh`, `max`, and unlisted levels) to pass through transparently to upstream endpoints without local gatekeeping in `chatcompletions`, `responses`, and `messages` adapters.
- Normalize tools for non-GPT native Responses models: unroll namespaces, convert custom tools (`exec`) into function tools, and sanitize `web_search` definitions when targeting non-GPT Responses endpoints (e.g. `muse-spark`, `grok`), while preserving transparent passthrough for native GPT models (e.g. `gpt-6-luna`).
- Restore custom tool calls across native Responses-to-Responses streaming and non-streaming adapters, ensure historical function_call arguments default to valid JSON via `shared.DefaultArgs` to resolve Muse Spark errors, drop compaction input items to fix Grok compaction blob decode failures, and expand `UnwrapCustomToolInput` to unwrap arguments, code, cmd, and command fields.
- Drop historical `reasoning` input items for non-GPT Responses upstreams: resolve Grok HTTP 400 "Could not decode the compaction blob" decryption errors on multi-turn conversations caused by pooled account credential mismatches on encrypted reasoning blobs.


## Upgrade Notes

- Replace the old plugin binary with the new release binary.
- Restart CLIProxyAPI after replacing the plugin.
- Hard-refresh Management Center if the plugin page looks stale.

**Full Changelog**: https://github.com/massiveits/opencode-go-cliproxyapi/compare/v0.1.9...v0.1.10