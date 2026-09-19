#!/usr/bin/env python3
"""Is the residential stall filtering, or just a slow home line?

Three questions a reviewer asks about the 20 to 32 KiB band, answered from the
sealed day files:

  1. Does it follow the clock? Congestion has a daily rhythm; a filter does not.
  2. Does it follow the target? Congestion hits whatever is being fetched at a
     bad moment; a filter hits the same hosts every day.
  3. Does it follow the size of the response? If short responses arrive whole and
     long ones are cut at the same place, the trigger is length, not identity.
  4. Does it follow the category, or is that just what the list is made of? Raw
     counts follow the list; only a share against the base rate says anything.

    python analysis/stalls.py
"""

import collections
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from figures import CONTROL, read_rows  # noqa: E402

HOME = "ru-mow-home"
HOST = "ru-msk-vps"


def control_index():
    """(slot, url) -> (verdict, bytes_read, body_len) from the Dutch control."""
    out = {}
    for row in read_rows(CONTROL):
        out[(row["run_id"], row["url"])] = (
            row["verdict"],
            row.get("bytes_read") or 0,
            row.get("body_len") or 0,
        )
    return out


def hour_of(slot):
    return slot.split("T")[1][:2]


def main():
    control = control_index()

    by_hour = collections.defaultdict(lambda: collections.Counter())
    stall_days = collections.defaultdict(set)
    seen_days = collections.defaultdict(set)
    stall_ctrl_bytes = []
    ok_ctrl_bytes = []
    stall_bytes = []
    ctrl_sizes = collections.defaultdict(list)
    category = {}
    split = collections.defaultdict(collections.Counter)

    for probe in (HOME, HOST):
        for row in read_rows(probe):
            key = (row["run_id"], row["url"])
            peer = control.get(key)
            if peer is None or peer[0] != "ok":
                continue
            hour = hour_of(row["run_id"])
            day = row["run_id"].split("T")[0]
            verdict = row["verdict"]

            by_hour[probe][(hour, "seen")] += 1
            if verdict != "ok":
                by_hour[probe][(hour, "failed")] += 1
            if verdict == "response_timeout":
                by_hour[probe][(hour, "stalled")] += 1

            if probe != HOME:
                continue
            cat = row.get("category") or "NONE"
            category[row["url"]] = cat
            split[cat]["paired"] += 1
            split[cat][verdict] += 1
            if peer[0] == "ok":
                ctrl_sizes[row["url"]].append(peer[1])
            seen_days[row["url"]].add(day)
            if verdict == "response_timeout":
                stall_days[row["url"]].add(day)
                stall_ctrl_bytes.append(peer[1])
                stall_bytes.append(row.get("bytes_read") or 0)
            elif verdict == "ok":
                ok_ctrl_bytes.append(peer[1])

    print("1. Does it follow the clock?\n")
    print("   %-13s %-6s %10s %10s %10s" % ("probe", "slot", "measured", "failed", "stalled"))
    for probe in (HOME, HOST):
        for hour in sorted({h for h, _ in by_hour[probe]}):
            seen = by_hour[probe][(hour, "seen")]
            failed = by_hour[probe][(hour, "failed")]
            stalled = by_hour[probe][(hour, "stalled")]
            if not seen:
                continue
            print("   %-13s %-6s %10d %9.1f%% %9.1f%%"
                  % (probe, hour + "Z", seen, 100 * failed / seen, 100 * stalled / seen))
    print()

    print("2. Does it follow the target?\n")
    repeat = []
    for url, days in stall_days.items():
        repeat.append(len(days) / float(len(seen_days[url])))
    repeat.sort()
    once = sum(1 for r in repeat if r <= 0.25)
    most = sum(1 for r in repeat if r >= 0.75)
    print("   %d distinct targets ever stalled on the residential line" % len(repeat))
    print("   stalled on 75%% or more of the days they were measured: %d (%.0f%%)"
          % (most, 100 * most / max(1, len(repeat))))
    print("   stalled on 25%% or fewer of those days:                  %d (%.0f%%)"
          % (once, 100 * once / max(1, len(repeat))))
    if repeat:
        mid = repeat[len(repeat) // 2]
        print("   median share of measured days a stalling target stalls: %.0f%%" % (100 * mid))
    print()

    print("3. Does it follow the size of the response?\n")

    def buckets(values, label):
        edges = [0, 8, 20, 32, 64, 128, 1 << 30]
        names = ["<8", "8-20", "20-32", "32-64", "64-128", ">128"]
        counts = [0] * len(names)
        for v in values:
            kib = v / 1024.0
            for i in range(len(names)):
                if edges[i] <= kib < edges[i + 1]:
                    counts[i] += 1
                    break
        total = sum(counts) or 1
        print("   %-46s n=%d" % (label, total))
        for name, count in zip(names, counts):
            bar = "#" * int(round(48 * count / total))
            print("     %7s KiB %6.1f%%  %s" % (name, 100 * count / total, bar))
        print()

    buckets(ok_ctrl_bytes, "control size of responses the home line got whole")
    buckets(stall_ctrl_bytes, "control size of responses the home line lost")
    buckets(stall_bytes, "how far the home line got before the stall")

    big_ok = sum(1 for v in ok_ctrl_bytes if v / 1024.0 >= 32)
    big_stall = sum(1 for v in stall_ctrl_bytes if v / 1024.0 >= 32)
    small_stall = sum(1 for v in stall_ctrl_bytes if v / 1024.0 < 32)
    print("   responses the control read as 32 KiB or larger:")
    print("     arrived whole at the home line: %d" % big_ok)
    print("     stalled at the home line:       %d" % big_stall)
    if big_ok + big_stall:
        print("     stall rate among large responses: %.1f%%"
              % (100 * big_stall / (big_ok + big_stall)))
    print("   responses smaller than 32 KiB that stalled anyway: %d" % small_stall)
    print()

    print("4. Does it follow the category, or is that just the list?\n")

    def median(values):
        values = sorted(values)
        return values[len(values) // 2] if values else 0

    big = set()
    for url, sizes in ctrl_sizes.items():
        if median(sizes) / 1024.0 >= 32:
            big.add(url)
    steady = set()
    for url, days in stall_days.items():
        if len(days) / float(len(seen_days[url])) >= 0.75:
            steady.add(url)
    base = len(big & steady) / float(len(big) or 1)
    small = set(ctrl_sizes) - big
    print("   %d targets answer the control with 32 KiB or more, %d of them stall on"
          % (len(big), len(big & steady)))
    print("   three quarters of their days or more, so the base rate is %.1f%%." % (100 * base))
    print("   of the %d targets under 32 KiB, %d stall that steadily, %.1f%%\n"
          % (len(small), len(small & steady), 100 * len(small & steady) / float(len(small) or 1)))
    print("   %-9s %10s %10s %8s %10s" % ("category", "large", "stalling", "share", "vs base"))
    rows = []
    for cat in {category[u] for u in big}:
        here = [u for u in big if category[u] == cat]
        hit = [u for u in here if u in steady]
        if len(here) < 10:
            continue
        share = len(hit) / float(len(here))
        rows.append((share / base if base else 0, cat, len(here), len(hit), share))
    for ratio, cat, n, hit, share in sorted(rows, reverse=True):
        print("   %-9s %10d %10d %7.1f%% %9.1fx" % (cat, n, hit, 100 * share, ratio))
    print()

    print("   where each category dies, over every paired row on the home line\n")
    print("   %-9s %10s %8s %14s %18s" % ("category", "paired", "ok", "tls_timeout", "response_timeout"))
    for cat, counts in sorted(split.items(), key=lambda kv: -kv[1]["paired"]):
        paired = counts["paired"]
        if paired < 500:
            continue
        print("   %-9s %10d %7.0f%% %13.0f%% %17.1f%%"
              % (cat, paired, 100 * counts["ok"] / paired,
                 100 * counts["tls_timeout"] / paired,
                 100 * counts["response_timeout"] / paired))


if __name__ == "__main__":
    main()
