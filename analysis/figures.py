#!/usr/bin/env python3
"""Draw the figures used in README.md straight from the sealed day files.

Every number in the README comes out of this script, so a reader can check the
pictures against the data instead of trusting them. Run it from the repository
root after `rnfo-collect pull`:

    python analysis/figures.py

It writes PNGs into docs/img/, two of each: one tuned for a light page and one
for a dark one, because GitHub renders README on either.
"""

import collections
import glob
import json
import os
import sys

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.ticker import FuncFormatter

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "docs", "img")

SUBJECTS = ["ru-mow-home", "ru-msk-vps", "de-fra-vps"]
CONTROL = "nl-lim-panel"

LABEL = {
    "ru-mow-home": "Moscow, residential (AS8402)",
    "ru-msk-vps": "Moscow, hosting (AS203273)",
    "de-fra-vps": "Frankfurt, hosting (AS210644)",
}

SHORT = {
    "ru-mow-home": "Moscow residential",
    "ru-msk-vps": "Moscow hosting",
    "de-fra-vps": "Frankfurt hosting",
}

THEMES = {
    "light": {
        "bg": "#ffffff",
        "fg": "#1f2328",
        "muted": "#656d76",
        "grid": "#d8dee4",
        "series": {"ru-mow-home": "#b4530a", "ru-msk-vps": "#1a7f64", "de-fra-vps": "#6e7781"},
        "accent": "#b4530a",
        "second": "#1a7f64",
    },
    "dark": {
        "bg": "#0d1117",
        "fg": "#e6edf3",
        "muted": "#8b949e",
        "grid": "#21262d",
        "series": {"ru-mow-home": "#e8a33d", "ru-msk-vps": "#4ec99a", "de-fra-vps": "#8b949e"},
        "accent": "#e8a33d",
        "second": "#4ec99a",
    },
}


def day_files(probe):
    pattern = os.path.join(ROOT, "data", probe, "measurements", "2026-*.jsonl")
    return sorted(p for p in glob.glob(pattern) if "live-" not in os.path.basename(p))


def read_rows(probe, want_full=True):
    """First attempts only, one row per (run_id, target)."""
    for path in day_files(probe):
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                line = line.strip()
                if not line:
                    continue
                try:
                    row = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if row.get("attempt", 1) != 1:
                    continue
                if want_full and not str(row.get("run_id", "")).endswith("/full"):
                    continue
                if row.get("list") == "own":
                    continue
                yield row


def paired_by_slot():
    """slot -> {probe -> {target -> verdict}} for every full slot we have."""
    table = collections.defaultdict(lambda: collections.defaultdict(dict))
    for probe in SUBJECTS + [CONTROL]:
        for row in read_rows(probe):
            table[row["run_id"]][probe][row["url"]] = row["verdict"]
    return table


def subject_only_by_day(table):
    """day -> probe -> share of paired targets that failed only from the subject."""
    hits = collections.defaultdict(lambda: collections.defaultdict(lambda: [0, 0]))
    for slot, probes in table.items():
        control = probes.get(CONTROL)
        if not control:
            continue
        day = slot.split("T")[0]
        for probe in SUBJECTS:
            rows = probes.get(probe)
            if not rows:
                continue
            for url, verdict in rows.items():
                peer = control.get(url)
                if peer is None:
                    continue
                hits[day][probe][1] += 1
                if verdict != "ok" and peer == "ok":
                    hits[day][probe][0] += 1
    out = {}
    for day, probes in hits.items():
        out[day] = {p: (n / total * 100.0) for p, (n, total) in probes.items() if total}
    return out


def stalls_histogram():
    """bytes_read of response_timeout rows that only the residential probe lost."""
    control = {}
    for row in read_rows(CONTROL):
        control[(row["run_id"], row["url"])] = row["verdict"]

    edges = [0, 8, 20, 32, 64, 1 << 30]
    names = ["<8 KiB", "8-20", "20-32", "32-64", ">64"]
    counts = {"ru-mow-home": [0] * 5, "ru-msk-vps": [0] * 5}
    for probe in counts:
        for row in read_rows(probe):
            if row.get("verdict") != "response_timeout":
                continue
            if control.get((row["run_id"], row["url"])) != "ok":
                continue
            kib = (row.get("bytes_read") or 0) / 1024.0
            for i in range(5):
                if edges[i] <= kib < edges[i + 1]:
                    counts[probe][i] += 1
                    break
    return names, counts


def mechanisms():
    """Subject-only failures by verdict, residential versus hosting."""
    control = {}
    for row in read_rows(CONTROL):
        control[(row["run_id"], row["url"])] = row["verdict"]
    out = {p: collections.Counter() for p in ("ru-mow-home", "ru-msk-vps")}
    for probe in out:
        for row in read_rows(probe):
            if row.get("verdict") == "ok":
                continue
            if control.get((row["run_id"], row["url"])) != "ok":
                continue
            out[probe][row["verdict"]] += 1
    return out


def style(theme):
    t = THEMES[theme]
    plt.rcParams.update(
        {
            "figure.facecolor": t["bg"],
            "axes.facecolor": t["bg"],
            "savefig.facecolor": t["bg"],
            "text.color": t["fg"],
            "axes.labelcolor": t["muted"],
            "xtick.color": t["muted"],
            "ytick.color": t["muted"],
            "axes.edgecolor": t["grid"],
            "grid.color": t["grid"],
            "font.size": 11,
            "font.family": "DejaVu Sans",
            "axes.spines.top": False,
            "axes.spines.right": False,
        }
    )
    return t


def save(fig, name, theme):
    os.makedirs(OUT, exist_ok=True)
    path = os.path.join(OUT, "%s-%s.png" % (name, theme))
    fig.savefig(path, dpi=180, bbox_inches="tight")
    plt.close(fig)
    print("wrote", os.path.relpath(path, ROOT))


def fig_gap(by_day, theme):
    t = style(theme)
    days = sorted(by_day)
    fig, ax = plt.subplots(figsize=(9, 4.2))
    for probe in SUBJECTS:
        ys = [by_day[d].get(probe) for d in days]
        xs = [i for i, y in enumerate(ys) if y is not None]
        vals = [y for y in ys if y is not None]
        if not vals:
            continue
        ax.plot(xs, vals, marker="o", markersize=4.5, linewidth=2.2,
                color=t["series"][probe], label=SHORT[probe])
        ax.annotate("%.0f%%" % vals[-1], (xs[-1], vals[-1]), textcoords="offset points",
                    xytext=(8, -3), color=t["series"][probe], fontsize=10, fontweight="bold")
    ax.set_xticks(range(len(days)))
    ax.set_xticklabels([d[5:] for d in days], fontsize=9)
    ax.yaxis.set_major_formatter(FuncFormatter(lambda v, _: "%d%%" % v))
    ax.set_ylim(0, max(45, max(max(v.values()) for v in by_day.values()) + 6))
    ax.grid(axis="y", linewidth=0.8)
    ax.set_axisbelow(True)
    ax.set_title("Targets that fail only from this probe, against the Dutch control",
                 loc="left", fontsize=12.5, fontweight="bold", color=t["fg"], pad=34)
    ax.set_xlabel("day, 2026", labelpad=8)
    leg = ax.legend(frameon=False, ncol=3, fontsize=9.5, loc="upper left",
                    bbox_to_anchor=(-0.01, 1.14), borderaxespad=0, columnspacing=2.4,
                    handletextpad=0.6)
    for text in leg.get_texts():
        text.set_color(t["muted"])
    save(fig, "gap", theme)


def fig_stalls(names, counts, theme):
    t = style(theme)
    fig, ax = plt.subplots(figsize=(9, 4.2))
    width = 0.38
    xs = range(len(names))
    for i, (probe, colour) in enumerate((("ru-mow-home", t["accent"]), ("ru-msk-vps", t["second"]))):
        total = sum(counts[probe]) or 1
        vals = [c / total * 100.0 for c in counts[probe]]
        pos = [x + (i - 0.5) * width for x in xs]
        ax.bar(pos, vals, width=width, color=colour, label="%s, n=%d" % (LABEL[probe], total))
        for x, v in zip(pos, vals):
            if v >= 3:
                ax.text(x, v + 1.4, "%.0f%%" % v, ha="center", fontsize=9,
                        color=colour, fontweight="bold")
    ax.set_xticks(list(xs))
    ax.set_xticklabels(names)
    ax.yaxis.set_major_formatter(FuncFormatter(lambda v, _: "%d%%" % v))
    ax.set_ylim(0, 100)
    ax.grid(axis="y", linewidth=0.8)
    ax.set_axisbelow(True)
    ax.set_title("Where a stalled response stops",
                 loc="left", fontsize=12.5, fontweight="bold", color=t["fg"], pad=14)
    ax.set_xlabel("bytes received before the connection went silent", labelpad=8)
    leg = ax.legend(frameon=False, loc="upper right", fontsize=9.5)
    for text in leg.get_texts():
        text.set_color(t["muted"])
    save(fig, "stalls", theme)


def fig_mechanisms(data, theme):
    t = style(theme)
    keep = ["tls_timeout", "response_timeout", "connect_timeout", "request_timeout", "tls_reset"]
    fig, ax = plt.subplots(figsize=(9, 3.8))
    height = 0.38
    ys = range(len(keep))
    for i, (probe, colour) in enumerate((("ru-mow-home", t["accent"]), ("ru-msk-vps", t["second"]))):
        total = sum(data[probe].values()) or 1
        vals = [data[probe].get(k, 0) / total * 100.0 for k in keep]
        pos = [y + (0.5 - i) * height for y in ys]
        ax.barh(pos, vals, height=height, color=colour, label=LABEL[probe])
        for y, v in zip(pos, vals):
            if v >= 2:
                ax.text(v + 1.2, y, "%.0f%%" % v, va="center", fontsize=9,
                        color=colour, fontweight="bold")
    ax.set_yticks(list(ys))
    ax.set_yticklabels(keep, fontsize=10)
    ax.invert_yaxis()
    ax.xaxis.set_major_formatter(FuncFormatter(lambda v, _: "%d%%" % v))
    ax.set_xlim(0, 100)
    ax.grid(axis="x", linewidth=0.8)
    ax.set_axisbelow(True)
    ax.set_title("How those failures happen, share of each probe's subject-only failures",
                 loc="left", fontsize=12.5, fontweight="bold", color=t["fg"], pad=14)
    leg = ax.legend(frameon=False, loc="lower right", fontsize=9.5)
    for text in leg.get_texts():
        text.set_color(t["muted"])
    save(fig, "mechanisms", theme)


def main():
    if not day_files(CONTROL):
        sys.exit("no sealed day files under data/; run `rnfo-collect pull` first")
    table = paired_by_slot()
    by_day = subject_only_by_day(table)
    names, counts = stalls_histogram()
    mech = mechanisms()

    for theme in THEMES:
        fig_gap(by_day, theme)
        fig_stalls(names, counts, theme)
        fig_mechanisms(mech, theme)

    print("\nnumbers behind the figures:")
    for day in sorted(by_day):
        parts = ["%s %.1f%%" % (p, v) for p, v in sorted(by_day[day].items())]
        print(" ", day, " ".join(parts))
    for probe in counts:
        total = sum(counts[probe]) or 1
        print(" ", probe, "stalls n=%d" % total,
              " ".join("%s %.0f%%" % (n, c / total * 100) for n, c in zip(names, counts[probe])))


if __name__ == "__main__":
    main()
