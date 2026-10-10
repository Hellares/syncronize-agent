"""Genera los recursos gráficos del instalador desde el ícono del agente.

    python instalador/generar-imagenes.py

Deja en instalador/:
  icono.ico, icono-beta.ico        ícono del Setup.exe (16 a 256 px)
  lateral.bmp, lateral@2x.bmp      imagen de la izquierda (bienvenida y final)
  chico.bmp, chico@2x.bmp          imagen de arriba a la derecha (resto de páginas)
Los .bmp son los mismos para beta y prod; la versión beta se nota en el título.
Requiere Pillow. Los archivos generados se guardan en el repo: el build no
necesita Python.
"""
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

aqui = Path(__file__).parent
iconos = aqui.parent / "cmd" / "syncronize-agent" / "icono"

AZUL_OSCURO = (8, 27, 58)
AZUL = (0, 74, 148)


def fuente(tam, negrita=False):
    nombre = "segoeuib.ttf" if negrita else "segoeui.ttf"
    try:
        return ImageFont.truetype(f"C:/Windows/Fonts/{nombre}", tam)
    except OSError:
        return ImageFont.load_default()


def degradado(ancho, alto):
    img = Image.new("RGB", (ancho, alto))
    d = ImageDraw.Draw(img)
    for y in range(alto):
        t = y / max(alto - 1, 1)
        color = tuple(round(a + (b - a) * t) for a, b in zip(AZUL_OSCURO, AZUL))
        d.line([(0, y), (ancho, y)], fill=color)
    return img


def lateral(escala):
    ancho, alto = 164 * escala, 314 * escala
    img = degradado(ancho, alto)
    logo = Image.open(iconos / "icono.png").convert("RGBA")
    lado = 120 * escala
    logo = logo.resize((lado, lado), Image.LANCZOS)
    img.paste(logo, ((ancho - lado) // 2, 56 * escala), logo)
    d = ImageDraw.Draw(img)
    for texto, y, f in (
        ("Syncronize", 196, fuente(19 * escala, True)),
        ("Agente de monitoreo", 222, fuente(11 * escala)),
    ):
        w = d.textlength(texto, font=f)
        d.text(((ancho - w) / 2, y * escala), texto, font=f, fill=(255, 255, 255))
    return img


def chico(escala):
    lado = 55 * escala
    img = Image.new("RGB", (lado, lado), (255, 255, 255))
    logo = Image.open(iconos / "icono.png").convert("RGBA").resize((lado, lado), Image.LANCZOS)
    img.paste(logo, (0, 0), logo)
    return img


for nombre, png in (("icono.ico", "icono.png"), ("icono-beta.ico", "icono-beta.png")):
    Image.open(iconos / png).convert("RGBA").save(
        aqui / nombre, sizes=[(16, 16), (24, 24), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)]
    )
lateral(1).save(aqui / "lateral.bmp")
lateral(2).save(aqui / "lateral@2x.bmp")
chico(1).save(aqui / "chico.bmp")
chico(2).save(aqui / "chico@2x.bmp")
print("listo")
