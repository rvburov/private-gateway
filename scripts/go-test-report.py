#!/usr/bin/env python3
"""Свести вывод `go test -json` для scripts/checks.sh.

Печатает строку на пакет, итоги, каждый пропущенный тест с причиной и вывод упавших тестов.
В контейнере проверок TEST_DATABASE_URL и PRIVATE_GATEWAY_REAL_XRAY_BINARY заданы всегда, поэтому
тест, пропущенный из-за отсутствия одной из них, значит, что интеграционный путь молча не прошёл:
отчёт проваливается, а не проходит тихо.
"""

import json
import sys

MODULE = "github.com/rvburov/private-gateway/"
REQUIRED = ("TEST_DATABASE_URL", "PRIVATE_GATEWAY_REAL_XRAY_BINARY")


def short(package):
    return package[len(MODULE):] if package.startswith(MODULE) else package


def messages(lines):
    """Текст t.Skip и t.Error без собственных рамок go test: === и ---."""
    return [line.strip() for line in lines
            if line.strip() and not line.lstrip().startswith(("=== ", "--- "))]


def main():
    order, package_result, package_output = [], {}, {}
    test_result, test_output = {}, {}
    build_output = []
    for raw in sys.stdin:
        try:
            event = json.loads(raw)
        except json.JSONDecodeError:
            build_output.append(raw.rstrip("\n"))
            continue
        action = event.get("Action")
        if action == "build-output":
            build_output.append(event.get("Output", "").rstrip("\n"))
            continue
        if action == "build-fail":
            continue
        package, test = event.get("Package", ""), event.get("Test")
        if package not in package_output:
            order.append(package)
            package_output[package] = []
        if action == "output":
            if test:
                test_output.setdefault((package, test), []).append(event.get("Output", ""))
            else:
                package_output[package].append(event.get("Output", ""))
        elif action in ("pass", "fail", "skip"):
            if test:
                test_result[(package, test)] = action
            else:
                package_result[package] = (action, event.get("Elapsed"))

    failed_packages = []
    for package in order:
        # Пакет без итогового результата был прерван, и go test считает это сбоем.
        action, elapsed = package_result.get(package, ("fail", None))
        timing = f" ({elapsed:.1f}s)" if isinstance(elapsed, (int, float)) and action != "skip" else ""
        label = {"pass": "ok     ", "fail": "FAIL   ", "skip": "no test"}[action]
        print(f"{label} {short(package)}{timing}")
        if action == "fail":
            failed_packages.append(package)

    passed = [key for key, result in test_result.items() if result == "pass"]
    failed = [key for key, result in test_result.items() if result == "fail"]
    skipped = [key for key, result in test_result.items() if result == "skip"]
    print(f"\nGo tests and subtests: {len(passed)} passed, {len(failed)} failed, {len(skipped)} skipped.")

    missing_integration = []
    if skipped:
        print("Skipped:")
        for key in skipped:
            why = messages(test_output.get(key, [])) or ["(no reason given)"]
            print(f"  {short(key[0])} {key[1]}: {' '.join(why)}")
            if any(variable in line for line in why for variable in REQUIRED):
                missing_integration.append(key)

    for key in failed:
        print(f"\n--- FAIL {short(key[0])} {key[1]}")
        print("".join(test_output.get(key, [])), end="")
    failed_tests_by_package = {package for package, _ in failed}
    for package in failed_packages:
        if package not in failed_tests_by_package:
            # Паники, тайм-ауты и ошибки сборки — вывод пакета, а не теста.
            print(f"\n--- FAIL {short(package)}")
            print("".join(package_output[package]), end="")
    if build_output:
        print("\nBuild output:")
        print("\n".join(build_output))

    if missing_integration:
        print("\nIntegration tests skipped although the checks container sets "
              + " and ".join(REQUIRED) + ": the run is not valid.")
    return 1 if failed or failed_packages or missing_integration else 0


if __name__ == "__main__":
    sys.exit(main())
