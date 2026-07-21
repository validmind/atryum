#!/usr/bin/env python3
"""
Governance scenario driver for Atryum Agent Gateway.

Runs scenarios against a deployed Vertex AI agent and prints ALLOW/DENY outcomes.
Supports optional rule seeding via Atryum admin API and resilient error handling.
"""

import argparse
import json
import sys
import time
from dataclasses import dataclass, asdict
from pathlib import Path
from typing import Any, Optional
import traceback


@dataclass
class ToolCall:
    """Represents a tool call attempt during agent execution."""
    name: str
    allowed: bool
    error: Optional[str] = None


@dataclass
class ScenarioResult:
    """Result of running a single scenario."""
    scenario_id: str
    title: str
    expected: str
    observed: str
    tool_calls: list[ToolCall]
    final_message: Optional[str]
    error: Optional[str]
    duration_sec: float


def load_scenarios(path: Path) -> list[dict[str, Any]]:
    """Load scenarios from JSON file."""
    with open(path, "r") as f:
        data = json.load(f)
    if not isinstance(data, list):
        raise ValueError(f"scenarios.json must be an array, got {type(data)}")
    return data


def seed_rule(atryum_url: str, rule: dict[str, Any]) -> bool:
    """
    POST a rule to Atryum admin API.
    Returns True if successful, False otherwise.
    """
    try:
        import requests
    except ImportError:
        print("  WARNING: requests library not available, skipping rule seed", file=sys.stderr)
        return False

    try:
        # Assume rule_id is in the rule payload
        rule_id = rule.get("id", "unknown")
        admin_endpoint = f"{atryum_url}/api/v1/rules"

        response = requests.post(
            admin_endpoint,
            json=rule,
            timeout=10,
        )
        if response.status_code in (200, 201, 204):
            print(f"  [SEEDED] Rule {rule_id}")
            return True
        else:
            print(
                f"  [SEED-FAILED] Rule {rule_id}: {response.status_code} {response.text}",
                file=sys.stderr,
            )
            return False
    except Exception as e:
        print(f"  [SEED-ERROR] {type(e).__name__}: {e}", file=sys.stderr)
        return False


def run_scenario(
    agent_resource: str,
    scenario: dict[str, Any],
) -> ScenarioResult:
    """
    Run a single scenario against the agent.
    Returns a ScenarioResult with tool calls and outcome.
    """
    start_time = time.time()
    scenario_id = scenario.get("id", "unknown")
    title = scenario.get("title", "untitled")
    prompt = scenario.get("prompt", "")
    expected = scenario.get("expected", "UNKNOWN")
    rule = scenario.get("rule")
    profile = scenario.get("profile", "default")

    tool_calls: list[ToolCall] = []
    final_message: Optional[str] = None
    error: Optional[str] = None
    observed = "UNKNOWN"

    try:
        import vertexai
        from vertexai import generative_ai

        # Initialize client
        # Note: assumes gcloud auth is set up and project/location are in env
        client = vertexai.Client()
        engine = client.agent_engines.get(agent_resource)

        # Stream the query
        stream = engine.stream_query(
            message=prompt,
            user_id=profile,  # Use profile as user_id for governance scoping
        )

        denied_count = 0
        allowed_count = 0
        last_message = ""

        for chunk in stream:
            # Parse response chunks for tool calls and outcomes
            if hasattr(chunk, "text"):
                last_message += chunk.text
            if hasattr(chunk, "function_calls"):
                for fc in chunk.function_calls:
                    tool_name = fc.name if hasattr(fc, "name") else str(fc)
                    tool_calls.append(ToolCall(name=tool_name, allowed=True))
                    allowed_count += 1
            if hasattr(chunk, "tool_results"):
                for tr in chunk.tool_results:
                    # If we got a result, tool was allowed
                    result_status = getattr(tr, "error", None)
                    is_blocked = result_status and "403" in str(result_status)
                    if is_blocked:
                        denied_count += 1
                        if tool_calls and tool_calls[-1].allowed:
                            tool_calls[-1].allowed = False
                            tool_calls[-1].error = "403 Forbidden"
                    else:
                        allowed_count += 1

        final_message = last_message[:500] if last_message else None

        # Determine observed outcome: if any denied, DENY; else ALLOW
        if denied_count > 0:
            observed = "DENY"
        elif allowed_count > 0:
            observed = "ALLOW"
        else:
            observed = "NO_TOOLS"

    except Exception as e:
        error = f"{type(e).__name__}: {str(e)}"
        observed = "ERROR"
        # Extract denied tool errors
        if "403" in str(e):
            observed = "DENY"

    duration = time.time() - start_time

    return ScenarioResult(
        scenario_id=scenario_id,
        title=title,
        expected=expected,
        observed=observed,
        tool_calls=tool_calls,
        final_message=final_message,
        error=error,
        duration_sec=duration,
    )


def print_scenario_result(result: ScenarioResult) -> None:
    """Print a single scenario result in readable format."""
    outcome_color = ""
    outcome_reset = ""
    outcome_marker = result.observed

    # Colored markers if terminal supports it
    if sys.stdout.isatty():
        if result.observed == "ALLOW":
            outcome_color = "\033[32m"  # Green
            outcome_reset = "\033[0m"
        elif result.observed == "DENY":
            outcome_color = "\033[31m"  # Red
            outcome_reset = "\033[0m"
        elif result.observed == "ERROR":
            outcome_color = "\033[33m"  # Yellow
            outcome_reset = "\033[0m"

    print(f"\n{outcome_color}[{outcome_marker}]{outcome_reset} {result.title} (ID: {result.scenario_id})")
    print(f"  Expected:  {result.expected}")
    print(f"  Observed:  {outcome_color}{result.observed}{outcome_reset}")
    print(f"  Duration:  {result.duration_sec:.2f}s")

    if result.tool_calls:
        print("  Tool calls:")
        for tool in result.tool_calls:
            call_marker = "ALLOW" if tool.allowed else "DENY"
            call_color = "\033[32m" if tool.allowed else "\033[31m" if sys.stdout.isatty() else ""
            call_reset = "\033[0m" if sys.stdout.isatty() else ""
            error_msg = f" ({tool.error})" if tool.error else ""
            print(f"    {call_color}[{call_marker}]{call_reset} {tool.name}{error_msg}")

    if result.final_message:
        msg_preview = result.final_message.replace("\n", " ")[:80]
        print(f"  Message:   {msg_preview}...")

    if result.error:
        print(f"  ERROR:     {result.error}")


def print_summary_table(results: list[ScenarioResult]) -> None:
    """Print a summary table of all results."""
    print("\n" + "=" * 80)
    print("SUMMARY")
    print("=" * 80)

    # Count outcomes
    passed = sum(1 for r in results if r.expected == r.observed)
    total = len(results)
    allow_count = sum(1 for r in results if r.observed == "ALLOW")
    deny_count = sum(1 for r in results if r.observed == "DENY")
    error_count = sum(1 for r in results if r.observed == "ERROR")

    print(f"\nTotal scenarios: {total}")
    print(f"  Passed:     {passed}/{total} ({100*passed//total if total > 0 else 0}%)")
    print(f"  ALLOW:      {allow_count}")
    print(f"  DENY:       {deny_count}")
    print(f"  ERROR:      {error_count}")

    print("\nDetailed Results:")
    print("-" * 80)
    print(f"{'ID':<15} {'Title':<25} {'Expected':<10} {'Observed':<10} {'Status':<10}")
    print("-" * 80)

    for result in results:
        status = "PASS" if result.expected == result.observed else "FAIL"
        title = result.title[:23]
        print(
            f"{result.scenario_id:<15} {title:<25} {result.expected:<10} "
            f"{result.observed:<10} {status:<10}"
        )

    print("-" * 80)


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Run governance scenarios against a deployed agent",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # Run all scenarios
  python run_scenarios.py \\
    --agent-resource projects/781736505510/locations/us-central1/reasoningEngines/1876979048555479040

  # Run specific scenario
  python run_scenarios.py \\
    --agent-resource projects/781736505510/locations/us-central1/reasoningEngines/1876979048555479040 \\
    --only scenario-1

  # Seed rules before running (requires VPC access to Atryum admin)
  python run_scenarios.py \\
    --agent-resource projects/781736505510/locations/us-central1/reasoningEngines/1876979048555479040 \\
    --seed-rules \\
    --atryum-url http://atryum-admin:8000

  # Use custom scenarios file
  python run_scenarios.py \\
    --agent-resource projects/781736505510/locations/us-central1/reasoningEngines/1876979048555479040 \\
    --scenarios-file /custom/path/scenarios.json
        """,
    )

    parser.add_argument(
        "--agent-resource",
        required=True,
        help="Full resource name of the agent engine "
        "(e.g. projects/PROJECT/locations/REGION/reasoningEngines/ENGINE_ID)",
    )
    parser.add_argument(
        "--scenarios-file",
        type=Path,
        default=Path(__file__).parent.parent / "scenarios" / "scenarios.json",
        help="Path to scenarios.json (default: ../scenarios/scenarios.json)",
    )
    parser.add_argument(
        "--only",
        type=str,
        default=None,
        help="Run only a specific scenario ID",
    )
    parser.add_argument(
        "--seed-rules",
        action="store_true",
        help="POST each scenario's rule to Atryum admin API before running",
    )
    parser.add_argument(
        "--atryum-url",
        type=str,
        default="http://atryum-admin:8000",
        help="Atryum admin API base URL (default: http://atryum-admin:8000). "
        "Note: callout is only reachable in-VPC; run from a machine with access.",
    )

    args = parser.parse_args()

    # Load scenarios
    print(f"Loading scenarios from {args.scenarios_file}...")
    try:
        scenarios = load_scenarios(args.scenarios_file)
    except FileNotFoundError:
        print(f"ERROR: {args.scenarios_file} not found", file=sys.stderr)
        return 1
    except Exception as e:
        print(f"ERROR loading scenarios: {e}", file=sys.stderr)
        return 1

    print(f"Loaded {len(scenarios)} scenarios\n")

    # Filter if --only was specified
    if args.only:
        scenarios = [s for s in scenarios if s.get("id") == args.only]
        if not scenarios:
            print(f"ERROR: No scenario with ID '{args.only}'", file=sys.stderr)
            return 1
        print(f"Running only scenario: {args.only}\n")

    # Run scenarios
    results: list[ScenarioResult] = []

    for i, scenario in enumerate(scenarios, 1):
        scenario_id = scenario.get("id", "unknown")
        title = scenario.get("title", "untitled")

        print(f"[{i}/{len(scenarios)}] Running {scenario_id}: {title}")

        # Optionally seed rule
        if args.seed_rules and scenario.get("rule"):
            print(f"  Seeding rule...")
            seed_rule(args.atryum_url, scenario["rule"])

        # Run scenario
        try:
            result = run_scenario(args.agent_resource, scenario)
            results.append(result)
            print_scenario_result(result)
        except Exception as e:
            print(f"  EXCEPTION: {type(e).__name__}: {e}", file=sys.stderr)
            traceback.print_exc()
            # Record as error result
            result = ScenarioResult(
                scenario_id=scenario_id,
                title=title,
                expected=scenario.get("expected", "UNKNOWN"),
                observed="ERROR",
                tool_calls=[],
                final_message=None,
                error=f"{type(e).__name__}: {str(e)}",
                duration_sec=0.0,
            )
            results.append(result)
            continue

    # Print summary
    print_summary_table(results)

    # Exit with success only if all passed
    passed = sum(1 for r in results if r.expected == r.observed)
    if passed == len(results):
        print("\n✓ All scenarios passed")
        return 0
    else:
        print(f"\n✗ {len(results) - passed} scenario(s) failed")
        return 1


if __name__ == "__main__":
    sys.exit(main())
