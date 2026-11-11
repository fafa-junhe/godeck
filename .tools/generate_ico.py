#!/usr/bin/env python3
import subprocess
import os
from PIL import Image

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
PROJECT_ROOT = os.path.dirname(SCRIPT_DIR)
SVG_PATH = os.path.join(PROJECT_ROOT, "packaging", "arch", "godeck.svg")
ICO_PATH = os.path.join(PROJECT_ROOT, "packaging", "godeck.ico")

os.makedirs(os.path.dirname(ICO_PATH), exist_ok=True)

sizes = [256, 128, 64, 48, 32, 16]
imgs = []
for s in sizes:
    png_path = f"/tmp/godeck_{s}.png"
    subprocess.run(["/usr/sbin/rsvg-convert", "-w", str(s), "-h", str(s), SVG_PATH, "-o", png_path], check=True)
    imgs.append(Image.open(png_path))

# Save multi-size ICO
imgs[0].save(ICO_PATH, format="ICO", sizes=[(s, s) for s in sizes], append_images=imgs[1:])
print(f"Generated {ICO_PATH} with sizes: {sizes}")
