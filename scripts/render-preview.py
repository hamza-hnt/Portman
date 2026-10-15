#!/usr/bin/env python3
"""Render the real TUI's exported ANSI frames as standalone SVGs (no deps)."""
import html
import pathlib
import re
import sys
import unicodedata

SGR = re.compile(r"\x1b\[([0-9;]*)m")
# A neutral sample terminal theme; the TUI itself leaves the background unset.
TERMINAL_BG = "#171717"
PALETTE = [TERMINAL_BG, "#FF8495", "#70E1C3", "#F4C77E", "#7CABEB", "#B6A0F5", "#70D7E1", "#E4E7F2",
           "#858CA5", "#FF8495", "#70E1C3", "#F4C77E", "#7CABEB", "#B6A0F5", "#70D7E1", "#FFFFFF"]


def indexed(n):
    if n < 16:
        return PALETTE[n]
    if n >= 232:
        return "#{0:02x}{0:02x}{0:02x}".format(8 + 10 * (n - 232))
    n -= 16
    ramp = [0, 95, 135, 175, 215, 255]
    return "#%02x%02x%02x" % (ramp[n // 36], ramp[n // 6 % 6], ramp[n % 6])


def cells(text):
    return sum(0 if unicodedata.combining(c) else 2 if unicodedata.east_asian_width(c) in "WF" else 1 for c in text)


def render(source, destination):
    lines = source.read_text().splitlines()
    columns = max(cells(SGR.sub("", line)) for line in lines)
    cell, line_height, pad, top = 9, 23, 24, 58
    width, height = columns * cell + pad * 2, len(lines) * line_height + top + pad
    out = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}" role="img" aria-label="Portman terminal preview">',
           '<title>Portman — rendered from the production TUI with sample processes</title>',
           f'<rect width="{width}" height="{height}" rx="12" fill="{TERMINAL_BG}"/>',
           f'<path d="M0 42H{width}" stroke="#25313F"/>',
           '<circle cx="24" cy="22" r="5" fill="#FF8495"/><circle cx="43" cy="22" r="5" fill="#F4C77E"/><circle cx="62" cy="22" r="5" fill="#70E1C3"/>',
           f'<text x="{width/2}" y="27" text-anchor="middle" font-family="Menlo,monospace" font-size="12" fill="#858CA5">portman — local workspace</text>',
           '<g font-family="Menlo,DejaVu Sans Mono,monospace" font-size="15" xml:space="preserve">']
    state = dict(fg=None, bg=None, bold=False, reverse=False, underline=False)
    for row, line in enumerate(lines):
        x, last = pad, 0

        def text_span(text):
            nonlocal x
            if not text:
                return
            span_width = cells(text) * cell
            fg, bg = state["fg"] or "#E4E7F2", state["bg"]
            if state["reverse"]:
                fg, bg = bg or TERMINAL_BG, fg
            y = top + row * line_height
            if bg:
                out.append(f'<rect x="{x}" y="{y-16}" width="{span_width}" height="{line_height}" fill="{bg}"/>')
            if text.strip():
                weight = ' font-weight="700"' if state["bold"] else ""
                underline = ' text-decoration="underline"' if state["underline"] else ""
                glyph_x = x
                for glyph in text:
                    if not glyph.isspace():
                        out.append(f'<text x="{glyph_x}" y="{y}" fill="{fg}"{weight}{underline}>{html.escape(glyph)}</text>')
                    glyph_x += cells(glyph) * cell
            x += span_width

        for match in SGR.finditer(line):
            text_span(line[last:match.start()])
            codes = [int(v or 0) for v in match.group(1).split(";")]
            i = 0
            while i < len(codes):
                code = codes[i]
                if code == 0:
                    state.update(fg=None, bg=None, bold=False, reverse=False, underline=False)
                elif code in (1, 22):
                    state["bold"] = code == 1
                elif code in (4, 24):
                    state["underline"] = code == 4
                elif code in (7, 27):
                    state["reverse"] = code == 7
                elif code in (38, 48) and i + 2 < len(codes):
                    key = "fg" if code == 38 else "bg"
                    if codes[i+1] == 2 and i + 4 < len(codes):
                        state[key] = "#%02x%02x%02x" % tuple(codes[i+2:i+5])
                        i += 4
                    elif codes[i+1] == 5:
                        state[key] = indexed(codes[i+2])
                        i += 2
                elif code in (39, 49):
                    state["fg" if code == 39 else "bg"] = None
                elif 30 <= code <= 37:
                    state["fg"] = PALETTE[code-30]
                elif 40 <= code <= 47:
                    state["bg"] = PALETTE[code-40]
                i += 1
            last = match.end()
        text_span(line[last:])
    out += ["</g>", "</svg>"]
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text("\n".join(out) + "\n")


if __name__ == "__main__":
    render(pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2]))
