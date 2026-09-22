# Security policy

## Reporting a vulnerability

Please do not report security vulnerabilities through public GitHub issues,
discussions, or pull requests.

Report them to Docker at **<security@docker.com>**, following Docker's
[vulnerability disclosure process](https://www.docker.com/security/). A
report is most actionable when it includes the affected version or commit,
what an attacker gains, and the steps to reproduce it.

## Scope

This repository holds the kit descriptor grammar, its BuildKit frontend,
and the resolver and assembler libraries. Reports of particular interest:

- A kit descriptor that escapes its declared permissions — content or
  behavior reaching a sandbox that the descriptor never requested, or a
  capability grant wider than the one the permission surface reported.
- A published descriptor whose effective form differs from what a host
  validated and a user approved, including through argument expansion.
- A build that reads or writes outside the build context, or that
  publishes an artifact not derived from the declared inputs.

Runtime enforcement — the sandbox boundary, the egress proxy, credential
mediation — lives in the runtime that consumes these kits, not here. A
finding there should be reported through the same address, naming the
runtime.

## Further information

For how Docker reviews and responds to security reports generally, see
Docker's [vulnerability disclosure policy](https://www.docker.com/trust/vulnerability-disclosure-policy/).
