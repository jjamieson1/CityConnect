# CIS Benchmarks

## Scope

Configuration baselines published by the Center for Internet Security, one per platform: the Linux distribution, Docker or Kubernetes, the database engine, the web server, and the cloud account itself. They cover the layer beneath the application — the part that application-level standards like ASVS assume someone else has handled.

## What it demands

Each benchmark defines two profiles. **Level 1** is safe, broadly applicable hardening that should not break a working system. **Level 2** is defence-in-depth for higher-sensitivity environments and will break things if applied without thought. Recurring themes: remove unused packages and services, restrict filesystem permissions, disable direct root login, enforce key-based SSH, configure a host firewall, enable and retain audit logging, set kernel network parameters, and constrain the container runtime — no privileged containers, no docker socket mounted into a container, resource limits set.

Cloud benchmarks (AWS, GCP, Azure) add identity hygiene: no long-lived root keys, MFA on privileged accounts, logging enabled in every region, storage buckets not public by default, encryption at rest.

## How it lands in this app

Pick the benchmark version matching the deployed distribution and runtime, decide Level 1 or 2 per host class, and encode the result in configuration management rather than a runbook — a benchmark applied by hand drifts within a quarter. Score with CIS-CAT or an equivalent scanner on a schedule, and record accepted deviations with a reason. The web tier deserves particular attention because it terminates TLS and serves the SPA: TLS version and cipher policy, security headers, directory listing off, and server version not advertised.

## Evidence to keep

Benchmark name and version per platform, the chosen profile level, the latest scan score with a trend, the deviation register, and the configuration-management code that enforces it.