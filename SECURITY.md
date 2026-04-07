# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest  | Yes       |

## Reporting a Vulnerability

**Do not open a public issue for security vulnerabilities.**

Please use [GitHub's private vulnerability reporting](https://github.com/djlord-it/cronlite/security/advisories/new) to submit your report. Include:

- Description of the vulnerability
- Steps to reproduce
- Impact assessment (what an attacker could do)
- Suggested fix (if you have one)

Reports go directly to the maintainers — only they can see it. You should receive an acknowledgment within 48 hours.

## Scope

The following are in scope:

- Authentication and authorization bypass
- SQL injection
- SSRF via webhook URLs
- HMAC signature bypass
- Credential exposure in logs or API responses
- Namespace isolation bypass

## Disclosure Policy

- We will acknowledge receipt within 48 hours
- We will confirm the vulnerability and determine its impact within 7 days
- We will release a fix and credit the reporter (unless anonymity is requested)
- We ask that you do not publicly disclose the issue until a fix is available
