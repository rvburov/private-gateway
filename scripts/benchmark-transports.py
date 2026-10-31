#!/usr/bin/env python3
"""Сравнить новые запросы через два уже работающих локальных клиента Xray."""

import argparse
import ipaddress
import json
import math
import statistics
import subprocess
import sys
from urllib.parse import urlsplit


DEFAULT_URL = "https://cp.cloudflare.com/generate_204"
LABELS = {"tcp": "TCP + REALITY + Vision", "xhttp": "XHTTP + REALITY"}
CURL_METRICS = ('{"http_code":"%{http_code}",'
                '"time_starttransfer":%{time_starttransfer},"time_total":%{time_total}}')
NOTES = [
    "Клиенты Xray должны работать постоянно: этот инструмент не запускает и не перезапускает их.",
    "Первая проба показана отдельно; если клиент уже передавал трафик, она может быть прогретой.",
    "TTFB включает SOCKS, разрешение имени, целевой HTTPS и ответ сайта; это не чистый RTT.",
    "Переиспользование HTTP/2/XMUX требует отдельного наблюдения; по TTFB оно не доказано.",
    "Ускорение не гарантировано. Пропускная способность и устойчивость длительных соединений здесь не измеряются.",
]


class Parser(argparse.ArgumentParser):
    def error(self, message):
        # Не повторять неизвестные аргументы: по ошибке здесь могут оказаться секреты.
        if message.startswith("unrecognized arguments:"):
            message = "неизвестные аргументы; используйте --help"
        super().error(message)


def integer(minimum, maximum):
    def parse(value):
        try:
            number = int(value)
        except (ValueError, TypeError):
            raise argparse.ArgumentTypeError("требуется целое число") from None
        if not minimum <= number <= maximum:
            raise argparse.ArgumentTypeError(f"допустимы значения от {minimum} до {maximum}")
        return number
    return parse


def seconds(value):
    try:
        number = float(value)
    except (ValueError, TypeError):
        raise argparse.ArgumentTypeError("требуется положительное число секунд") from None
    if not math.isfinite(number) or not 0 < number <= 300:
        raise argparse.ArgumentTypeError("тайм-аут должен быть больше 0 и не больше 300 секунд")
    return number


def socks_endpoint(value):
    try:
        parsed = urlsplit("socks5h://" + value)
        host, port = parsed.hostname, parsed.port
        if (not host or port is None or not 1 <= port <= 65535 or parsed.username is not None
                or parsed.password is not None or parsed.path or parsed.query or parsed.fragment):
            raise ValueError
        if host == "localhost":
            host = "127.0.0.1"
        address = ipaddress.ip_address(host)
        if not address.is_loopback or (isinstance(address, ipaddress.IPv6Address) and address.scope_id):
            raise ValueError
        return f"[{address}]:{port}" if address.version == 6 else f"{address}:{port}"
    except (ValueError, TypeError):
        raise argparse.ArgumentTypeError("нужен локальный SOCKS-адрес вида 127.0.0.1:1080 или [::1]:1080") from None


def target_url(value):
    try:
        parsed = urlsplit(value)
        if (parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username is not None
                or parsed.password is not None or parsed.query or parsed.fragment
                or any(character.isspace() or ord(character) < 32 for character in value)):
            raise ValueError
        if parsed.port is not None and not 1 <= parsed.port <= 65535:
            raise ValueError
    except (ValueError, TypeError):
        raise argparse.ArgumentTypeError("нужен HTTP(S)-адрес без авторизации, параметров запроса и фрагмента") from None
    return value


def parser():
    result = Parser(description=__doc__)
    result.add_argument("--tcp-socks", required=True, type=socks_endpoint,
                        help="локальный SOCKS работающего клиента TCP + Vision")
    result.add_argument("--xhttp-socks", required=True, type=socks_endpoint,
                        help="локальный SOCKS работающего клиента XHTTP")
    result.add_argument("--requests", type=integer(1, 1000), default=10,
                        help="число измеряемых запросов на транспорт после прогрева (по умолчанию 10)")
    result.add_argument("--warmup", type=integer(0, 1000), default=1,
                        help="дополнительный прогрев на транспорт после первой пробы (по умолчанию 1)")
    result.add_argument("--url", type=target_url, default=DEFAULT_URL,
                        help="один HTTP(S)-адрес для обоих транспортов; в отчёт не включается")
    result.add_argument("--expected-status", type=integer(100, 599), default=204,
                        help="ожидаемый HTTP-статус (по умолчанию 204)")
    result.add_argument("--connect-timeout", type=seconds, default=10.0,
                        help="тайм-аут соединения curl, секунды (по умолчанию 10)")
    result.add_argument("--max-time", type=seconds, default=30.0,
                        help="общий тайм-аут запроса curl, секунды (по умолчанию 30)")
    result.add_argument("--json", action="store_true", help="выдать отчёт JSON вместо текста")
    return result


def probe(endpoint, url, expected_status, connect_timeout, max_time):
    result = {"success": False, "curl_exit_code": None, "http_status": None,
              "ttfb_seconds": None, "total_seconds": None, "error": None}
    command = ["curl", "-q", "--proxy", "socks5h://" + endpoint, "--noproxy", "",
               "--retry", "0", "--connect-timeout", str(connect_timeout), "--max-time", str(max_time),
               "--silent", "--show-error", "--output", "/dev/null", "--write-out", CURL_METRICS,
               "--proto", "=http,https", "--url", url]
    try:
        completed = subprocess.run(command, stdin=subprocess.DEVNULL, capture_output=True,
                                   text=True, check=False, timeout=max_time + 5)
    except FileNotFoundError:
        result["error"] = "curl_unavailable"
        return result
    except subprocess.TimeoutExpired:
        result["error"] = "process_timeout"
        return result
    except OSError:
        result["error"] = "process_start_failed"
        return result
    result["curl_exit_code"] = completed.returncode
    if completed.returncode:
        result["error"] = "curl_exit"
        return result
    try:
        metrics = json.loads(completed.stdout)
        status = metrics["http_code"]
        if not isinstance(status, str) or len(status) != 3 or not status.isascii() or not status.isdigit():
            raise ValueError
        status = int(status)
        if not 100 <= status <= 599:
            raise ValueError
        result["http_status"] = status
        if status != expected_status:
            result["error"] = "unexpected_status"
            return result
        ttfb, total = metrics["time_starttransfer"], metrics["time_total"]
        if (type(ttfb) not in (int, float) or type(total) not in (int, float)
                or not math.isfinite(ttfb) or not math.isfinite(total) or ttfb <= 0 or total < ttfb):
            raise ValueError
    except (json.JSONDecodeError, ValueError, TypeError, KeyError, OverflowError):
        result["error"] = "invalid_metrics"
        return result
    result.update(success=True, ttfb_seconds=ttfb, total_seconds=total)
    return result


def summarize(samples):
    successful = [sample["ttfb_seconds"] for sample in samples if sample["success"]]
    return {"attempts": len(samples), "successes": len(successful), "errors": len(samples) - len(successful),
            "median_ttfb_seconds": statistics.median(successful) if successful else None,
            "samples": samples}


def benchmark(options):
    transports = {"tcp": options.tcp_socks, "xhttp": options.xhttp_socks}
    phases = {name: {"cold": None, "warmup": [], "measured": []} for name in transports}

    def perform(name):
        return probe(transports[name], options.url, options.expected_status,
                     options.connect_timeout, options.max_time)

    for name in transports:
        phases[name]["cold"] = perform(name)
    for _ in range(options.warmup):
        for name in transports:
            phases[name]["warmup"].append(perform(name))
    for _ in range(options.requests):
        for name in transports:
            phases[name]["measured"].append(perform(name))

    results = {name: {"label": LABELS[name], "cold": data["cold"], "warmup": summarize(data["warmup"]),
                      "measured": summarize(data["measured"])} for name, data in phases.items()}
    comparison = {"status": "unavailable", "median_reduction_seconds": None,
                  "median_reduction_percent": None}
    tcp, xhttp = results["tcp"]["measured"], results["xhttp"]["measured"]
    if tcp["successes"] and xhttp["successes"] and not tcp["errors"] and not xhttp["errors"]:
        difference = tcp["median_ttfb_seconds"] - xhttp["median_ttfb_seconds"]
        comparison.update(status="available", median_reduction_seconds=difference,
                          median_reduction_percent=difference / tcp["median_ttfb_seconds"] * 100)
    return {"schema_version": 1, "requests_per_transport": options.requests,
            "additional_warmup_per_transport": options.warmup, "expected_http_status": options.expected_status,
            "connect_timeout_seconds": options.connect_timeout, "max_time_seconds": options.max_time,
            "order": "alternating_tcp_then_xhttp", "reuse_verified": False,
            "results": results, "comparison": comparison, "notes": NOTES}


def display(report):
    lines = ["Сравнение новых запросов через TCP + Vision и XHTTP", ""]
    for result in report["results"].values():
        cold = result["cold"]
        lines.append(result["label"])
        if cold["success"]:
            lines.append(f"  Первая проба отдельно: {cold['ttfb_seconds'] * 1000:.1f} мс")
        else:
            lines.append(f"  Первая проба: ошибка ({cold['error']}, curl={cold['curl_exit_code']})")
        warmup, measured = result["warmup"], result["measured"]
        lines.append(f"  Дополнительный прогрев: {warmup['successes']}/{warmup['attempts']} успешно; ошибок {warmup['errors']}")
        lines.append(f"  Измерения: {measured['successes']}/{measured['attempts']} успешно; ошибок {measured['errors']}")
        median = measured["median_ttfb_seconds"]
        lines.append(f"  Медиана TTFB успешных измерений: {median * 1000:.1f} мс" if median is not None
                     else "  Медиана TTFB: нет успешных измерений")
        for index, sample in enumerate(measured["samples"], 1):
            detail = (f"{sample['ttfb_seconds'] * 1000:.1f} мс" if sample["success"] else
                      f"ошибка ({sample['error']}, curl={sample['curl_exit_code']}, HTTP={sample['http_status']})")
            lines.append(f"    {index}: {detail}")
        lines.append("")
    comparison = report["comparison"]
    if comparison["status"] == "available":
        lines.append(f"Разность медиан TCP − XHTTP: {comparison['median_reduction_seconds'] * 1000:.1f} мс")
        lines.append(f"Уменьшение медианы относительно TCP: {comparison['median_reduction_percent']:.1f}%")
        lines.append("Отрицательное значение означает, что XHTTP оказался медленнее в этой серии.")
    else:
        lines.append("Сравнение медиан недоступно: есть ошибки или нет успешных измерений.")
    lines.append("")
    lines.extend(report["notes"])
    return "\n".join(lines)


def main(argv=None):
    arguments = parser()
    options = arguments.parse_args(argv)
    if options.tcp_socks == options.xhttp_socks:
        arguments.error("для сравнения нужны разные локальные SOCKS-входы")
    try:
        report = benchmark(options)
    except KeyboardInterrupt:
        print("Измерение остановлено; незавершённая серия не выдана за результат.", file=sys.stderr)
        return 130
    print(json.dumps(report, ensure_ascii=False, allow_nan=False, indent=2) if options.json else display(report))
    failed = any(not result["cold"]["success"] or result["warmup"]["errors"] or result["measured"]["errors"]
                 for result in report["results"].values())
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
