"""Render the AgentPack app icon to PNG (1024) and multi-size ICO.

Design: Single bold gesture - a thick off-white diagonal bar (the "path/agent")
passing through a centered electric-blue circle (the "hub/core").

  - Off-black squircle background (subtle vertical gradient, NOT pure black)
  - 2px inner edge highlight (white at 8%) for physical refraction
  - Diagonal bar: corner-to-corner (bottom-left -> top-right), off-white #fafafa,
    width 96, round caps. Drawn FIRST.
  - Blue hub circle: centered, electric blue #3b82f6, radius 150.
    Drawn ON TOP of the bar, so the bar appears to pass behind the hub.
  - The bar pokes out on both sides of the circle, creating a "path through hub" silhouette.
  - No glow, no outlines, no labels. Single confident gesture.
Mirrors build/icon.svg so both vector and bitmap outputs match.
"""
import os
from PIL import Image, ImageDraw

SIZE = 1024
OUT_DIR = os.path.dirname(os.path.abspath(__file__))


def lerp(a, b, t):
    return a + (b - a) * t


def lerp_color(c1, c2, t):
    return tuple(int(round(lerp(c1[i], c2[i], t))) for i in range(3))


def make_vertical_gradient_bg(size, c_top, c_bottom):
    """Vertical gradient top->bottom."""
    img = Image.new("RGB", (size, size), c_top)
    px = img.load()
    for y in range(size):
        t = y / max(1, size - 1)
        c = lerp_color(c_top, c_bottom, t)
        for x in range(size):
            px[x, y] = c
    return img


def make_squircle_mask(size, radius):
    """Rounded square mask with given corner radius."""
    mask = Image.new("L", (size, size), 0)
    d = ImageDraw.Draw(mask)
    d.rounded_rectangle((0, 0, size - 1, size - 1), radius=radius, fill=255)
    return mask


def composite_rounded_line(base, p1, p2, color, width, opacity=1.0):
    """Composite a line with round caps onto base.
    Pillow's line() has no cap support, so we draw circles at both endpoints.
    """
    layer = Image.new("RGBA", base.size, (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    a = int(round(255 * opacity))
    d.line([p1, p2], fill=(*color, a), width=width)
    r = width // 2
    d.ellipse((p1[0] - r, p1[1] - r, p1[0] + r, p1[1] + r), fill=(*color, a))
    d.ellipse((p2[0] - r, p2[1] - r, p2[0] + r, p2[1] + r), fill=(*color, a))
    return Image.alpha_composite(base, layer)


def composite_solid_circle(base, center, r, fill, opacity=1.0):
    """Composite a solid-color circle onto base."""
    layer = Image.new("RGBA", base.size, (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    cx, cy = center
    a = int(round(255 * opacity))
    d.ellipse((cx - r, cy - r, cx + r, cy + r), fill=(*fill, a))
    return Image.alpha_composite(base, layer)


def composite_rounded_stroke(base, radius, stroke_width, color, opacity):
    """Composite a stroked rounded-rectangle outline onto base."""
    layer = Image.new("RGBA", base.size, (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    half = stroke_width // 2
    a = int(round(255 * opacity))
    d.rounded_rectangle(
        (half, half, base.size[0] - 1 - half, base.size[1] - 1 - half),
        radius=max(0, radius - half),
        outline=(*color, a),
        width=stroke_width,
    )
    return Image.alpha_composite(base, layer)


def render_icon(size):
    """Render the icon at a given pixel size; return RGBA Image."""
    s = size
    k = s / 1024.0

    # 1. Off-black vertical gradient background, clipped to squircle
    bg = make_vertical_gradient_bg(s, (26, 26, 31), (10, 10, 13)).convert("RGBA")
    radius = int(round(228 * k))
    mask = make_squircle_mask(s, radius)
    out = Image.new("RGBA", (s, s), (0, 0, 0, 0))
    out.paste(bg, (0, 0), mask)

    # 2. Inner edge highlight: 2px white at 8%
    ring_w = max(1, int(round(2 * k)))
    out = composite_rounded_stroke(out, radius, ring_w, (255, 255, 255), 0.08)

    # 3. Diagonal bar: off-white, corner-to-corner, drawn FIRST (behind circle)
    # from (200, 824) to (824, 200), width 96, round caps
    bar_color = (250, 250, 250)   # #fafafa, off-white (not pure white)
    bar_w = max(2, int(round(96 * k)))
    p1 = (200 * k, 824 * k)
    p2 = (824 * k, 200 * k)
    out = composite_rounded_line(out, p1, p2, bar_color, bar_w)

    # 4. Blue hub circle: centered, drawn ON TOP of the bar
    # center (512, 512), radius 150
    hub_cx = int(round(512 * k))
    hub_cy = int(round(512 * k))
    hub_r = max(1, int(round(150 * k)))
    out = composite_solid_circle(out, (hub_cx, hub_cy), hub_r, (59, 130, 246), opacity=1.0)

    return out


def main():
    # 1024 PNG
    big = render_icon(1024)
    png_path = os.path.join(OUT_DIR, "appicon.png")
    big.save(png_path, "PNG")
    print(f"wrote {png_path} ({big.size})")

    # Multi-size ICO
    ico_sizes = [(16, 16), (24, 24), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)]
    ico_base = render_icon(256)
    ico_path = os.path.join(OUT_DIR, "windows", "icon.ico")
    os.makedirs(os.path.dirname(ico_path), exist_ok=True)
    ico_base.save(ico_path, format="ICO", sizes=ico_sizes)
    print(f"wrote {ico_path} (sizes={[s[0] for s in ico_sizes]})")


if __name__ == "__main__":
    main()
