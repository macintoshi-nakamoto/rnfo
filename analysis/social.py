#!/usr/bin/env python3
"""Draw the repository's social preview card, 1280x640, from the measured data.

The background is not decoration. Each horizontal line is one real connection
that the residential probe lost mid-response, and its length is how many bytes
arrived before the line went silent. Sorted, so the band where they stop is the
shape of the finding itself.

    python analysis/social.py
"""

import os
import sys

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.lines import Line2D

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from figures import CONTROL, ROOT, read_rows  # noqa: E402

OUT = os.path.join(ROOT, "docs", "img", "social-preview.png")

BG = "#0d1117"
FG = "#e6edf3"
MUTED = "#8b949e"
AMBER = "#e8a33d"
GREEN = "#4ec99a"
GRID = "#1b2029"

LINES = 42
CAP_KIB = 72.0


def stalls():
    """bytes_read, in KiB, of residential stalls the Dutch control read fine."""
    control = {}
    for row in read_rows(CONTROL):
        control[(row["run_id"], row["url"])] = row["verdict"]
    out = []
    for row in read_rows("ru-mow-home"):
        if row.get("verdict") != "response_timeout":
            continue
        if control.get((row["run_id"], row["url"])) != "ok":
            continue
        out.append((row.get("bytes_read") or 0) / 1024.0)
    return sorted(out)


def sample(values, n):
    if len(values) <= n:
        return values
    step = len(values) / float(n)
    return [values[int(i * step)] for i in range(n)]


def main():
    values = stalls()
    if not values:
        sys.exit("no stalled rows found; run `rnfo-collect pull` first")
    rows = sample(values, LINES)

    fig = plt.figure(figsize=(12.8, 6.4), dpi=100)
    fig.patch.set_facecolor(BG)

    ax = fig.add_axes([0.0, 0.0, 1.0, 1.0])
    ax.set_facecolor(BG)
    ax.set_xlim(0, 100)
    ax.set_ylim(0, 100)
    ax.axis("off")

    field_x0, field_x1 = 6.0, 78.0
    field_y0, field_y1 = 16.0, 50.0
    span = field_x1 - field_x0

    def kib_to_x(kib):
        return field_x0 + min(kib, CAP_KIB) / CAP_KIB * span

    band_a, band_b = kib_to_x(20), kib_to_x(32)
    for x in (band_a, band_b):
        ax.plot([x, x], [field_y0 - 1.2, field_y1 + 1.2], color=AMBER, linewidth=1,
                linestyle=(0, (2, 3)), alpha=0.5)
    ax.annotate("", xy=(band_a, field_y1 + 3.4), xytext=(band_b, field_y1 + 3.4),
                arrowprops=dict(arrowstyle="<->", color=AMBER, linewidth=1.1, alpha=0.9))
    ax.text((band_a + band_b) / 2.0, field_y1 + 4.6,
            "73% stop in this band, 20 to 32 KiB", color=AMBER, fontsize=12,
            ha="center", va="bottom", fontweight="bold")

    step = (field_y1 - field_y0) / float(len(rows))
    for i, kib in enumerate(rows):
        y = field_y0 + i * step + step / 2.0
        ax.plot([field_x0, kib_to_x(kib)], [y, y], color=AMBER, linewidth=2.6,
                alpha=0.92, solid_capstyle="butt")
        ax.plot([kib_to_x(kib), kib_to_x(kib) + 1.6], [y, y], color=AMBER,
                linewidth=2.6, alpha=0.16, solid_capstyle="butt")

    ctrl = kib_to_x(56)
    ax.plot([ctrl, ctrl], [field_y0 - 1.2, field_y1 + 1.2], color=GREEN, linewidth=1.7,
            linestyle=(0, (5, 4)), alpha=0.8)
    ax.text(ctrl + 1.6, field_y1 - 1.0,
            "the Dutch control read\nthe same responses on\nto a median of 56 KiB",
            color=GREEN, fontsize=11.5, va="top", linespacing=1.6)

    ax.text(field_x0, field_y0 - 3.6, "bytes received before the connection went silent",
            color=MUTED, fontsize=11, va="top", alpha=0.8)

    ax.plot([field_x0, 94.0], [58.0, 58.0], color=GRID, linewidth=1)

    ax.text(field_x0, 88.0, "Russian Network Filtering Observatory",
            color=FG, fontsize=33, fontweight="bold", va="center")
    ax.text(field_x0, 79.0,
            "An open, longitudinal dataset of how network filtering behaves inside Russia",
            color=MUTED, fontsize=15.5, va="center")
    ax.text(field_x0, 68.5,
            "Every bar below is one real connection a Moscow home line lost mid-response.\n"
            "Its length is how many bytes arrived before the line went silent.",
            color=MUTED, fontsize=12.5, va="center", linespacing=1.6, alpha=0.82)

    legend = [
        Line2D([], [], color=AMBER, linewidth=3,
               label="%s stalled responses, residential AS8402" % format(len(values), ",d")),
        Line2D([], [], color=GREEN, linewidth=2, linestyle=(0, (4, 3)),
               label="the Dutch control on the same responses"),
    ]
    leg = ax.legend(handles=legend, loc="upper left", bbox_to_anchor=(0.635, 0.30),
                    frameon=False, fontsize=11.5, handlelength=2.4, labelspacing=0.9)
    for text in leg.get_texts():
        text.set_color(MUTED)

    ax.text(field_x0, 5.0, "github.com/macintoshi-nakamoto/rnfo",
            color=MUTED, fontsize=11.5, va="center", alpha=0.75)
    ax.text(94.0, 5.0, "four vantage points  ·  twelve sealed days  ·  MIT + CC BY 4.0",
            color=MUTED, fontsize=11.5, va="center", ha="right", alpha=0.6)

    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    fig.savefig(OUT, facecolor=BG, dpi=100)
    plt.close(fig)
    print("wrote %s (%d stalls, %d lines drawn)" % (os.path.relpath(OUT, ROOT), len(values), len(rows)))


if __name__ == "__main__":
    main()
