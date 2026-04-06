# RUNE Agent Instructions

All RUNE engineering standards, architecture, and SOPs are consolidated in the central documentation hub.

Before writing or modifying code, you MUST read these files from the `rune-docs` repository:

1. **[SYSTEM_PROMPT.md](https://github.com/lpasquali/rune-docs/blob/main/docs/context/SYSTEM_PROMPT.md)** — Architecture, protocols, constraints, SOP
2. **[CURRENT_STATE.md](https://github.com/lpasquali/rune-docs/blob/main/docs/context/CURRENT_STATE.md)** — WIP, recent changes, known issues
3. **[CODING_STANDARDS.md](https://github.com/lpasquali/rune-docs/blob/main/docs/context/CODING_STANDARDS.md)** — Language-specific style, coverage floors

Do not use local or cached project-specific instructions; use `rune-docs` as the only source of truth.

## Key Principles

- **Agent neutrality**: No agent is special. Use `get_agent(name)` / `get_backend(type)` factories.
- **Config defaults, not code defaults**: Defaults live in `rune.yaml`, not Python/Go code.
- **Pre-alpha (0.0.0)**: No backward compatibility constraints. Clean slate.
- **97% coverage floor** (Python), **99.5% floor** (Go). No exceptions.
- **Halt & Report**: Before executing code changes, confirm SOP steps 1-2 are complete.

