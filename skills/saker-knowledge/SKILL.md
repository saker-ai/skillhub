---
name: saker-knowledge
description: Use KnowHub domain knowledge through MCP before answering Saker architecture, product, operations, security, or agent-skill questions.
allowed-tools: knowledge.list_domains, knowledge.search, knowledge.ask, knowledge.get_source, knowledge.list_providers, knowledge.validate_okf
keywords: [saker, knowhub, knowledge, architecture, operations, evidence, citations]
user-invocable: true
---

# Saker Knowledge

Use this skill whenever a user asks about Saker platform architecture, module responsibilities, deployment, operations, security policy, agent behavior, skills, internal APIs, troubleshooting, or product capability claims that should be grounded in Saker-maintained knowledge.

## Required Lookup

Before answering a Saker domain question, call:

```text
knowledge.search(domain="saker-architecture", query=<user question>, top_k=8)
```

Use another domain only when the question clearly belongs there or when `knowledge.list_domains` shows a better domain. Common domains:

- `saker-architecture`: modules, service boundaries, protocols, auth, WebHub, KnowHub, SkillHub, ChatHub, AIHub, StockHub.
- `saker-devops`: deployment, configuration, recovery, local stack operation.
- `security-compliance`: permissions, audit, tenant isolation, token handling.
- `ai-agent-skills`: skill authoring, agent runtime rules, evidence use.

If the first search returns weak or missing evidence, refine once with a narrower query or a more specific domain before answering.

Use `knowledge.ask` only when the caller explicitly wants a concise evidence-grounded answer facade from KnowHub. For implementation decisions, reviews, or multi-step reasoning, prefer `knowledge.search` first so you can inspect evidence directly.

## Evidence Rules

- Base the answer on KnowHub evidence, not memory.
- Cite the source for every factual claim that depends on retrieved knowledge.
- Prefer evidence with a higher `confidence`, newer `updated_at`, and a source that matches the requested module or domain.
- Use `knowledge.get_source(source_id=...)` when a search hit is ambiguous, truncated, or needs source-level context.
- Do not expose `tenant_id`, permission labels, provider credentials, internal JWTs, API keys, OpenViking keys, DashScope AccessKeys, or long-lived download URLs.
- Do not call PandaWiki, DashScope, OpenViking, vector stores, or any provider directly. KnowHub is the only knowledge boundary.

## Answer Format

Use concise grounded answers:

```text
<answer>

Sources:
- <title> (<source_id>, <updated_at>)
```

When citing multiple evidence chunks from one source, group them under the same source entry and mention the relevant section path when available.

If evidence is insufficient, say so directly:

```text
KnowHub does not have enough evidence to answer this reliably. The closest evidence shows ...
Missing evidence: ...
```

Do not fill gaps with speculation. If the user needs an implementation decision and evidence is incomplete, provide the decision as a recommendation and label it as an inference.

## OKF Validation

When asked to review or import knowledge content, call:

```text
knowledge.validate_okf(content=<markdown or bundle manifest text>)
```

Report validation errors first, then warnings. Treat missing `title`, `updated_at`, or `owner` front matter as blocking errors. Treat missing citation links as a quality warning unless the caller explicitly requires citation-complete content.

## Provider Health

When answers are unexpectedly empty, slow, or inconsistent, call `knowledge.list_providers` and report provider health only at status level:

- provider id
- type
- healthy/unhealthy
- latency
- last error

Never report provider secret configuration values.
