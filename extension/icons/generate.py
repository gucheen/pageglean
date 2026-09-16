"""Generate toolbar and extension listing icons; requires Pillow."""

from pathlib import Path

from PIL import Image, ImageDraw


def render(size):
    scale = 8
    unit = size * scale / 32
    image = Image.new("RGBA", (size * scale, size * scale))
    draw = ImageDraw.Draw(image)

    def box(values):
        return tuple(value * unit for value in values)

    draw.rounded_rectangle(box((1, 1, 31, 31)), radius=7 * unit, fill="#c84b2b")
    draw.rounded_rectangle(box((9, 6, 24, 25)), radius=2 * unit, fill="white")
    draw.line([box((6, 11)), box((6, 27)), box((20, 27))], fill="white", width=round(2 * unit))
    draw.polygon([box(point) for point in [(17, 6), (21, 6), (21, 14), (19, 12), (17, 14)]], fill="#c84b2b")
    draw.line([box((12, 18)), box((20, 18))], fill="#c84b2b", width=round(2 * unit))
    draw.line([box((12, 22)), box((17, 22))], fill="#c84b2b", width=round(2 * unit))
    return image.resize((size, size), Image.Resampling.LANCZOS)


if __name__ == "__main__":
    for size in (16, 32, 48, 128):
        render(size).save(Path(__file__).with_name(f"icon-{size}.png"))
