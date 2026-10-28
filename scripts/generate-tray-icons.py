#!/usr/bin/env python3
"""Export monochrome @2x tray logos from the existing Agent SVGs (librsvg)."""
from pathlib import Path
import subprocess
import xml.etree.ElementTree as ET

root = Path(__file__).resolve().parents[1]
destination = root / "assets/tray"
destination.mkdir(exist_ok=True)
for agent in ("codex", "claude"):
    svg = ET.fromstring((root / f"assets/agents/{agent}.svg").read_bytes())
    for node in svg.iter():
        if node.get("fill") not in (None, "none"):
            node.set("fill", "#000000")
    # 16 DIP artwork centered in an 18 DIP status item, exported at 2x.
    svg.set("x", "2")
    svg.set("y", "2")
    svg.set("width", "32")
    svg.set("height", "32")
    wrapper = ET.Element("{http://www.w3.org/2000/svg}svg", {
        "width": "36", "height": "36", "viewBox": "0 0 36 36"
    })
    wrapper.append(svg)
    subprocess.run([
        "rsvg-convert", "--format", "png", "--output",
        str(destination / f"{agent}.png")
    ], input=ET.tostring(wrapper), check=True)
