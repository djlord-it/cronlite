#!/usr/bin/env python3
"""EasyCron MCP Playground Agent — Claude edition.

Interactive agent powered by Claude that manages cron jobs through
EasyCron's MCP server.

Usage
-----
    # Terminal 1 — webhook sink (optional, to see deliveries land)
    python playground/receiver.py

    # Terminal 2 — the agent
    export ANTHROPIC_API_KEY="sk-ant-..."
    export EASYCRON_API_KEY="ec_..."
    python playground/agent.py

Built-in commands
-----------------
    /monitor          Set up 5 website-monitoring jobs
    /stress [N]       Bulk-create N jobs (default 10)
    /lifecycle        Full create -> pause -> resume -> trigger -> delete cycle
    /circuit          Circuit breaker demo: dead endpoint -> failures -> recovery
    /schedule-lab     Natural-language resolver torture test (10 edge cases)
    /audit            Execution history deep-dive: delivery status per job
    /timeline         Preview when each job fires next, sorted chronologically
    /cleanup          Delete every job in the namespace
    /stats            Summarise current jobs and executions
    /quit             Exit
"""

import asyncio
import json
import os
import sys
import time

import anthropic
from mcp.client.streamable_http import streamablehttp_client
from mcp import ClientSession


# ---------------------------------------------------------------------------
# System prompt
# ---------------------------------------------------------------------------
SYSTEM_PROMPT = """\
You are an EasyCron testing agent. You manage cron jobs via MCP tools.

Ground rules:
- Webhook HMAC secret for every job: "playground-secret"
- Always use timezone "UTC" unless the user says otherwise.
- Tag every job with env:playground so cleanup is easy.
- Be concise — show what you did, skip the fluff.
- NEVER assume a cron schedule. If the user does not specify when or how
  often a job should run, ASK them before creating it. Do not default to
  "every 5 minutes" or any other interval.

Webhook URL convention:
  The webhook receiver is at {webhook_base}
  To make it actually check something, append ?check=<url-encoded-target>
  Examples:
    {webhook_base}?check=https://wttr.in/Montreal?format=j1      (weather)
    {webhook_base}?check=https://www.githubstatus.com/api/v2/status.json
  The receiver GETs the target URL on every webhook and logs the result.
  Pick a real, publicly accessible URL that returns useful data for what
  the user is asking to monitor.

MCP tools available:
  create-job, list-jobs, get-job, update-job, delete-job
  pause-job, resume-job, trigger-job, next-run, resolve-schedule

Execution visibility:
  get-job returns the job's last 5 executions, each with:
    - status: emitted | in_progress | delivered | failed
    - trigger_type: scheduled | manual
    - fired_at, scheduled_at

Circuit breaker:
  EasyCron tracks delivery attempts per webhook URL. After consecutive
  failures on the same URL the circuit opens and deliveries are skipped
  (status=failed, outcome=circuit_open) until the endpoint recovers.
  For demos use http://host.docker.internal:9999/dead — the receiver's
  /dead path returns HTTP 400 immediately (non-retryable), so executions
  flip to 'failed' within seconds. Do NOT use a dead port (connection
  refused errors are retried for up to 12 minutes, blocking the demo).
"""

# ---------------------------------------------------------------------------
# Scenario prompts — triggered by slash commands
# ---------------------------------------------------------------------------
SCENARIOS = {
    "/monitor": (
        "Create 5 website-monitoring jobs, each firing every 2 minutes "
        "(cron: '*/2 * * * *'). Target these URLs:\n"
        "  1. https://httpbin.org/get  2. https://github.com  3. https://google.com  "
        "4. https://cloudflare.com  5. https://example.com\n"
        "Name them monitor-<site>. Tag each with type:monitor and env:playground. "
        "Use the webhook receiver URL and secret."
    ),
    "/stress": (
        "Stress test: create {n} jobs as fast as possible. "
        "Names: stress-001 through stress-{n:03d}. Cron: '*/1 * * * *'. "
        "Tag: type:stress, env:playground. Webhook receiver URL + secret. "
        "After creating them all, list jobs to confirm the count."
    ),
    "/lifecycle": (
        "Run a full lifecycle test on a single job:\n"
        "1. Create 'lifecycle-test' (every minute, webhook receiver, tagged env:playground)\n"
        "2. Get its details\n"
        "3. Pause it — confirm it shows disabled\n"
        "4. Resume it — confirm enabled\n"
        "5. Trigger it manually\n"
        "6. Get details again — check the manual execution appears in the last executions\n"
        "7. Delete it\n"
        "Report pass/fail for each step."
    ),
    "/circuit": (
        "Demonstrate EasyCron's circuit breaker:\n"
        "\n"
        "IMPORTANT: The dead endpoint is http://host.docker.internal:9999/dead — "
        "this is the REAL receiver but its /dead path returns HTTP 400 immediately "
        "(non-retryable), so executions flip to 'failed' in seconds.\n"
        "\n"
        "1. Create job 'circuit-demo' (cron: '*/1 * * * *') with "
        "   webhook_url=http://host.docker.internal:9999/dead. "
        "   Tag: type:circuit, env:playground.\n"
        "2. Fire all 5 triggers IN A SINGLE BATCH — one response with 5 trigger-job "
        "   calls at once. Do NOT trigger them one at a time.\n"
        "3. Then call get-job ONCE. The 400 responses are non-retryable so executions "
        "   should already show 'failed'. If any are still 'emitted', call get-job "
        "   one more time. Do NOT poll in a loop — two get-job calls maximum.\n"
        "4. Report: how many executions show 'failed'.\n"
        "5. Recover: call update-job to set webhook_url={webhook_base}.\n"
        "6. Trigger once, call get-job once (wait a few seconds if needed), confirm "
        "   the execution is 'delivered'.\n"
        "7. Summarise: N failed → circuit would open after 5 consecutive failures on "
        "   the same URL. Switching the URL gives a clean circuit slate. "
        "   Traditional crons have none of this visibility.\n"
        "Do NOT delete the job."
    ),
    "/schedule-lab": (
        "Test the schedule resolver — both natural language AND raw cron expressions.\n"
        "Call resolve-schedule for each input and report: input → cron expression → "
        "human description → first next-run time.\n"
        "\n"
        "--- Natural language (these should all work): ---\n"
        "  1.  'every 5 minutes'\n"
        "  2.  'every 2 hours'\n"
        "  3.  'hourly'\n"
        "  4.  'daily at 9am'\n"
        "  5.  'every day at 3:30pm'\n"
        "  6.  'every weekday at 9am'\n"
        "  7.  'every monday at 3pm'\n"
        "  8.  'every friday at 14:30'\n"
        "\n"
        "--- Raw cron expressions (pass-through validation): ---\n"
        "  9.  '*/15 9-17 * * 1-5'   (every 15 min, business hours, weekdays)\n"
        " 10.  '0 0 1 */3 *'          (quarterly on the 1st at midnight)\n"
        " 11.  '30 * * * *'           (every hour on the half hour)\n"
        "\n"
        "--- Edge cases (expected to fail — show the boundary): ---\n"
        " 12.  'twice a day'\n"
        " 13.  'first monday of every month at noon'\n"
        "\n"
        "Present results as a table. Mark each as PASS/FAIL. At the end, note that "
        "natural language covers common patterns and raw cron covers everything else."
    ),
    "/audit": (
        "Execution audit — show the real delivery record for every job:\n"
        "\n"
        "1. List all jobs.\n"
        "2. For each job, call get-job to retrieve its last 5 executions.\n"
        "3. Build a table with columns: "
        "   Job Name | Enabled | Last 5 statuses (delivered/failed/in_progress) | "
        "   Most recent fired_at\n"
        "4. At the bottom: total delivered vs total failed across all jobs.\n"
        "\n"
        "This is the execution audit that fire-and-forget crons can't give you."
    ),
    "/timeline": (
        "Build a schedule timeline — preview upcoming fires across all jobs:\n"
        "\n"
        "1. First, use resolve-schedule to validate and describe these four schedules:\n"
        "   a. 'every 5 minutes'\n"
        "   b. 'every hour at quarter past'\n"
        "   c. 'every weekday at 8am'\n"
        "   d. 'every sunday at midnight'\n"
        "2. Create four jobs with those schedules:\n"
        "   Names: timeline-5min, timeline-hourly, timeline-weekday, timeline-sunday\n"
        "   Tags: type:timeline, env:playground. Webhook receiver URL + secret.\n"
        "3. Call next-run on each job and show the next 5 fire times per job.\n"
        "4. Sort ALL upcoming times across all 4 jobs into a single chronological "
        "   list (show up to 12 entries) and label which job fires when.\n"
        "\n"
        "This is schedule coordination — instant preview without waiting for logs."
    ),
    "/cleanup": (
        "List all jobs, then delete every single one. "
        "Report how many were deleted."
    ),
    "/stats": (
        "Full status report:\n"
        "1. List all jobs.\n"
        "2. For each job, call get-job to see recent executions.\n"
        "3. Report: total jobs | enabled vs paused | "
        "   for each job: name, next run time, last execution status.\n"
        "4. Count total delivered and failed executions across all jobs."
    ),
}


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
def extract_text(result) -> str:
    """Turn an MCP CallToolResult into a plain string for Claude."""
    parts = []
    for block in result.content:
        if hasattr(block, "text"):
            parts.append(block.text)
        else:
            parts.append(str(block))
    return "\n".join(parts) if parts else "(empty)"


# ---------------------------------------------------------------------------
# Core agent loop
# ---------------------------------------------------------------------------
async def run_agent():
    easycron_url = os.environ.get("EASYCRON_URL", "http://localhost:8080")
    api_key = os.environ.get("EASYCRON_API_KEY", "")
    webhook_base = os.environ.get(
        "WEBHOOK_RECEIVER_URL", "http://host.docker.internal:9999/webhook"
    )
    model = os.environ.get("ANTHROPIC_MODEL", "claude-haiku-4-5")

    if not api_key:
        print("EASYCRON_API_KEY is not set.")
        sys.exit(1)
    if not os.environ.get("ANTHROPIC_API_KEY"):
        print("ANTHROPIC_API_KEY is not set.")
        sys.exit(1)

    claude = anthropic.Anthropic()
    mcp_url = f"{easycron_url}/mcp"

    print(f"Connecting to EasyCron MCP at {mcp_url} ...")

    async with streamablehttp_client(
        mcp_url, headers={"Authorization": f"Bearer {api_key}"}
    ) as (read_stream, write_stream, _):
        async with ClientSession(read_stream, write_stream) as session:
            await session.initialize()

            # Discover tools and convert MCP inputSchema → Anthropic input_schema
            mcp_tools = (await session.list_tools()).tools
            anthropic_tools = [
                {
                    "name": t.name,
                    "description": t.description,
                    "input_schema": t.inputSchema,
                }
                for t in mcp_tools
            ]
            tool_names = [t["name"] for t in anthropic_tools]
            print(f"Connected  ({len(tool_names)} tools: {', '.join(tool_names)})")
            print(f"Model: {model}  |  Webhook: {webhook_base}")
            print("\nCommands: " + "  ".join(SCENARIOS.keys()) + "  /quit\n")

            system_msg = SYSTEM_PROMPT.format(webhook_base=webhook_base)
            messages: list[dict] = []

            # REPL
            while True:
                try:
                    user_input = input("You: ").strip()
                except (EOFError, KeyboardInterrupt):
                    print("\nBye!")
                    break

                if not user_input:
                    continue
                if user_input == "/quit":
                    print("Bye!")
                    break

                # Expand scenario commands
                for cmd, template in SCENARIOS.items():
                    if user_input.startswith(cmd):
                        rest = user_input[len(cmd):].strip()
                        n = int(rest) if rest.isdigit() else 10
                        user_input = template.format(n=n, webhook_base=webhook_base)
                        print(f"  -> scenario {cmd}\n")
                        break

                messages.append({"role": "user", "content": user_input})

                t0 = time.time()
                calls = 0

                # Agent tool-use loop
                while True:
                    # Stream text live; accumulate the full response for tool handling
                    text_started = False
                    with claude.messages.stream(
                        model=model,
                        max_tokens=8096,
                        system=system_msg,
                        tools=anthropic_tools,
                        messages=messages,
                    ) as stream:
                        for text in stream.text_stream:
                            if not text_started:
                                print("\nAgent: ", end="", flush=True)
                                text_started = True
                            print(text, end="", flush=True)
                        response = stream.get_final_message()

                    # Append assistant turn to history
                    messages.append({"role": "assistant", "content": response.content})

                    if response.stop_reason == "end_turn":
                        elapsed = time.time() - t0
                        if calls:
                            print(f"\n  [{calls} tool call(s) in {elapsed:.1f}s]")
                        print()
                        break

                    # Process tool calls
                    if response.stop_reason == "tool_use":
                        if text_started:
                            print()  # newline after any partial text

                        tool_results = []
                        for block in response.content:
                            if block.type == "tool_use":
                                calls += 1
                                compact = json.dumps(block.input, separators=(",", ":"))
                                print(f"  -> {block.name}({compact})")

                                try:
                                    result = await session.call_tool(block.name, block.input)
                                    content = extract_text(result)
                                    is_error = False
                                except Exception as exc:
                                    content = f"MCP error: {exc}"
                                    is_error = True

                                tool_results.append({
                                    "type": "tool_result",
                                    "tool_use_id": block.id,
                                    "content": content,
                                    "is_error": is_error,
                                })

                        messages.append({"role": "user", "content": tool_results})

                # Keep context from growing unbounded (preserve last 80 turns)
                if len(messages) > 120:
                    messages = messages[-80:]


if __name__ == "__main__":
    asyncio.run(run_agent())
