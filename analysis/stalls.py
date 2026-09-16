#!/usr/bin/env python3
"""Is the residential stall filtering, or just a slow home line?

Three questions a reviewer asks about the 20 to 32 KiB band, answered from the
sealed day files:

  1. Does it follow the clock? Congestion has a daily rhythm; a filter does not.
  2. Does it follow the target? Congestion hits whatever is being fetched at a
     bad moment; a filter hits the same hosts every day.
  3. Does it follow the size of the response? If short responses arrive whole and
     long ones are cut at the same place, the trigger is length, not identity.

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


if __name__ == "__main__":
    main()
